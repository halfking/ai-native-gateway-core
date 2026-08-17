package nodestatecache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-NS-04a：TryAcquire/Release 基本语义 + Limit=0 表示不限。
func TestResourceTryAcquireBasic(t *testing.T) {
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)

	// 未配置上限：不限（Used 饱和计数防回绕）。
	for i := 0; i < 70000; i++ {
		require.True(t, c.TryAcquire(id, ResourceConcurrency))
	}
	slot := c.ResourceSlot(id)
	assert.EqualValues(t, 65535, slot.ConcUsed, "unlimited counter saturates, never wraps")
	assert.False(t, c.bitmap.TestFull(id), "Limit=0 never full")

	// 配置上限：精确准入。
	c2 := newTestCache(8, nil)
	id2 := mustRegister(c2, 2)
	c2.SetLimits(id2, 2, 1, 3)
	assert.True(t, c2.TryAcquire(id2, ResourceConcurrency))
	assert.True(t, c2.TryAcquire(id2, ResourceConcurrency))
	assert.False(t, c2.TryAcquire(id2, ResourceConcurrency), "conc limit 2")

	assert.True(t, c2.TryAcquire(id2, ResourceFPSlot))
	assert.False(t, c2.TryAcquire(id2, ResourceFPSlot), "fp limit 1")

	for i := 0; i < 3; i++ {
		assert.True(t, c2.TryAcquire(id2, ResourceRPM))
	}
	assert.False(t, c2.TryAcquire(id2, ResourceRPM), "rpm limit 3")

	// Release 下限 0、不泄漏。
	c2.Release(id2, ResourceConcurrency)
	c2.Release(id2, ResourceConcurrency)
	c2.Release(id2, ResourceConcurrency) // 超额释放：下限 0
	assert.EqualValues(t, 0, c2.ResourceSlot(id2).ConcUsed)
}

// UT-NS-04b：并发 TryAcquire/Release 无超卖、无泄漏（race detector 下验证）。
func TestResourceConcurrentNoOversellNoLeak(t *testing.T) {
	const workers = 32
	const rounds = 500
	const concLimit = 10
	c := newTestCache(8, nil)
	id := mustRegister(c, 1)
	c.SetLimits(id, concLimit, 0, 0)

	var success atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			held := 0
			for r := 0; r < rounds; r++ {
				if c.TryAcquire(id, ResourceConcurrency) {
					success.Add(1)
					held++
				}
				if held > 0 && r%3 == 0 {
					c.Release(id, ResourceConcurrency)
					held--
				}
			}
			for i := 0; i < held; i++ {
				c.Release(id, ResourceConcurrency)
			}
		}()
	}
	wg.Wait()

	// 无超卖：任一时刻 Used <= Limit 由准入保证（达限即拒）；
	// 无泄漏：每 worker 结束前归还全部持有 → 终态归零。
	final := c.ResourceSlot(id)
	assert.LessOrEqual(t, int(final.ConcUsed), concLimit)
	assert.EqualValues(t, 0, final.ConcUsed, "no leak: all held leases returned")
	assert.False(t, c.bitmap.TestFull(id), "all released: not full")
	assert.Greater(t, success.Load(), int64(0), "sanity: some acquires must succeed")
}

// UT-NS-05：任一资源达限 → 置 Full 位图；全释放（回到限内）→ 清除；
// 选择闭包预过滤剔除满载节点。
func TestFullBitmapLinkage(t *testing.T) {
	clk := newFakeClock(testBase)
	c2 := newTestCache(16, clk)
	ids := make([]int32, 3)
	for i := range ids {
		id, err := c2.Register(NodeRef{TenantID: "t", CredentialID: int64(i + 1), RawModel: "m"})
		require.NoError(t, err)
		ids[i] = id
		c2.Update(id, true, ErrKindNone, 10) // 全部可用
	}

	// 并发达限 → Full 置位。
	c2.SetLimits(ids[0], 1, 0, 0)
	require.True(t, c2.TryAcquire(ids[0], ResourceConcurrency))
	assert.True(t, c2.bitmap.TestFull(ids[0]), "conc at limit -> Full")

	// FP 达限 → Full 置位。
	c2.SetLimits(ids[1], 0, 1, 0)
	require.True(t, c2.TryAcquire(ids[1], ResourceFPSlot))
	assert.True(t, c2.bitmap.TestFull(ids[1]))

	// RPM 达限 → Full 置位。
	c2.SetLimits(ids[2], 0, 0, 1)
	require.True(t, c2.TryAcquire(ids[2], ResourceRPM))
	assert.True(t, c2.bitmap.TestFull(ids[2]))

	// 选择闭包剔除全部满载 → Exhausted。
	res := c2.Select(t.Context(), SelectQuery{Model: "m"})
	assert.True(t, res.Exhausted)

	// 释放并发占用 → 回到限内 → Full 清除、可再选。
	c2.Release(ids[0], ResourceConcurrency)
	c2.Release(ids[1], ResourceFPSlot)
	assert.False(t, c2.bitmap.TestFull(ids[0]))
	assert.False(t, c2.bitmap.TestFull(ids[1]))
	res = c2.Select(t.Context(), SelectQuery{Model: "m"})
	assert.False(t, res.Exhausted)
	assert.Equal(t, ids[0], res.NodeID)
	// ids[2] 仍满载（RPM 名额本分钟消耗制）。
	assert.True(t, c2.bitmap.TestFull(ids[2]))
}

// UT-NS-06：ResetWindow —— RPM 分钟窗翻转 + 1h 统计窗同时验证（fake clock 推进跨窗）。
func TestResetWindowMinuteAndHour(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	c.SetLimits(id, 0, 0, 2)
	c.Update(id, true, ErrKindNone, 10) // 统计窗计数 +1
	require.True(t, c.TryAcquire(id, ResourceRPM))
	require.True(t, c.TryAcquire(id, ResourceRPM))
	require.False(t, c.TryAcquire(id, ResourceRPM))
	require.True(t, c.bitmap.TestFull(id))
	require.EqualValues(t, 1, c.StatsSlot(id).Succ1h)

	// 未跨窗：ResetWindow 空转。
	rpmN, statsN := c.ResetWindow(clk.Now())
	assert.Equal(t, 0, rpmN)
	assert.Equal(t, 0, statsN)
	assert.True(t, c.bitmap.TestFull(id))

	// 跨 1 分钟 + 1 小时。
	clk.Add(61 * time.Minute)
	rpmN, statsN = c.ResetWindow(clk.Now())
	assert.Equal(t, 1, rpmN)
	assert.Equal(t, 1, statsN)

	slot := c.StatsSlot(id)
	assert.EqualValues(t, 0, slot.Succ1h, "1h stats window flipped")
	assert.EqualValues(t, 0, slot.Fail1h)
	assert.EqualValues(t, uint32(clk.Now().Unix()/3600), slot.WindowStart)

	res := c.ResourceSlot(id)
	assert.EqualValues(t, 0, res.RPMUsed, "RPM minute window flipped")
	assert.EqualValues(t, uint16(clk.Now().Unix()/60), res.WindowID)
	assert.False(t, c.bitmap.TestFull(id), "full cleared after window flip")
	assert.True(t, c.TryAcquire(id, ResourceRPM), "RPM available again after flip")
}

// 内置 ticker：分钟重置由 ticker 驱动（真实短周期 + 每次调用自增的注入时钟）。
func TestResetTickerDrivesWindowFlip(t *testing.T) {
	var mu sync.Mutex
	base := testBase
	calls := 0
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		// 每次 Read 时钟前进 2 分钟，保证 ticker 触发时窗口已跨。
		base = base.Add(2 * time.Minute)
		return base
	}
	c := New(Options{Capacity: 8, StartTickers: true, ResetPeriod: 10 * time.Millisecond, Clock: clock})
	defer c.Close()
	id, err := c.Register(NodeRef{TenantID: "t", CredentialID: 1, RawModel: "m"})
	require.NoError(t, err)
	c.SetLimits(id, 0, 0, 1)
	require.True(t, c.TryAcquire(id, ResourceRPM))
	require.True(t, c.bitmap.TestFull(id))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !c.bitmap.TestFull(id) && c.ResourceSlot(id).RPMUsed == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("ticker did not reset RPM minute window in time")
}

// 429 联动（N16）：RPM 镜像视为本分钟满载 → Full 排除；分钟窗翻转后回归。
func TestForceRateLimitedClearsOnFlip(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(8, clk)
	id := mustRegister(c, 1)
	c.Update(id, true, ErrKindNone, 10)
	c.Update(id, false, ErrKindRateLimit, 100)
	assert.True(t, c.bitmap.TestFull(id), "429 -> Full via RPM mirror")

	clk.Add(90 * time.Second) // 跨分钟
	c.ResetWindow(clk.Now())
	assert.False(t, c.bitmap.TestFull(id), "minute flip clears 429 full")
}
