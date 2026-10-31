package provider

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// candidates_circuit_test.go — 候选查询熔断器状态机（2026-10-28
// routing_schema_error 轮）。行为契约：连续失败 open；open 期 park 延迟；
// 冷却到期单探测；成功闭合；失败冷却翻倍封顶。

func TestCandidatesCircuit_ClosedUntilThreshold(t *testing.T) {
	var c candidatesCircuit
	now := time.Now()

	// 前两次失败：仍 closed（AllowProbe 直通）。
	c.RecordFailure(now)
	c.RecordFailure(now)
	allow, retryAt := c.AllowProbe(now.Add(time.Second))
	assert.True(t, allow, "below threshold must stay closed")
	assert.True(t, retryAt.IsZero())

	// 第三次失败 → open。
	c.RecordFailure(now)
	allow, retryAt = c.AllowProbe(now.Add(time.Second))
	assert.False(t, allow, "third consecutive failure must open the circuit")
	assert.False(t, retryAt.IsZero())
	assert.WithinDuration(t, now.Add(time.Second).Add(circuitCooldownInitial), retryAt, time.Second,
		"first cooldown = initial 30s")
}

func TestCandidatesCircuit_SuccessResets(t *testing.T) {
	var c candidatesCircuit
	now := time.Now()

	c.RecordFailure(now)
	c.RecordFailure(now)
	c.RecordSuccess()

	allow, _ := c.AllowProbe(now)
	assert.True(t, allow, "success must close the circuit and clear the count")

	// 清零后两次失败不得 open（阈值是连续失败）。
	c.RecordFailure(now.Add(time.Second))
	c.RecordFailure(now.Add(time.Second))
	allow, _ = c.AllowProbe(now.Add(time.Second))
	assert.True(t, allow)
}

func TestCandidatesCircuit_HalfOpenSingleProbe(t *testing.T) {
	var c candidatesCircuit
	now := time.Now()

	for i := 0; i < circuitFailThreshold; i++ {
		c.RecordFailure(now)
	}
	expiry := now.Add(circuitCooldownInitial)

	// 冷却未到期：全部拒绝。
	allow, retryAt := c.AllowProbe(expiry.Add(-time.Second))
	assert.False(t, allow)
	assert.Equal(t, expiry, retryAt)

	// 冷却到期：放一个探测，第二个请求被拒（probe 占用）。
	allow, _ = c.AllowProbe(expiry.Add(time.Second))
	assert.True(t, allow, "cooldown expiry must admit exactly one probe")
	allow, retryAt = c.AllowProbe(expiry.Add(time.Second))
	assert.False(t, allow, "half-open admits a single probe")
	assert.Equal(t, expiry, retryAt)

	// 探测失败 → 冷却翻倍重新 open。
	c.RecordFailure(expiry.Add(time.Second))
	allow, retryAt = c.AllowProbe(expiry.Add(2 * time.Second))
	assert.False(t, allow)
	assert.WithinDuration(t, expiry.Add(2*time.Second).Add(2*circuitCooldownInitial), retryAt, time.Second,
		"failed probe doubles the cooldown")
}

func TestCandidatesCircuit_ProbeSuccessCloses(t *testing.T) {
	var c candidatesCircuit
	now := time.Now()

	for i := 0; i < circuitFailThreshold; i++ {
		c.RecordFailure(now)
	}
	c.AllowProbe(now.Add(circuitCooldownInitial + time.Second)) // 放出探测
	c.RecordSuccess()

	allow, _ := c.AllowProbe(now.Add(circuitCooldownInitial + 2*time.Second))
	assert.True(t, allow, "successful probe must close the circuit")

	// 闭合后冷却归位：下一次 open 从初值起步。
	base := now.Add(circuitCooldownInitial + 3*time.Second)
	for i := 0; i < circuitFailThreshold; i++ {
		c.RecordFailure(base)
	}
	_, retryAt := c.AllowProbe(base.Add(time.Second))
	assert.WithinDuration(t, base.Add(circuitCooldownInitial), retryAt, time.Second,
		"cooldown must reset after a successful probe closed the circuit")
}

func TestCandidatesCircuit_CooldownCap(t *testing.T) {
	var c candidatesCircuit
	t0 := time.Now()

	// 触发首次 open（连续 3 次失败）。
	for i := 0; i < circuitFailThreshold; i++ {
		c.RecordFailure(t0)
	}

	// 每轮：快进到冷却到期 → 放探测 → 探测失败 ⇒ 冷却翻倍。
	// 期望序列 30/60/120/240/480/600(封顶)。
	cursor := t0
	for round := 1; round <= 6; round++ {
		expiry := cursor.Add(circuitCooldownInitial * (1 << (round - 1)))
		if expiry.Sub(cursor) > circuitCooldownCap {
			expiry = cursor.Add(circuitCooldownCap)
		}
		allow, _ := c.AllowProbe(expiry.Add(time.Second))
		require.True(t, allow, "round %d: expiry must admit the probe", round)
		c.RecordFailure(expiry.Add(time.Second)) // 探测失败
		allow, retryAt := c.AllowProbe(expiry.Add(time.Second))
		require.False(t, allow, "round %d: must re-open after failed probe", round)
		want := circuitCooldownInitial << round
		if want > circuitCooldownCap {
			want = circuitCooldownCap
		}
		assert.WithinDuration(t, expiry.Add(time.Second).Add(want), retryAt, time.Second,
			"round %d cooldown", round)
		cursor = expiry.Add(time.Second)
	}
}

func TestErrCandidatesCircuitOpenIsSentinel(t *testing.T) {
	wrapped := errWrap()
	assert.True(t, errors.Is(wrapped, ErrCandidatesCircuitOpen))
}

func errWrap() error {
	return errWrapInner()
}

func errWrapInner() error {
	return errors.Join(ErrCandidatesCircuitOpen)
}
