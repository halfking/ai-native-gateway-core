package nodestatecache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UT-NS-07：多源不一致收敛——人为注入漂移（缓存值≠权威值）→ Reconcile 以
// 权威（Governor/fpslot/URSM）为准覆盖收敛；二次对账零校正（幂等收敛）。
func TestReconcileConvergesDrift(t *testing.T) {
	clk := newFakeClock(testBase)
	c := newTestCache(32, clk)
	ids := seedNodes(t, c, 3)

	// 注入漂移：
	// ① 位图漂移：id0 缓存 Available，权威 offline（Avail=false）。
	require.True(t, c.bitmap.TestAvail(ids[0]))
	// ② 状态漂移：id1 缓存 degraded（喂一次失败），权威 available。
	c.Update(ids[1], false, ErrKindNetwork, 100)
	require.Equal(t, StateDegraded, c.StatsSlot(ids[1]).State)
	// ③ 资源漂移：id2 缓存 ConcUsed=1/ConcLimit=1（满载），权威 Used=0/Limit=4。
	c.SetLimits(ids[2], 1, 0, 0)
	require.True(t, c.TryAcquire(ids[2], ResourceConcurrency))
	require.True(t, c.bitmap.TestFull(ids[2]))
	// ④ 权威含缓存未知的新节点 id3。
	ref1, _ := c.NodeRef(ids[0])
	ref2, _ := c.NodeRef(ids[1])
	ref3, _ := c.NodeRef(ids[2])
	authority := &fakeAuthority{records: []AuthorityRecord{
		{Ref: ref1, Avail: false, State: StateOffline},
		{Ref: ref2, Avail: true, State: StateAvailable},
		{Ref: ref3, Avail: true, State: StateAvailable, ConcUsed: 0, ConcLimit: 4, FPUsed: 1, FPLimit: 2, RPMUsed: 3, RPMLimit: 10},
		{Ref: NodeRef{TenantID: "t", CredentialID: 99, RawModel: "m"}, Avail: true, State: StateAvailable},
	}}

	rep := c.Reconcile(t.Context(), authority)
	require.Equal(t, 4, rep.Checked)
	assert.Equal(t, 1, rep.Registered)
	// 位图校正 2：id0（缓存 Avail→权威 offline）+ 新节点（置 Avail）。
	assert.Equal(t, 2, rep.CorrectedBitmap)
	// 状态校正 3：id0（→offline）、id1（degraded→available）、新节点（→available）。
	assert.Equal(t, 3, rep.CorrectedState)
	// 资源校正 1：仅 id2（新节点资源全 0 无漂移）。
	assert.Equal(t, 1, rep.CorrectedResource)

	// 收敛断言：
	assert.False(t, c.bitmap.TestAvail(ids[0]), "① authority offline wins")
	assert.Equal(t, StateOffline, c.StatsSlot(ids[0]).State)
	assert.Equal(t, StateAvailable, c.StatsSlot(ids[1]).State)
	assert.True(t, c.bitmap.TestAvail(ids[1]))
	assert.False(t, c.bitmap.TestFull(ids[2]), "③ full cleared under authority limits")
	slot := c.ResourceSlot(ids[2])
	assert.EqualValues(t, 0, slot.ConcUsed)
	assert.EqualValues(t, 4, slot.ConcLimit)
	assert.EqualValues(t, 1, slot.FPUsed)
	assert.EqualValues(t, 2, slot.FPLimit)
	assert.EqualValues(t, 3, slot.RPMUsed)
	assert.EqualValues(t, 10, slot.RPMLimit)
	newID := c.NodeID(NodeRef{TenantID: "t", CredentialID: 99, RawModel: "m"})
	require.NotZero(t, newID)
	assert.True(t, c.bitmap.TestAvail(newID), "④ new node fed from authority")

	// 幂等：二次对账（已收敛）零校正。
	rep2 := c.Reconcile(t.Context(), authority)
	assert.Equal(t, 0, rep2.CorrectedBitmap)
	assert.Equal(t, 0, rep2.CorrectedState)
	assert.Equal(t, 0, rep2.CorrectedResource)
}

// 周期对账 ticker：注入 Authority + 短周期，漂移在周期内被自动校正。
func TestReconcileTickerDrivesConvergence(t *testing.T) {
	clk := newFakeClock(testBase)
	ref := NodeRef{TenantID: "t", CredentialID: 1, RawModel: "m"}
	authority := &fakeAuthority{records: []AuthorityRecord{
		{Ref: ref, Avail: true, State: StateAvailable, ConcLimit: 8},
	}}
	c := New(Options{
		Capacity:        8,
		StartTickers:    true,
		ReconcilePeriod: 10 * time.Millisecond,
		ResetPeriod:     -time.Second, // 关闭重置 ticker（本测试不涉及）
		Clock:           clk.Now,
		Authority:       authority,
	})
	defer c.Close()

	id, err := c.Register(ref)
	require.NoError(t, err)
	require.False(t, c.bitmap.TestAvail(id), "not fed yet")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.bitmap.TestAvail(id) {
			return // ticker 已按权威校正
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("reconcile ticker did not converge in time")
}
