package nodestatecache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedNodes 注册 n 个节点并喂入成功使其可用，返回 dense ids。
func seedNodes(t *testing.T, c *Cache, n int) []int32 {
	t.Helper()
	ids := make([]int32, n)
	for i := 0; i < n; i++ {
		id, err := c.Register(NodeRef{TenantID: "t", CredentialID: int64(i + 1), RawModel: "m"})
		require.NoError(t, err)
		c.Update(id, true, ErrKindNone, 10)
		ids[i] = id
	}
	return ids
}

// UT-NS-08：位图预过滤（不可用/冷却/已试/满载）→ 幸存集交评分排序 → 取第一。
func TestSelectorPrefilterAndPickFirst(t *testing.T) {
	clk := newFakeClock(testBase)
	scorer := &fakeScorer{order: map[int32]float64{}}
	c := New(Options{Capacity: 32, StartTickers: false, Clock: clk.Now, Scorer: scorer})
	ids := seedNodes(t, c, 5) // 全部可用

	// id[1]：满载（conc 达限）。
	c.SetLimits(ids[1], 1, 0, 0)
	require.True(t, c.TryAcquire(ids[1], ResourceConcurrency))
	// id[2]：本请求已试。
	used := []NodeUseRecord{{Seq: 1, ModelID: 1, NodeID: ids[2], Packed: PackUseRecord(StateAvailable, ErrKindUpstream, false, false, false)}}
	// id[3]：冷却中（连续失败 3 次达阈值 → Avail 清除）。
	for i := 0; i < 3; i++ {
		c.Update(ids[3], false, ErrKindUpstream, 100)
	}
	require.False(t, c.bitmap.TestAvail(ids[3]))
	// id[4]：degraded（失败 1 次，未达阈值，仍可路由）。
	c.Update(ids[4], false, ErrKindNetwork, 100)

	// 评分：id[4] 最优（degraded 可参与、评分降权由 Scorer 负责）。
	scorer.order[ids[0]] = 0.1
	scorer.order[ids[4]] = 0.9

	res := c.Select(t.Context(), SelectQuery{Model: "m", Used: used})
	assert.False(t, res.Exhausted)
	assert.Equal(t, ids[4], res.NodeID, "highest scored survivor wins")
	assert.NotZero(t, res.ModelID)
	// 后备序列：剩余按评分排序，首位是 ids[0]。
	require.NotEmpty(t, res.Alternatives)
	assert.Equal(t, ids[0], res.Alternatives[0].NodeID)

	// Scorer 收到的幸存集 = {id0, id4}（剔除满载/已试/冷却）。
	calls, surv, mdl := scorer.snapshot()
	require.Equal(t, 1, calls)
	assert.Equal(t, "m", mdl)
	assert.ElementsMatch(t, []int32{ids[0], ids[4]}, surv)
}

// Flags bit2 排除冷却中：degraded 幸存者被剔除。
func TestSelectorExcludeCoolingFlag(t *testing.T) {
	clk := newFakeClock(testBase)
	c := New(Options{Capacity: 16, StartTickers: false, Clock: clk.Now})
	ids := seedNodes(t, c, 2)
	c.Update(ids[1], false, ErrKindNetwork, 100) // degraded，仍 Avail

	res := c.Select(t.Context(), SelectQuery{Model: "m", Flags: FlagExcludeCooling})
	assert.False(t, res.Exhausted)
	assert.Equal(t, ids[0], res.NodeID, "degraded node excluded under FlagExcludeCooling")

	// ids[0] 转冷却（Avail=0）后：缺省与 FlagFreeTolerant 均仍可路由 degraded。
	for i := 0; i < 3; i++ {
		c.Update(ids[0], false, ErrKindUpstream, 100)
	}
	require.False(t, c.bitmap.TestAvail(ids[0]))
	res = c.Select(t.Context(), SelectQuery{Model: "m"})
	assert.Equal(t, ids[1], res.NodeID, "default keeps degraded routable (URSM semantics)")
	res = c.Select(t.Context(), SelectQuery{Model: "m", Flags: FlagFreeTolerant})
	assert.Equal(t, ids[1], res.NodeID, "FlagFreeTolerant keeps degraded routable")

	// FlagExcludeCooling 下唯一幸存是 degraded → 穷尽。
	res = c.Select(t.Context(), SelectQuery{Model: "m", Flags: FlagExcludeCooling})
	assert.True(t, res.Exhausted)
	// 缺省（无标志）：degraded 也可路由（URSM 语义，评分降权）。
	scorer := &fakeScorer{order: map[int32]float64{ids[1]: 1}}
	c2 := New(Options{Capacity: 16, StartTickers: false, Clock: clk.Now, Scorer: scorer})
	ids2 := seedNodes(t, c2, 2)
	c2.Update(ids2[1], false, ErrKindNetwork, 100)
	assert.Equal(t, ids2[1], c2.Select(t.Context(), SelectQuery{Model: "m"}).NodeID)
}

// UT-NS-08b：Flags bit0 sticky 优先生效——绑定节点健康未满载未试过则直接复用。
func TestSelectorStickyPriority(t *testing.T) {
	clk := newFakeClock(testBase)
	scorer := &fakeScorer{order: map[int32]float64{}}
	ids := make([]int32, 0)
	stickyRef := NodeRef{}
	var stickyID int32

	st := &fakeSticky{}
	c := New(Options{Capacity: 16, StartTickers: false, Clock: clk.Now, Scorer: scorer, StickyLookup: st})
	for i := 0; i < 3; i++ {
		id, err := c.Register(NodeRef{TenantID: "t", CredentialID: int64(i + 1), RawModel: "m"})
		require.NoError(t, err)
		c.Update(id, true, ErrKindNone, 10)
		ids = append(ids, id)
	}
	stickyID = ids[2]
	stickyRef, _ = c.NodeRef(stickyID)
	st.ref, st.valid = stickyRef, true
	scorer.order[ids[0]] = 9 // 评分最优并非 sticky 节点

	res := c.Select(t.Context(), SelectQuery{Model: "m", Flags: FlagSticky})
	assert.Equal(t, stickyID, res.NodeID, "sticky binding wins over score")
	assert.False(t, res.SwitchedModel)

	// sticky 节点已试过 → 回退评分主路径。
	res = c.Select(t.Context(), SelectQuery{
		Model: "m",
		Flags: FlagSticky,
		Used:  []NodeUseRecord{{Seq: 1, NodeID: stickyID}},
	})
	assert.Equal(t, ids[0], res.NodeID)

	// sticky 节点满载 → 回退。
	c.SetLimits(stickyID, 1, 0, 0)
	require.True(t, c.TryAcquire(stickyID, ResourceConcurrency))
	res = c.Select(t.Context(), SelectQuery{Model: "m", Flags: FlagSticky})
	assert.Equal(t, ids[0], res.NodeID)
}

// UT-NS-09：AllowModelSwitch 且当前模型幸存为空 → 品质档位换模型再选；
// 全空 Exhausted=true。
func TestSelectorModelSwitchAndExhausted(t *testing.T) {
	clk := newFakeClock(testBase)
	scorer := &fakeScorer{
		order:    map[int32]float64{},
		emptyFor: map[string]bool{"m1": true},
	}
	fb := &fakeFallback{models: map[string][]string{"m1": {"m2", "m3"}}}
	c := New(Options{
		Capacity:      16,
		StartTickers:  false,
		Clock:         clk.Now,
		Scorer:        scorer,
		ModelFallback: fb,
	})
	ids := seedNodes(t, c, 2)
	scorer.order[ids[1]] = 5

	// 不允许切换：m1 无幸存 → Exhausted。
	res := c.Select(t.Context(), SelectQuery{Model: "m1"})
	assert.True(t, res.Exhausted)
	assert.EqualValues(t, 0, res.NodeID)

	// 允许切换：m1 空 → m2 再选成功，SwitchedModel=true。
	res = c.Select(t.Context(), SelectQuery{Model: "m1", AllowModelSwitch: true})
	assert.False(t, res.Exhausted)
	assert.True(t, res.SwitchedModel)
	assert.Equal(t, ids[1], res.NodeID)
	assert.Equal(t, c.RegisterModel("m2"), res.ModelID)
	// 后备序列带「切换而来」位。
	for _, alt := range res.Alternatives {
		_, _, _, switched, _ := UnpackUseRecord(alt.Packed)
		assert.True(t, switched)
	}

	// 全部模型空 → Exhausted（组合穷尽信号）。
	scorer.emptyFor["m2"] = true
	scorer.emptyFor["m3"] = true
	res = c.Select(t.Context(), SelectQuery{Model: "m1", AllowModelSwitch: true})
	assert.True(t, res.Exhausted)
	assert.EqualValues(t, 0, res.NodeID)
}

// 全宇宙不可用（Avail 全 0）→ Exhausted。
func TestSelectorAllUnavailableExhausted(t *testing.T) {
	c := newTestCache(8, nil)
	mustRegister(c, 1) // 注册但从未喂入 → Avail=0
	res := c.Select(t.Context(), SelectQuery{Model: "m"})
	assert.True(t, res.Exhausted)
}

// 未注入 Scorer：退化为 dense id 序（仍可用，取最小 id）。
func TestSelectorDefaultOrderWithoutScorer(t *testing.T) {
	c := newTestCache(8, nil)
	ids := seedNodes(t, c, 3)
	res := c.Select(t.Context(), SelectQuery{Model: "m"})
	assert.Equal(t, ids[0], res.NodeID)
}
