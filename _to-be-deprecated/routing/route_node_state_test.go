
package routing

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUsable_CoolingDown(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		Disabled:      true,
		DisabledUntil: now.Add(5 * time.Minute),
	}

	// Should not be usable while cooling down
	assert.False(t, state.IsUsable(now))
	assert.False(t, state.IsUsable(now.Add(4*time.Minute)))
}

func TestIsUsable_CooldownExpired(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		Disabled:      true,
		DisabledUntil: now.Add(5 * time.Minute),
		FailureCount:  5,
	}

	// After cooldown expires, should auto-recover and reset failure count
	assert.True(t, state.IsUsable(now.Add(6*time.Minute)))
	assert.False(t, state.Disabled)
	assert.Equal(t, int64(0), state.FailureCount)
}

func TestConsecutiveFailureStreak(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		window   []RouteNodeRecord
		expected int
	}{
		{
			name:     "empty window",
			window:   []RouteNodeRecord{},
			expected: 0,
		},
		{
			name: "no failures",
			window: []RouteNodeRecord{
				{Success: true, Timestamp: now.Add(-1 * time.Minute)},
				{Success: true, Timestamp: now},
			},
			expected: 0,
		},
		{
			name: "consecutive 3 failures",
			window: []RouteNodeRecord{
				{Success: false, Timestamp: now.Add(-3 * time.Minute)},
				{Success: false, Timestamp: now.Add(-2 * time.Minute)},
				{Success: false, Timestamp: now.Add(-1 * time.Minute)},
			},
			expected: 3,
		},
		{
			name: "mixed with success in middle",
			window: []RouteNodeRecord{
				{Success: false, Timestamp: now.Add(-4 * time.Minute)},
				{Success: false, Timestamp: now.Add(-3 * time.Minute)},
				{Success: true, Timestamp: now.Add(-2 * time.Minute)},
				{Success: false, Timestamp: now.Add(-1 * time.Minute)},
			},
			expected: 1,
		},
		{
			name: "old records pruned",
			window: []RouteNodeRecord{
				{Success: false, Timestamp: now.Add(-10 * time.Minute)}, // pruned
				{Success: false, Timestamp: now.Add(-2 * time.Minute)},
				{Success: false, Timestamp: now.Add(-1 * time.Minute)},
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &RouteNodeState{
				SlideWindow: tt.window,
			}
			streak := state.ConsecutiveFailureStreak()
			assert.Equal(t, tt.expected, streak)
		})
	}
}

func TestPruneOldRecords(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		SlideWindow: []RouteNodeRecord{
			{RequestID: "r1", Timestamp: now.Add(-10 * time.Minute)},
			{RequestID: "r2", Timestamp: now.Add(-6 * time.Minute)},
			{RequestID: "r3", Timestamp: now.Add(-4 * time.Minute)},
			{RequestID: "r4", Timestamp: now.Add(-1 * time.Minute)},
		},
	}

	// Prune records older than 5 minutes
	state.PruneOldRecords(now, 5*time.Minute)

	require.Len(t, state.SlideWindow, 2)
	assert.Equal(t, "r3", state.SlideWindow[0].RequestID)
	assert.Equal(t, "r4", state.SlideWindow[1].RequestID)
}

func TestRecordSuccess(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		CredentialID: 1,
		Model:        "gpt-4",
		SuccessCount: 10,
	}

	state.RecordSuccess("req-123", now)

	assert.Equal(t, int64(11), state.SuccessCount)
	assert.Equal(t, now, state.LastSuccessAt)
	require.Len(t, state.SlideWindow, 1)
	assert.Equal(t, "req-123", state.SlideWindow[0].RequestID)
	assert.True(t, state.SlideWindow[0].Success)
}

func TestRecordFailure(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		CredentialID: 1,
		Model:        "gpt-4",
		FailureCount: 5,
	}

	state.RecordFailure("req-456", "timeout", now)

	assert.Equal(t, int64(6), state.FailureCount)
	assert.Equal(t, now, state.LastFailureAt)
	require.Len(t, state.SlideWindow, 1)
	assert.Equal(t, "req-456", state.SlideWindow[0].RequestID)
	assert.False(t, state.SlideWindow[0].Success)
	assert.Equal(t, "timeout", state.SlideWindow[0].ErrorKind)
}

func TestRecordFailure_AutoDisable(t *testing.T) {
	now := time.Now()
	state := &RouteNodeState{
		CredentialID: 1,
		Model:        "gpt-4",
	}

	// Record 3 consecutive failures
	state.RecordFailure("req-1", "error1", now.Add(-2*time.Minute))
	assert.False(t, state.Disabled, "should not disable after 1 failure")

	state.RecordFailure("req-2", "error2", now.Add(-1*time.Minute))
	assert.False(t, state.Disabled, "should not disable after 2 failures")

	state.RecordFailure("req-3", "error3", now)
	assert.True(t, state.Disabled, "should disable after 3 consecutive failures")
	assert.Equal(t, now.Add(5*time.Minute), state.DisabledUntil)
	assert.Contains(t, state.DisabledReason, "consecutive 3 failures")
}

func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		MaxRetries:   3,
		MinIdleConns: 5,
		PoolSize:     10,
	})

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, cleanup
}

func TestRouteNodeStore_GetSet(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRouteNodeStore(client)
	ctx := context.Background()

	// Get non-existent state (should return empty state)
	state, err := store.Get(ctx, 1, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, 1, state.CredentialID)
	assert.Equal(t, "gpt-4", state.Model)
	assert.Equal(t, int64(0), state.SuccessCount)
	assert.Len(t, state.SlideWindow, 0)

	// Set state
	state.SuccessCount = 42
	state.FailureCount = 3
	state.SlideWindow = []RouteNodeRecord{
		{RequestID: "req-1", Success: true, Timestamp: time.Now()},
	}
	err = store.Set(ctx, state)
	require.NoError(t, err)

	// Get state again
	retrieved, err := store.Get(ctx, 1, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(42), retrieved.SuccessCount)
	assert.Equal(t, int64(3), retrieved.FailureCount)
	assert.Len(t, retrieved.SlideWindow, 1)
	assert.Equal(t, "req-1", retrieved.SlideWindow[0].RequestID)
}

func TestRouteNodeStore_RecordSuccess(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRouteNodeStore(client)
	ctx := context.Background()

	// Record success
	err := store.RecordSuccess(ctx, 1, "gpt-4", "req-success")
	require.NoError(t, err)

	// Verify state
	state, err := store.Get(ctx, 1, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(1), state.SuccessCount)
	assert.Len(t, state.SlideWindow, 1)
	assert.Equal(t, "req-success", state.SlideWindow[0].RequestID)
	assert.True(t, state.SlideWindow[0].Success)
}

func TestRouteNodeStore_RecordFailure(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRouteNodeStore(client)
	ctx := context.Background()

	// Record failure
	err := store.RecordFailure(ctx, 1, "gpt-4", "req-fail", "timeout")
	require.NoError(t, err)

	// Verify state
	state, err := store.Get(ctx, 1, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, int64(1), state.FailureCount)
	assert.Len(t, state.SlideWindow, 1)
	assert.Equal(t, "req-fail", state.SlideWindow[0].RequestID)
	assert.False(t, state.SlideWindow[0].Success)
	assert.Equal(t, "timeout", state.SlideWindow[0].ErrorKind)
}

func TestRouteNodeStore_ConcurrentUpdates_NoLostUpdates(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRouteNodeStore(client)
	ctx := context.Background()
	workers := 4
	recordsPerWorker := 3

	var wg sync.WaitGroup
	errChan := make(chan error, workers*recordsPerWorker)

	for w := range workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for r := range recordsPerWorker {
				reqID := fmt.Sprintf("worker-%d-req-%d", workerID, r)
				if err := store.RecordSuccess(ctx, 99, "gpt-4-optimistic", reqID); err != nil {
					errChan <- fmt.Errorf("worker %d record %d failed: %w", workerID, r, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatal(err)
	}

	state, err := store.Get(ctx, 99, "gpt-4-optimistic")
	require.NoError(t, err)
	assert.Equal(t, int64(workers*recordsPerWorker), state.SuccessCount,
		"expected all concurrent updates to be accounted for")
	assert.Len(t, state.SlideWindow, workers*recordsPerWorker,
		"expected all slide window records to be present")
}

func TestRouteNodeStore_RecordFailure_Consecutive(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	store := NewRouteNodeStore(client)
	ctx := context.Background()

	// Record 3 consecutive failures
	for i := 1; i <= 3; i++ {
		err := store.RecordFailure(ctx, 1, "gpt-4", fmt.Sprintf("req-%d", i), "error")
		require.NoError(t, err)
	}

	// Verify node is disabled
	state, err := store.Get(ctx, 1, "gpt-4")
	require.NoError(t, err)
	assert.True(t, state.Disabled)
	assert.False(t, state.IsUsable(time.Now()))
}
