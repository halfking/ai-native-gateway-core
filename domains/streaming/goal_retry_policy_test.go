package streaming

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultGoalRetryPolicy(t *testing.T) {
	got := defaultGoalRetryPolicy()
	require.Equal(t, "minimal", got.CostMode)
	require.True(t, got.Enabled)
	require.Equal(t, 2, got.MaxRetries)
	require.Equal(t, 40*time.Second, got.TotalTimeout)
	require.Equal(t, 100*time.Millisecond, got.BaseDelay)
	require.Equal(t, 5*time.Second, got.MaxDelay)
}

func TestNormalizeGoalRetryPolicy(t *testing.T) {
	tests := []struct {
		name     string
		input    GoalRetryPolicy
		expected GoalRetryPolicy
	}{
		{
			name: "valid policy unchanged",
			input: GoalRetryPolicy{
				CostMode:     "balanced",
				Enabled:      true,
				MaxRetries:   3,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
			expected: GoalRetryPolicy{
				CostMode:     "balanced",
				Enabled:      true,
				MaxRetries:   3,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
		},
		{
			name: "negative max retries clamped to zero",
			input: GoalRetryPolicy{
				MaxRetries:   -5,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
			expected: GoalRetryPolicy{
				MaxRetries:   0,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
		},
		{
			name: "zero timeout replaced with default",
			input: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 0,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
			expected: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 40 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
		},
		{
			name: "negative timeout replaced with default",
			input: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: -10 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
			expected: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 40 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
		},
		{
			name: "zero base delay replaced with default",
			input: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    0,
				MaxDelay:     5 * time.Second,
			},
			expected: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    100 * time.Millisecond,
				MaxDelay:     5 * time.Second,
			},
		},
		{
			name: "max delay less than base delay adjusted",
			input: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    5 * time.Second,
				MaxDelay:     100 * time.Millisecond,
			},
			expected: GoalRetryPolicy{
				MaxRetries:   2,
				TotalTimeout: 50 * time.Second,
				BaseDelay:    5 * time.Second,
				MaxDelay:     5 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.Normalize()
			require.Equal(t, tt.expected, got)
		})
	}
}
