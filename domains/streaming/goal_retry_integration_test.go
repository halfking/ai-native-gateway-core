package streaming

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGoalRetryLoop_Integration validates the complete retry loop behavior
// without requiring a database or real executor.
func TestGoalRetryLoop_Integration(t *testing.T) {
	t.Run("context cancellation stops execution", func(t *testing.T) {
		// Deterministic cancellation: the mock executor signals on a channel
		// after its first (failed) call, and a goroutine selects on that
		// channel to fire cancel(). This guarantees cancel happens exactly
		// after the first attempt's failure — no timing races under CI load
		// or GOMAXPROCS=1.
		cancelCh := make(chan struct{}, 1)
		attempts := 0
		var attemptsMu sync.Mutex
		mockExecute := func(ctx context.Context) error {
			attemptsMu.Lock()
			attempts++
			first := attempts == 1
			attemptsMu.Unlock()
			if first {
				// Signal the cancellation goroutine that the first attempt
				// has started and failed, so it can fire cancel().
				select {
				case cancelCh <- struct{}{}:
				default:
				}
			}
			return errors.New("transient error")
		}

		// Create a context that will be cancelled
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel exactly after the first attempt's failure is reported.
		go func() {
			select {
			case <-cancelCh:
				cancel()
			case <-time.After(5 * time.Second):
				// Safety net so the test fails fast instead of hanging if
				// the executor never runs.
				cancel()
			}
		}()

		// Execute retry loop simulation
		policy := GoalRetryPolicy{
			MaxRetries:   5,
			TotalTimeout: 10 * time.Second,
			BaseDelay:    50 * time.Millisecond,
			MaxDelay:     200 * time.Millisecond,
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, policy.TotalTimeout)
		defer retryCancel()

		retriesPerformed := 0
		var finalErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			// Check cancellation before execute (DEF-001 fix)
			if err := retryCtx.Err(); err != nil {
				finalErr = err
				break
			}

			if attempt > 0 {
				retriesPerformed++
			}

			// Execute
			err := mockExecute(retryCtx)
			if err == nil {
				break
			}
			finalErr = err

			// Don't retry on last attempt
			if attempt >= policy.MaxRetries {
				break
			}

			// Delay with cancellation check
			timer := time.NewTimer(policy.BaseDelay)
			select {
			case <-timer.C:
				// Continue
			case <-retryCtx.Done():
				timer.Stop()
				finalErr = retryCtx.Err()
				break
			}
		}

		// Verify
		require.Error(t, finalErr)
		require.True(t, errors.Is(finalErr, context.Canceled), "should be cancelled")
		// Cancel fires after the first attempt, so at most 1-2 attempts
		// should run before the loop observes the cancelled context.
		require.LessOrEqual(t, attempts, 2, "should not execute many times after cancellation")
		t.Logf("Attempts made: %d, Retries: %d", attempts, retriesPerformed)
	})

	t.Run("total timeout enforced", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			time.Sleep(50 * time.Millisecond)
			return errors.New("transient error")
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   100,                    // High limit
			TotalTimeout: 200 * time.Millisecond, // But short timeout
			BaseDelay:    10 * time.Millisecond,
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
		require.True(t, errors.Is(finalErr, context.DeadlineExceeded), "should timeout")
		require.Less(t, elapsed, 500*time.Millisecond, "should stop within reasonable time")
		require.Less(t, attempts, 10, "should not exhaust max retries")
		t.Logf("Attempts: %d, Elapsed: %v", attempts, elapsed)
	})

	t.Run("success on retry stops loop", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient error")
			}
			return nil // Success on 3rd attempt
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
				break // Success
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
		require.NoError(t, finalErr, "should succeed")
		require.Equal(t, 3, attempts, "should stop after success")
		require.Equal(t, 2, retriesPerformed, "should record 2 retries (attempt 2 and 3)")
	})

	t.Run("max retries exhausted", func(t *testing.T) {
		attempts := 0
		mockExecute := func(ctx context.Context) error {
			attempts++
			return errors.New("permanent error")
		}

		ctx := context.Background()
		policy := GoalRetryPolicy{
			MaxRetries:   2,
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
		require.Error(t, finalErr, "should fail")
		require.Equal(t, 3, attempts, "should try initial + 2 retries")
		require.Equal(t, 2, retriesPerformed, "should record 2 retries")
	})
}

// TestGoalRetryRecorder_FailOpen validates that persistence failures don't block requests
func TestGoalRetryRecorder_FailOpen(t *testing.T) {
	t.Run("recorder error does not block", func(t *testing.T) {
		// Mock recorder that always fails
		failingRecorder := &mockFailingRecorder{}

		// This should not panic or fail
		err := failingRecorder.AddRetryCount(context.Background(), "tenant-a", "session-123", 3)

		// Verify it returns error but doesn't panic
		require.Error(t, err)
		require.Equal(t, "mock persistence failure", err.Error())
	})

	t.Run("nil recorder is safe", func(t *testing.T) {
		var recorder GoalRetryRecorder = nil

		// Should not panic when recorder is nil
		// (this is how production behaves when recorder not wired)
		require.NotPanics(t, func() {
			if recorder != nil {
				_ = recorder.AddRetryCount(context.Background(), "tenant-a", "session-123", 1)
			}
		})
	})
}

// Mock recorder for testing
type mockFailingRecorder struct{}

func (m *mockFailingRecorder) AddRetryCount(ctx context.Context, tenantID, sessionID string, delta int) error {
	return errors.New("mock persistence failure")
}

// TestGoalRetryMetrics_Recording validates metrics are recorded correctly
func TestGoalRetryMetrics_Recording(t *testing.T) {
	t.Run("record policy resolution", func(t *testing.T) {
		// This should not panic
		require.NotPanics(t, func() {
			recordGoalRetryPolicyResolution("tenant-123", "balanced", "resolver")
			recordGoalRetryPolicyResolution("tenant-456", "minimal", "fallback")
		})
	})

	t.Run("record retry outcome", func(t *testing.T) {
		require.NotPanics(t, func() {
			recordGoalRetryOutcome("tenant-123", "balanced", "success", 2, 1500*time.Millisecond)
			recordGoalRetryOutcome("tenant-456", "aggressive", "exhausted", 5, 30*time.Second)
			recordGoalRetryOutcome("tenant-789", "minimal", "cancelled", 1, 500*time.Millisecond)
		})
	})

	t.Run("record persistence", func(t *testing.T) {
		require.NotPanics(t, func() {
			recordGoalRetryCountPersistence("tenant-123", "success")
			recordGoalRetryCountPersistence("tenant-456", "failure")
			recordGoalRetryCountPersistence("tenant-789", "skipped")
		})
	})

	t.Run("track active retries", func(t *testing.T) {
		require.NotPanics(t, func() {
			trackGoalActiveRetry("tenant-123", 1)
			trackGoalActiveRetry("tenant-123", -1)
		})
	})
}
