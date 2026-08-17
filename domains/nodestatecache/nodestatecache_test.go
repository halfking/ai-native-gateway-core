package nodestatecache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 构造器默认值：容量 65536、阈值 3、退避 30s/30m、36h 窗、1m/30s ticker、time.Now。
func TestOptionsDefaults(t *testing.T) {
	o := Options{}
	o.fillDefaults()
	assert.Equal(t, DefaultCapacity, o.Capacity)
	assert.EqualValues(t, 3, o.FailStreakLimit)
	assert.Equal(t, 30*time.Second, o.BackoffBase)
	assert.Equal(t, 30*time.Minute, o.BackoffCap)
	assert.Equal(t, 36*time.Hour, o.RecentSuccessWindow)
	assert.Equal(t, time.Minute, o.ResetPeriod)
	assert.Equal(t, 30*time.Second, o.ReconcilePeriod)
	assert.NotNil(t, o.Clock)
}

// New 返回三闭包 + 资源仪表组 + 对账，全部可用。
func TestNewReturnsClosureGroup(t *testing.T) {
	c := New(Options{StartTickers: false})
	defer c.Close()
	require.NotNil(t, c.Selector())
	require.NotNil(t, c.Updater())
	require.NotNil(t, c.TryAcquire)
	assert.NotNil(t, c.NeedProbe)

	id, err := c.Register(NodeRef{TenantID: "t", CredentialID: 1, RawModel: "m"})
	require.NoError(t, err)
	c.Updater()(id, true, ErrKindNone, 10)
	assert.True(t, c.bitmap.TestAvail(id))
	c.SetLimits(id, 1, 1, 1)
	assert.True(t, c.TryAcquire(id, ResourceConcurrency))
	assert.True(t, c.bitmap.TestFull(id))
	c.Release(id, ResourceConcurrency)
	assert.False(t, c.bitmap.TestFull(id))
}

// 负周期关闭对应 ticker（不 panic、不空转）。
func TestNewNegativePeriodsDisableTickers(t *testing.T) {
	c := New(Options{
		StartTickers:    true,
		ResetPeriod:     -time.Second,
		ReconcilePeriod: -time.Second,
	})
	c.Close()
	c.Close() // 幂等
}

// Reconcile(nil snapshot) 安全空转。
func TestReconcileNilSnapshot(t *testing.T) {
	c := newTestCache(8, nil)
	rep := c.Reconcile(t.Context(), nil)
	assert.Equal(t, ReconcileReport{}, rep)
}
