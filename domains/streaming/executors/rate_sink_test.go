package executors

// rate_sink_test.go — B2（429 沉底排序）的门。
//
// 判据（研究方案 §五 B2 验收量具）：
//	① 429 写键（Mark ⇒ Get 可见）；
//	② 下一次 plan 沉底（applyRateSinkOrder 把近期被 429 者排到未被限流者之后）；
//	③ TTL 过期回原序（expiresAt 过后信号消失 ⇒ 逐元素一致）；
//	④ quota 类不写键（调用面只有 KindRateLimit 进 MarkRateLimited ——
//	   由 executor_dispatch 的接线形态保证，本文件用 sinkOrderBy 的
//	   分段稳定性断言排序语义 + 先沉者在前）。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// 判据①：Mark 写键。
func TestRateSinkMarkAndGet(t *testing.T) {
	s := NewRateSink(nil) // nil store ⇒ 进程内默认
	s.Mark(context.Background(), 7, "m")
	if got := s.sinkAt(context.Background(), 7, "m"); got.IsZero() {
		t.Fatal("Mark must record a sink signal")
	}
	// 不同键不受影响。
	if got := s.sinkAt(context.Background(), 8, "m"); !got.IsZero() {
		t.Fatalf("unrelated key must have no signal, got %v", got)
	}
}

// 判据②：沉底排序（未沉段保持原序，被沉段在后）。
func TestApplyRateSinkOrderSinks(t *testing.T) {
	s := NewRateSink(nil)
	s.Mark(context.Background(), 2, "m") // 候选 2 被限流
	r := &Router{RateSink: s}
	in := []provider.Candidate{
		{CredentialID: 1, RawModel: "m"},
		{CredentialID: 2, RawModel: "m"},
		{CredentialID: 3, RawModel: "m"},
	}
	out := r.applyRateSinkOrder(context.Background(), in)
	if out[0].CredentialID != 1 || out[1].CredentialID != 3 || out[2].CredentialID != 2 {
		t.Fatalf("order = %d,%d,%d want 1,3,2 (sunk last)", out[0].CredentialID, out[1].CredentialID, out[2].CredentialID)
	}
}

// 判据③：TTL 过期回原序。
func TestRateSinkTTLExpiryRestoresOrder(t *testing.T) {
	store := NewMemoryRateSink()
	// 手工写入一个已过期的条目（等价于 TTL 自然流走）。
	store.Put(context.Background(), 2, "m", time.Now().Add(-2*RateSinkTTL), RateSinkTTL)
	s := &RateSink{store: store}
	r := &Router{RateSink: s}
	in := []provider.Candidate{
		{CredentialID: 1, RawModel: "m"},
		{CredentialID: 2, RawModel: "m"},
	}
	out := r.applyRateSinkOrder(context.Background(), in)
	if out[0].CredentialID != 1 || out[1].CredentialID != 2 {
		t.Fatalf("expired signal must not reorder: got %d,%d", out[0].CredentialID, out[1].CredentialID)
	}
}

// 未接线（nil RateSink）⇒ 零变化（旧行为）。
func TestRateSinkNilUnchanged(t *testing.T) {
	r := &Router{} // RateSink nil
	in := []provider.Candidate{{CredentialID: 1, RawModel: "m"}, {CredentialID: 2, RawModel: "m"}}
	out := r.applyRateSinkOrder(context.Background(), in)
	if out[0].CredentialID != 1 || out[1].CredentialID != 2 {
		t.Fatal("nil RateSink must be a no-op")
	}
	// Router.MarkRateLimited 也不 panic。
	r.MarkRateLimited(context.Background(), 1, "m")
}

// 先沉者在前（magpie sinkOrder 语义：最早被限流者最先轮到再试）。
func TestSinkOrderByEarliestFirst(t *testing.T) {
	now := time.Now()
	cands := []provider.Candidate{
		{CredentialID: 1, RawModel: "m"},
		{CredentialID: 2, RawModel: "m"},
		{CredentialID: 3, RawModel: "m"},
		{CredentialID: 4, RawModel: "m"},
	}
	times := []time.Time{
		{},                         // 1 未沉
		now.Add(-10 * time.Minute), // 2 先沉
		{},                         // 3 未沉
		now.Add(-5 * time.Minute),  // 4 后沉
	}
	out := sinkOrderBy(cands, times)
	want := []int{1, 3, 2, 4}
	for i, c := range out {
		if c.CredentialID != want[i] {
			t.Fatalf("out[%d] = cred %d want %d (open first, earliest sink first)", i, c.CredentialID, want[i])
		}
	}
}

// 全员未沉 ⇒ 逐元素一致（零变化）。
func TestSinkOrderByNoSignalUnchanged(t *testing.T) {
	cands := []provider.Candidate{{CredentialID: 1, RawModel: "m"}, {CredentialID: 2, RawModel: "m"}}
	out := sinkOrderBy(cands, []time.Time{{}, {}})
	if out[0].CredentialID != 1 || out[1].CredentialID != 2 {
		t.Fatal("no signal must be element-identical")
	}
}
