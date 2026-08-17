package streaming

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// survival_jitter_test.go — T3 survival 部分：±20% 退避抖动 + Deadline 2h。

func TestApplyBackoffJitterBoundaries(t *testing.T) {
	d := 10 * time.Second

	// Deterministic seams pin the ±20% envelope (legacy handler.go parity).
	assert.Equal(t, 8*time.Second, applyBackoffJitter(d, func() float64 { return 0 }), "r=0 → −20%")
	assert.Equal(t, 10*time.Second, applyBackoffJitter(d, func() float64 { return 0.5 }), "r=0.5 → unchanged")
	assert.Equal(t, 12*time.Second, applyBackoffJitter(d, func() float64 { return 1 }), "r=1 → +20%")

	// Randomized: always within [0.8d, 1.2d], never negative.
	for i := 0; i < 200; i++ {
		j := applyBackoffJitter(d, nil)
		require.GreaterOrEqual(t, j, 8*time.Second)
		require.LessOrEqual(t, j, 12*time.Second)
	}

	// Degenerate inputs: non-positive durations pass through unchanged.
	assert.Equal(t, time.Duration(0), applyBackoffJitter(0, nil))
	assert.Equal(t, -time.Second, applyBackoffJitter(-time.Second, nil))
}

func TestSurvivalOptionsDeadlineDefaultTwoHours(t *testing.T) {
	// T3: 任务总时限 24h → 2h（终止优先级：组合穷尽 > 2h 时限 > 100 次预算）。
	opts := SurvivalOptions{}.withDefaults()
	assert.Equal(t, 2*time.Hour, opts.Deadline)
	// Explicit values still win (env/config override path).
	opts = SurvivalOptions{Deadline: 42 * time.Minute}.withDefaults()
	assert.Equal(t, 42*time.Minute, opts.Deadline)
}
