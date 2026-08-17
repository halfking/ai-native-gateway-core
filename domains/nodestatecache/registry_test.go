package nodestatecache

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testBase = time.Unix(1_700_000_000, 0)

// UT-NS-01a：NodeRef ↔ int32 dense id 双射（同 ref 稳定返回同 id，不同 ref 不同 id）。
func TestRegistryBijection(t *testing.T) {
	c := newTestCache(64, nil)
	refs := make([]NodeRef, 10)
	ids := make([]int32, 10)
	for i := range refs {
		refs[i] = NodeRef{TenantID: "t1", CredentialID: int64(i + 1), RawModel: "m"}
		id, err := c.Register(refs[i])
		require.NoError(t, err)
		require.Greater(t, id, int32(0))
		ids[i] = id
	}
	seen := map[int32]bool{}
	for _, id := range ids {
		require.False(t, seen[id], "duplicate dense id %d", id)
		seen[id] = true
	}
	// 双射：ref → id 稳定；id → ref 还原。
	for i, ref := range refs {
		assert.Equal(t, ids[i], c.NodeID(ref))
		got, ok := c.NodeRef(ids[i])
		require.True(t, ok)
		assert.Equal(t, ref, got)
	}
	// 未注册返回 0。
	assert.EqualValues(t, 0, c.NodeID(NodeRef{TenantID: "other"}))
}

// UT-NS-01b：容量有界——满时 LRU 回收未活跃 id 并复用；回收清理位图/槽位
// 并回调 InvalidationListener(Evicted)。
func TestRegistryBoundedLRUEviction(t *testing.T) {
	inv := &fakeInvalidation{}
	clk := newFakeClock(testBase)
	c := New(Options{
		Capacity:     4,
		StartTickers: false,
		Clock:        clk.Now,
		Invalidation: inv,
	})
	ref1 := NodeRef{TenantID: "t", CredentialID: 1, RawModel: "m"}
	id1, err := c.Register(ref1)
	require.NoError(t, err)
	for i := 2; i <= 4; i++ {
		_, err := c.Register(NodeRef{TenantID: "t", CredentialID: int64(i), RawModel: "m"})
		require.NoError(t, err)
	}
	// id1 喂入状态后应被 LRU 回收（最早注册且未再触碰）。
	feedSuccess(c, id1)
	c.SetLimits(id1, 1, 0, 0) // 释放后未活跃

	id5, err := c.Register(NodeRef{TenantID: "t", CredentialID: 5, RawModel: "m"})
	require.NoError(t, err)
	assert.Equal(t, id1, id5, "evicted id should be reused")

	// 旧 ref 不再解析；新 ref 双射成立。
	assert.EqualValues(t, 0, c.NodeID(ref1))
	assert.Equal(t, id1, c.NodeID(NodeRef{TenantID: "t", CredentialID: 5, RawModel: "m"}))

	// 回收时位图/统计已清零。
	assert.False(t, c.bitmap.TestAvail(id1))
	assert.Equal(t, NodeStatsSlot{}, c.StatsSlot(id1))

	// 回调收到 Evicted 事件。
	ev := inv.events()
	require.Len(t, ev, 1)
	assert.Equal(t, InvalidationEvicted, ev[0].Reason)
	assert.Equal(t, id1, ev[0].NodeID)
}

// UT-NS-01c：活跃（持有资源占用）的 id 不被回收；全部活跃且容量满 → ErrRegistryFull。
func TestRegistryEvictionSkipsActive(t *testing.T) {
	c := newTestCache(3, nil)
	var ids []int32
	for i := 1; i <= 3; i++ {
		id, err := c.Register(NodeRef{TenantID: "t", CredentialID: int64(i), RawModel: "m"})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	// 全部置为活跃：并发占用 1 个。
	for _, id := range ids {
		c.SetLimits(id, 1, 0, 0)
		require.True(t, c.TryAcquire(id, ResourceConcurrency))
	}
	_, err := c.Register(NodeRef{TenantID: "t", CredentialID: 99, RawModel: "m"})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRegistryFull))

	// 释放一个后可回收复用。
	c.Release(ids[0], ResourceConcurrency)
	id, err := c.Register(NodeRef{TenantID: "t", CredentialID: 99, RawModel: "m"})
	require.NoError(t, err)
	assert.Equal(t, ids[0], id)
}

// UT-NS-01d：失效广播置脏后重拉——Invalidate 清 Avail/Probe、State=unknown，
// 重喂（Updater 成功）后恢复。
func TestInvalidateMarksDirtyAndReload(t *testing.T) {
	inv := &fakeInvalidation{}
	clk := newFakeClock(testBase)
	c := New(Options{Capacity: 8, StartTickers: false, Clock: clk.Now, Invalidation: inv})
	ref := NodeRef{TenantID: "t", CredentialID: 7, RawModel: "m"}
	id, err := c.Register(ref)
	require.NoError(t, err)
	feedSuccess(c, id)
	require.True(t, c.bitmap.TestAvail(id))

	c.Invalidate(ref)
	assert.False(t, c.bitmap.TestAvail(id), "dirty node must not be routable")
	assert.False(t, c.bitmap.TestProbe(id))
	assert.Equal(t, StateUnknown, c.StatsSlot(id).State)
	ev := inv.events()
	require.Len(t, ev, 1)
	assert.Equal(t, InvalidationPubSub, ev[0].Reason)

	// 重拉：效应链喂入成功 → 恢复可用。
	feedSuccess(c, id)
	assert.True(t, c.bitmap.TestAvail(id))
	assert.Equal(t, StateAvailable, c.StatsSlot(id).State)
}
