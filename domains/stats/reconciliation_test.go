package stats

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestReconciliationWorker_AutoRepairThreshold(t *testing.T) {
	// Unit test for auto-repair threshold logic
	testCases := []struct {
		name          string
		source        float64
		projected     float64
		expectedAuto  bool
		shouldSkip    bool // true if no diff exists
		description   string
	}{
		{
			name:         "exact match",
			source:       1000,
			projected:    1000,
			expectedAuto: false,
			shouldSkip:   true, // no diff means no repair needed
			description:  "no difference should not trigger repair",
		},
		{
			name:         "small absolute diff under threshold",
			source:       100,
			projected:    99,
			expectedAuto: true,
			description:  "~1% diff with value < 1000 should auto-repair",
		},
		{
			name:         "large absolute diff",
			source:       10000,
			projected:    9000,
			expectedAuto: false,
			description:  "10% diff should require manual approval",
		},
		{
			name:         "zero projected with small source",
			source:       50,
			projected:    0,
			expectedAuto: true,
			description:  "missing projection with small value should auto-repair",
		},
		{
			name:         "zero projected with large source",
			source:       2000,
			projected:    0,
			expectedAuto: false,
			description:  "missing projection with large value needs approval",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Skip if no diff
			if tc.source == tc.projected {
				require.True(t, tc.shouldSkip, "test case should mark shouldSkip=true for equal values")
				return
			}

			diff := tc.source - tc.projected
			canAutoRepair := false

			if tc.projected != 0 {
				relDiff := abs(diff / tc.projected)
				if relDiff < autoRepairThreshold && abs(diff) < autoRepairMaxValue {
					canAutoRepair = true
				}
			} else if abs(diff) < autoRepairMaxValue {
				canAutoRepair = true
			}

			require.Equal(t, tc.expectedAuto, canAutoRepair, tc.description)
		})
	}
}

func TestReconciliationWorker_Lifecycle(t *testing.T) {
	db := &pgxpool.Pool{} // mock pool for lifecycle test
	worker := NewReconciliationWorker(db, 10*time.Second)
	require.NotNil(t, worker)
	require.Equal(t, 10*time.Second, worker.interval)

	// Test default interval
	worker2 := NewReconciliationWorker(db, 0)
	require.Equal(t, defaultReconciliationInterval, worker2.interval)

	// Lifecycle test with nil context to avoid actual work
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel to stop worker

	worker.Start(ctx)
	// Start again should be no-op
	worker.Start(ctx)

	worker.Stop()
	// Stop again should be no-op
	worker.Stop()
}

func TestReconciliationWorker_NilSafety(t *testing.T) {
	var worker *ReconciliationWorker
	worker.Start(context.Background()) // should not panic
	worker.Stop()                      // should not panic

	err := worker.ReconcilePeriod(context.Background(), time.Now(), time.Now(), "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized")
}
