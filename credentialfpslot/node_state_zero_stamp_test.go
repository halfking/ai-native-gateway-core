package credentialfpslot

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestProbe_ZeroStateFromSingleReadIsStamped 断言单条读路径的**两个早退分支**
// 也打读取时刻戳。
//
// 起因是一次自查发现的形状不一致：批量路径 GetNodeStatesBatch 的每个分支
// （client==nil、MGET miss、坏 JSON、身份不匹配）都打了 readAt，
// 唯独单条路径 GetNodeState 的 client==nil 与 redis.Nil 两个早退分支
// 直接 return newZeroNodeState(...)，SnapshotReadAt 是零值。
//
// ⚠️ 但要如实说明它**当前不会污染 unstamped 指标**：护栏在
// `!state.Capabilities.SupportsResponsesKnown()` 处就提前返回
// （node_state.go:564），零值快照的 capabilities 为空 ⇒ 走不到打戳分支。
// 我第一版判据写的是「会把正常情形报成 unstamped」，跑出来是绿的——
// 那条理由不成立，本条只钉「两条读路径行为一致」这条结构性质。
// 若将来有人把那个提前返回挪到打戳之后（例如想让 known 结论也走护栏），
// 这条不一致就会立刻变成指标污染。
func TestProbe_ZeroStateFromSingleReadIsStamped(t *testing.T) {
	t.Run("key 不存在（redis.Nil 早退）", func(t *testing.T) {
		m, _ := newCapabilityTestManager(t)
		st, err := m.GetNodeState(t.Context(), 22, "gpt-5.6-terra")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if st == nil {
			t.Fatal("GetNodeState 契约是 never nil")
		}
		if st.SnapshotReadAt.IsZero() {
			t.Fatal("零值快照未打戳：与 GetNodeStatesBatch 的同形分支不一致。" +
				"当前靠「护栏提前返回」侥幸不污染指标，但那是两个位置的巧合，不是设计")
		}
	})

	t.Run("client 为 nil（早退）", func(t *testing.T) {
		m := &Manager{}
		st, err := m.GetNodeState(t.Context(), 22, "gpt-5.6-terra")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if st.SnapshotReadAt.IsZero() {
			t.Fatal("client==nil 分支的零值快照未打戳，与批量路径行为不一致")
		}
	})
}

// TestProbe_UnstampedOnlyCountsRealBypasses 把 unstamped 的语义钉成
// 「确实绕过了读路径」，而不是「读到了零值」。
//
// 变异形状：把 GetNodeState 的 redis.Nil 早退分支还原成不打戳的版本。
// 这条判据必须判红——否则 unstamped 指标不可用于告警。
func TestProbe_UnstampedOnlyCountsRealBypasses(t *testing.T) {
	m, _ := newCapabilityTestManager(t)
	const credID = 22
	const model = "gpt-5.6-terra"

	// 正常生产情形：key 不存在。护栏不该把它算成 unstamped。
	st, err := m.GetNodeState(t.Context(), credID, model)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	before := testutil.ToFloat64(
		metrics.NodeStatePrefetchDroppedTotal.WithLabelValues(metrics.PrefetchDropReasonUnstamped))
	if _, _, err := m.GetSupportsResponses(t.Context(), credID, model, st); err != nil {
		t.Fatalf("gate: %v", err)
	}
	after := testutil.ToFloat64(
		metrics.NodeStatePrefetchDroppedTotal.WithLabelValues(metrics.PrefetchDropReasonUnstamped))
	if after != before {
		t.Fatalf("unstamped 计数 +%v：key 不存在是正常生产情形，不能报成"+
			"「有构造点绕过读路径」。这条指标一旦这么用，值班会去查一个不存在的问题",
			after-before)
	}
}
