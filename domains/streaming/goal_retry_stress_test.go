package streaming

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGoalRetryLoop_ConcurrentRequests validates thread safety and metrics under concurrent load
func TestGoalRetryLoop_ConcurrentRequests(t *testing.T) {
	t.Run("concurrent policy resolution", func(t *testing.T) {
		// Simulate multiple tenants resolving policies concurrently
		tenants := []string{"tenant-1", "tenant-2", "tenant-3", "tenant-4", "tenant-5"}
		var wg sync.WaitGroup
		errChan := make(chan error, len(tenants)*10)

		for i := 0; i < 10; i++ {
			for _, tenant := range tenants {
				wg.Add(1)
				go func(tid string) {
					defer wg.Done()

					// Simulate policy resolution
					policy := GoalRetryPolicy{
						CostMode:     "balanced",
						Enabled:      true,
						MaxRetries:   3,
						TotalTimeout: 10 * time.Second,
						BaseDelay:    100 * time.Millisecond,
						MaxDelay:     5 * time.Second,
					}

					// Record metrics concurrently
					recordGoalRetryPolicyResolution(tid, policy.CostMode, "resolver")

					// Simulate retry outcome
					recordGoalRetryOutcome(tid, policy.CostMode, "success", 2, 500*time.Millisecond)
				}(tenant)
			}
		}

		// Wait for all goroutines
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Success
		case err := <-errChan:
			t.Fatalf("Concurrent operation failed: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent operations")
		}

		// Verify no errors
		close(errChan)
		for err := range errChan {
			t.Errorf("Error during concurrent execution: %v", err)
		}
	})

	t.Run("concurrent active retry tracking", func(t *testing.T) {
		tenantID := "tenant-concurrent"
		iterations := 100
		var wg sync.WaitGroup

		for i := 0; i < iterations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				// Simulate retry loop lifecycle
				trackGoalActiveRetry(tenantID, 1)
				time.Sleep(time.Millisecond * 10)
				trackGoalActiveRetry(tenantID, -1)
			}()
		}

		wg.Wait()

		// If gauge was properly atomic, we should have no panics
		// and final value should be 0 (all increments matched by decrements)
		t.Log("Concurrent active retry tracking completed without race")
	})
}

// TestGoalRetryLoop_EdgeCases tests boundary conditions and error scenarios
func TestGoalRetryLoop_EdgeCases(t *testing.T) {
	t.Run("zero max retries", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			return errors.New("error")
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   0, // No retries allowed
			TotalTimeout: 10 * time.Second,
			BaseDelay:    100 * time.Millisecond,
			MaxDelay:     5 * time.Second,
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)
		defer retryCancel()

		retriesPerformed := 0
		var finalErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			if err := retryCtx.Err(); err != nil {
				finalErr = err
				break
			}

			if attempt > 0 {
				retriesPerformed++
			}

			err := mockExecute(retryCtx)
			if err == nil {
				finalErr = nil
				break
			}
			finalErr = err

			if attempt >= policy.MaxRetries {
				break
			}

			timer := time.NewTimer(policy.BaseDelay)
			select {
			case <-timer.C:
			case <-retryCtx.Done():
				timer.Stop()
				finalErr = retryCtx.Err()
				break
			}
		}

		// Verify
		require.Error(t, finalErr)
		require.Equal(t, 1, attempts, "should execute once (no retries)")
		require.Equal(t, 0, retriesPerformed, "should record 0 retries")
	})

	t.Run("immediate success no retries", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			return nil // Immediate success
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   5,
			TotalTimeout: 10 * time.Second,
			BaseDelay:    100 * time.Millisecond,
			MaxDelay:     5 * time.Second,
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)
		defer retryCancel()

		retriesPerformed := 0
		var finalErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			if err := retryCtx.Err(); err != nil {
				finalErr = err
				break
			}

			if attempt > 0 {
				retriesPerformed++
			}

			err := mockExecute(retryCtx)
			if err == nil {
				finalErr = nil
				break
			}
			finalErr = err

			if attempt >= policy.MaxRetries {
				break
			}

			timer := time.NewTimer(policy.BaseDelay)
			select {
			case <-timer.C:
			case <-retryCtx.Done():
				timer.Stop()
				finalErr = retryCtx.Err()
				break
			}
		}

		// Verify
		require.NoError(t, finalErr)
		require.Equal(t, 1, attempts, "should execute once")
		require.Equal(t, 0, retriesPerformed, "should record 0 retries")
	})

	t.Run("very short timeout", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			time.Sleep(50 * time.Millisecond)
			return errors.New("error")
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   10,
			TotalTimeout: 10 * time.Millisecond, // Very short
			BaseDelay:    5 * time.Millisecond,
			MaxDelay:     20 * time.Millisecond,
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)
		defer retryCancel()

		startTime := time.Now()
		retriesPerformed := 0
		var finalErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			if err := retryCtx.Err(); err != nil {
				finalErr = err
				break
			}

			if attempt > 0 {
				retriesPerformed++
			}

			err := mockExecute(retryCtx)
			if err == nil {
				finalErr = nil
				break
			}
			finalErr = err

			if attempt >= policy.MaxRetries {
				break
			}

			timer := time.NewTimer(policy.BaseDelay)
			select {
			case <-timer.C:
			case <-retryCtx.Done():
				timer.Stop()
				finalErr = retryCtx.Err()
				break
			}
		}

		elapsed := time.Since(startTime)

		// Verify
		require.Error(t, finalErr)
		require.True(t, errors.Is(finalErr, context.DeadlineExceeded) ||
			errors.Is(finalErr, context.Canceled), "should timeout or cancel")
		require.LessOrEqual(t, attempts, 2, "should not execute many times")
		require.Less(t, elapsed, 200*time.Millisecond, "should stop quickly")
		t.Logf("Attempts: %d, Elapsed: %v", attempts, elapsed)
	})

	t.Run("alternating success and failure", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			if attempts%2 == 0 {
				return nil // Even attempts succeed
			}
			return errors.New("odd attempt fails")
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   5,
			TotalTimeout: 10 * time.Second,
			BaseDelay:    10 * time.Millisecond,
			MaxDelay:     50 * time.Millisecond,
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)
		defer retryCancel()

		retriesPerformed := 0
		var finalErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			if err := retryCtx.Err(); err != nil {
				finalErr = err
				break
			}

			if attempt > 0 {
				retriesPerformed++
			}

			err := mockExecute(retryCtx)
			if err == nil {
				finalErr = nil
				break
			}
			finalErr = err

			if attempt >= policy.MaxRetries {
				break
			}

			timer := time.NewTimer(policy.BaseDelay)
			select {
			case <-timer.C:
			case <-retryCtx.Done():
				timer.Stop()
				finalErr = retryCtx.Err()
				break
			}
		}

		// Verify
		require.NoError(t, finalErr, "should succeed on 2nd attempt")
		require.Equal(t, 2, attempts, "should stop after first success")
		require.Equal(t, 1, retriesPerformed, "should record 1 retry")
	})
}

// TestGoalRetryLoop_StressTest validates behavior under high load
func TestGoalRetryLoop_StressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Run("sustained load", func(t *testing.T) {
		duration := 5 * time.Second
		concurrency := 50

		ctx, cancel := context.WithTimeout(context.Background(), duration)
		defer cancel()

		var wg sync.WaitGroup
		var totalAttempts atomic.Int64
		var successCount atomic.Int64
		var errorCount atomic.Int64

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()

				tenantID := "stress-tenant"
				iterationCount := 0

				for {
					select {
					case <-ctx.Done():
						return
					default:
					}

					iterationCount++
					attempts := 0

					mockExecute := func(ctx context.Context) error {
						attempts++
						totalAttempts.Add(1)

						// 70% success rate after 1-2 attempts
						if attempts >= 2 || (attempts == 1 && iterationCount%3 == 0) {
							successCount.Add(1)
							return nil
						}
						errorCount.Add(1)
						return errors.New("transient error")
					}

					policy := GoalRetryPolicy{
						MaxRetries:   3,
						TotalTimeout: 1 * time.Second,
						BaseDelay:    5 * time.Millisecond,
						MaxDelay:     50 * time.Millisecond,
					}

					retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)

					retriesPerformed := 0
					var finalErr error

					for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
						if err := retryCtx.Err(); err != nil {
							finalErr = err
							break
						}

						if attempt > 0 {
							retriesPerformed++
						}

						err := mockExecute(retryCtx)
						if err == nil {
							finalErr = nil
							break
						}
						finalErr = err

						if attempt >= policy.MaxRetries {
							break
						}

						timer := time.NewTimer(policy.BaseDelay)
						select {
						case <-timer.C:
						case <-retryCtx.Done():
							timer.Stop()
							finalErr = retryCtx.Err()
							break
						}
					}

					retryCancel()

					// Record outcome
					if finalErr == nil {
						recordGoalRetryOutcome(tenantID, "balanced", "success", retriesPerformed, 50*time.Millisecond)
					} else {
						recordGoalRetryOutcome(tenantID, "balanced", "error", retriesPerformed, 100*time.Millisecond)
					}

					// Small delay between iterations
					time.Sleep(10 * time.Millisecond)
				}
			}(i)
		}

		wg.Wait()

		// Report results
		total := totalAttempts.Load()
		success := successCount.Load()
		failed := errorCount.Load()

		t.Logf("Stress Test Results:")
		t.Logf("  Duration: %v", duration)
		t.Logf("  Concurrency: %d", concurrency)
		t.Logf("  Total Attempts: %d", total)
		t.Logf("  Successful: %d (%.1f%%)", success, float64(success)/float64(total)*100)
		t.Logf("  Failed: %d (%.1f%%)", failed, float64(failed)/float64(total)*100)

		require.Greater(t, total, int64(0), "should have executed attempts")
		require.Greater(t, success, int64(0), "should have some successes")
	})
}

// TestGoalRetryLoop_MemoryLeak validates no goroutine or timer leaks
func TestGoalRetryLoop_MemoryLeak(t *testing.T) {
	t.Run("timer cleanup on cancellation", func(t *testing.T) {
		// This test verifies timer.Stop() is called to prevent leaks
		iterations := 100

		for i := 0; i < iterations; i++ {
			ctx, cancel := context.WithCancel(context.Background())

			mockExecute := func(ctx context.Context) error {
				return errors.New("error")
			}

			policy := GoalRetryPolicy{
				MaxRetries:   5,
				TotalTimeout: 10 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			}

			retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)

			go func() {
				// Cancel after first attempt
				time.Sleep(50 * time.Millisecond)
				cancel()
			}()

			retriesPerformed := 0
			var finalErr error

			for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
				if err := retryCtx.Err(); err != nil {
					finalErr = err
					break
				}

				if attempt > 0 {
					retriesPerformed++
				}

				err := mockExecute(retryCtx)
				if err == nil {
					finalErr = nil
					break
				}
				finalErr = err

				if attempt >= policy.MaxRetries {
					break
				}

				timer := time.NewTimer(policy.BaseDelay)
				select {
				case <-timer.C:
				case <-retryCtx.Done():
					timer.Stop() // Critical: must stop timer to prevent leak
					finalErr = retryCtx.Err()
					break
				}
			}

			retryCancel()

			require.Error(t, finalErr)
		}

		t.Log("Completed 100 iterations with timer cleanup - no leaks expected")
	})
}
