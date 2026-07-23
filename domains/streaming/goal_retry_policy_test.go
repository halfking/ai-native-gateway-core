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

// TestEffectiveMaxRetriesHonorsDisabledFlag 回归测试（2026-07-24 审计修复）：
// 租户设置 goal.retry_on_error=false 后，重试循环必须最多执行 1 次。
// 修复前 Enabled 仅打日志、循环不受影响，会跑满 MaxRetries+1 次 + 指数退避。
func TestEffectiveMaxRetriesHonorsDisabledFlag(t *testing.T) {
	tests := []struct {
		name string
		in   GoalRetryPolicy
		want int
	}{
		{"disabled_resolves_zero", GoalRetryPolicy{Enabled: false, MaxRetries: 5}, 0},
		{"disabled_does_not_leak_big_value", GoalRetryPolicy{Enabled: false, MaxRetries: 99}, 0},
		{"enabled_keeps_value", GoalRetryPolicy{Enabled: true, MaxRetries: 3}, 3},
		{"enabled_zero_remains_zero", GoalRetryPolicy{Enabled: true, MaxRetries: 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.in.EffectiveMaxRetries())
		})
	}
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
