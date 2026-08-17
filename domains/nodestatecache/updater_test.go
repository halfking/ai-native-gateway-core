package nodestatecache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Updater 主路径：成功统计 + Avail；失败递增 FailStreak；达阈值（默认 3，
// 对齐 URSM FailStreakLimit）→ 清 Avail 置待探测。
func TestUpdaterFailStreakThreshold(t *testing.T) {
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)

	c.Update(id, true, ErrKindNone, 42)
	assert.True(t, c.bitmap.TestAvail(id))
	assert.Equal(t, StateAvailable, c.StatsSlot(id).State)

	c.Update(id, false, ErrKindUpstream, 100)
	c.Update(id, false, ErrKindUpstream, 100)
	require.True(t, c.bitmap.TestAvail(id), "below threshold: still routable")
	require.EqualValues(t, 2, c.StatsSlot(id).FailStreak)

	c.Update(id, false, ErrKindUpstream, 100) // 第 3 次 → 阈值
	assert.False(t, c.bitmap.TestAvail(id), "threshold reached: cleared Avail")
	assert.True(t, c.bitmap.TestProbe(id), "threshold reached: pending probe")
	assert.Equal(t, StateProbing, c.StatsSlot(id).State)
	assert.EqualValues(t, 3, c.StatsSlot(id).FailStreak)

	// 成功且冷却中 → 恢复 Avail、清 Probe、清 streak。
	c.Update(id, true, ErrKindNone, 30)
	assert.True(t, c.bitmap.TestAvail(id))
	assert.False(t, c.bitmap.TestProbe(id))
	assert.Equal(t, StateAvailable, c.StatsSlot(id).State)
	assert.EqualValues(t, 0, c.StatsSlot(id).FailStreak)
}

// 阈值可配（FailStreakLimit=5 时第 5 次失败才转 probing）。
func TestUpdaterConfigurableThreshold(t *testing.T) {
	clk := newFakeClock(testBase)
	c := New(Options{Capacity: 8, StartTickers: false, Clock: clk.Now, FailStreakLimit: 5})
	id := mustRegister(c, 1)
	c.Update(id, true, ErrKindNone, 10)
	for i := 0; i < 4; i++ {
		c.Update(id, false, ErrKindUpstream, 100)
	}
	require.True(t, c.bitmap.TestAvail(id), "below custom threshold")
	c.Update(id, false, ErrKindUpstream, 100)
	assert.False(t, c.bitmap.TestAvail(id), "custom threshold reached")
	assert.True(t, c.bitmap.TestProbe(id))
}

// 429 → Full 位图排除（RPM 镜像满载），分钟窗翻转后回归（见 resources_test）。
func TestUpdaterRateLimitMarksFull(t *testing.T) {
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)
	c.Update(id, false, ErrKindRateLimit, 100)
	assert.True(t, c.bitmap.TestFull(id))
	slot := c.ResourceSlot(id)
	assert.True(t, slot.RPMLimit > 0 && slot.RPMUsed >= slot.RPMLimit)
}

// Updater 闭包（R10.5 签名）可直接从构造器取用。
func TestUpdaterClosureSignature(t *testing.T) {
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)
	var u UpdaterFunc = c.Updater()
	u(id, true, ErrKindNone, 100)
	var s NodeSelector = c.Selector()
	assert.NotNil(t, s)
	assert.False(t, s(nil, SelectQuery{Model: "m"}).Exhausted)
}
