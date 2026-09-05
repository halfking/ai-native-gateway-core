package nodestatecache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-NS-03a：Succ1h/Fail1h 饱和 uint16 计数封顶不回绕。
func TestStatsSaturatingCounters(t *testing.T) {
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)
	c.Update(id, true, ErrKindNone, 10)
	c.Update(id, true, ErrKindNone, 10)
	slot := c.StatsSlot(id)
	assert.EqualValues(t, 2, slot.Succ1h)
	assert.EqualValues(t, 0, slot.FailStreak)
	assert.Equal(t, StateAvailable, slot.State)

	// 同窗口灌满：直捣槽内部验证饱和（70000 > 65535）。
	for i := 0; i < 70000; i++ {
		c.stats.recordOutcome(id, true, c.clock())
	}
	slot = c.StatsSlot(id)
	assert.EqualValues(t, 65535, slot.Succ1h, "Succ1h must saturate, not wrap")

	// Fail1h 与 FailStreak 同理（FailStreak 封顶 255 不回绕）。
	for i := 0; i < 70000; i++ {
		c.stats.recordOutcome(id, false, c.clock())
	}
	slot = c.StatsSlot(id)
	assert.EqualValues(t, 65535, slot.Fail1h, "Fail1h must saturate, not wrap")
	assert.EqualValues(t, 255, slot.FailStreak, "FailStreak must saturate, not wrap")
}

// UT-NS-03b：1h 窗口按 WindowStart 翻转重置计数；FailStreak/State 跨窗保留。
func TestStatsHourWindowFlip(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)

	for i := 0; i < 5; i++ {
		c.Update(id, true, ErrKindNone, 10)
	}
	slot := c.StatsSlot(id)
	require.EqualValues(t, 5, slot.Succ1h)
	hour0 := slot.WindowStart
	require.EqualValues(t, uint32(testBase.Unix()/3600), hour0)

	// 窗口内两次失败 → FailStreak=2。
	c.Update(id, false, ErrKindUpstream, 100)
	c.Update(id, false, ErrKindUpstream, 100)
	require.EqualValues(t, 2, c.StatsSlot(id).FailStreak)

	// 跨 1h：计数重置、WindowStart 翻转、FailStreak 保留。
	clk.Add(2 * time.Hour)
	c.Update(id, false, ErrKindUpstream, 100)
	slot = c.StatsSlot(id)
	assert.EqualValues(t, 0, slot.Succ1h, "Succ1h must reset on window flip")
	assert.EqualValues(t, 1, slot.Fail1h)
	assert.Greater(t, slot.WindowStart, hour0)
	assert.EqualValues(t, 3, slot.FailStreak, "FailStreak persists across window flip")
	// 第三次失败累计达阈值 3 → Updater 已转 probing。
	assert.Equal(t, StateProbing, slot.State)
	assert.False(t, c.bitmap.TestAvail(id))
	assert.True(t, c.bitmap.TestProbe(id))
}

// sweep1h（ResetWindow 的统计半边）独立验证：未跨窗的槽不动。
func TestStatsSweep(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	c.Update(id, true, ErrKindNone, 10)

	n := c.stats.sweep1h(clk.Now())
	assert.Equal(t, 0, n, "same window: nothing to sweep")

	clk.Add(90 * time.Minute)
	n = c.stats.sweep1h(clk.Now())
	assert.Equal(t, 1, n)
	slot := c.StatsSlot(id)
	assert.EqualValues(t, 0, slot.Succ1h)
	assert.EqualValues(t, 0, slot.Fail1h)
	assert.EqualValues(t, uint32(clk.Now().Unix()/3600), slot.WindowStart)
	assert.NotZero(t, slot.LastUsedUnixSec, "LastUsed must survive sweep")

	// 幂等：再扫一次零重置。
	assert.Equal(t, 0, c.stats.sweep1h(clk.Now()))
}

// recordOutcome 的秒级截断与窗口号语义。
func TestStatsLastUsedTruncatedToSeconds(t *testing.T) {
	clk := newFakeClock(testBase.Add(1500 * time.Millisecond)) // 带非零纳秒
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	c.Update(id, true, ErrKindNone, 10)
	slot := c.StatsSlot(id)
	assert.EqualValues(t, testBase.Unix()+1, slot.LastUsedUnixSec)
}
