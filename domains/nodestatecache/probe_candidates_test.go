package nodestatecache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// markProbingAt 把节点在指定时刻打满失败阈值 → 进 Probe 位图，LastUsed=该时刻。
func markProbingAt(t *testing.T, c *Cache, id int32, at time.Time) {
	t.Helper()
	clk := newFakeClock(at)
	for i := 0; i < int(c.opts.FailStreakLimit); i++ {
		c.stats.recordOutcome(id, false, clk.Now())
	}
	c.bitmap.MarkProbing(id)
	c.stats.setState(id, StateProbing)
	require.True(t, c.bitmap.TestProbe(id))
}

// UT-NS-10：NeedProbe = Probe 位图 ∩ 36h 命中 ∩ 退避到期，三条件缺一不产出。
func TestNeedProbeThreeConditions(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(16, clk)
	ids := make([]int32, 4)
	refs := make([]NodeRef, 4)
	for i := range ids {
		id, err := c.Register(NodeRef{TenantID: "t", CredentialID: int64(i + 1), RawModel: "m"})
		require.NoError(t, err)
		ids[i] = id
		refs[i], _ = c.NodeRef(id)
	}

	// A：三条件全满足 → 产出。
	//   失败发生于 1000s 前，streak=3 → 退避 30s·2^2=120s，早已到期。
	markProbingAt(t, c, ids[0], testBase.Add(-1000*time.Second))
	// B：缺 36h 命中（RecentSuccessLookup=false）。
	markProbingAt(t, c, ids[1], testBase.Add(-1000*time.Second))
	// C：缺退避到期（失败刚发生，退避 120s 未过）。
	markProbingAt(t, c, ids[2], testBase)
	// D：缺 Probe 位图（健康可用）。
	c.Update(ids[3], true, ErrKindNone, 10)

	recent := &fakeRecentSuccess{hit: map[NodeRef]bool{refs[0]: true, refs[2]: true, refs[3]: true}}
	c.recentSuccess = recent

	got := c.NeedProbe(testBase)
	assert.Equal(t, []NodeRef{refs[0]}, got, "only node A satisfies all three conditions")

	// 退避到期后 C 也产出。
	got = c.NeedProbe(testBase.Add(130 * time.Second))
	assert.ElementsMatch(t, []NodeRef{refs[0], refs[2]}, got)
}

// 36h 窗口参数由 Options.RecentSuccessWindow 传给 lookup。
func TestNeedProbeWindowPassedToLookup(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	markProbingAt(t, c, id, testBase.Add(-time.Hour))

	var gotWindow time.Duration
	c.recentSuccess = recentSuccessFunc(func(r NodeRef, w time.Duration, _ time.Time) bool {
		gotWindow = w
		return true
	})
	_ = c.NeedProbe(testBase)
	assert.Equal(t, 36*time.Hour, gotWindow)
}

// RecentSuccessLookup 未注入（nil）→ 36h 条件视为满足（由 R4.4 扫描器侧兜底）。
func TestNeedProbeNilLookupSatisfied(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	ref, _ := c.NodeRef(id)
	markProbingAt(t, c, id, testBase.Add(-time.Hour))
	assert.Equal(t, []NodeRef{ref}, c.NeedProbe(testBase))
}

// 退避曲线：streak=1 → 30s、streak=3 → 120s、封顶 BackoffCap。
func TestNeedProbeBackoffCurve(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)

	// streak=1（30s 退避）。
	c.stats.recordOutcome(id, false, testBase.Add(-40*time.Second))
	assert.True(t, c.backoffExpired(c.StatsSlot(id), testBase), "40s > 30s backoff")
	assert.False(t, c.backoffExpired(c.StatsSlot(id), testBase.Add(-20*time.Second)))

	// streak=3 → 120s。
	c.stats.recordOutcome(id, false, testBase.Add(-30*time.Second))
	c.stats.recordOutcome(id, false, testBase.Add(-20*time.Second))
	slot := c.StatsSlot(id)
	require.EqualValues(t, 3, slot.FailStreak)
	assert.False(t, c.backoffExpired(slot, testBase.Add(-100*time.Second).Add(60*time.Second)))
	assert.True(t, c.backoffExpired(slot, testBase.Add(100*time.Second)))

	// 封顶：大 streak 不再翻倍（BackoffCap=30m 默认）。
	c.stats.recordOutcome(id, false, testBase)
	c.stats.recordOutcome(id, false, testBase)
	slot = c.StatsSlot(id) // streak=5 → 30s·2^4=480s < cap
	require.EqualValues(t, 5, slot.FailStreak)
	assert.True(t, c.backoffExpired(slot, testBase.Add(10*time.Minute)))
}
