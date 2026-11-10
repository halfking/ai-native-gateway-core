package streaming

import (
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/stretchr/testify/require"
)

// passiveSinkSpy 记录 handler 交给 sink 的被动特征。
type passiveSinkSpy struct {
	feats []*modelcredibility.PassiveFeatures
}

func (s *passiveSinkSpy) Submit(f *modelcredibility.PassiveFeatures) {
	s.feats = append(s.feats, f)
}

// passiveFactory 生产真实的 PassiveTimingTracker（与生产同构）。
type passiveFactory struct{}

func (passiveFactory) NewPassiveTimingTracker() *PassiveTimingTracker {
	return NewPassiveTimingTracker(time.Now())
}

// 2026-11-10 审计 P0-B 的回归门：**端到端**测试（emitTelemetry → Submit）。
//
// ★ 为什么必须是端到端：原缺陷是「采样点位于
// `if len(result.ResponseBody) > 0` 之内」，而流式成功路径的
// ExecuteResult 不含 ResponseBody ⇒ Submit 永不执行。
// ⇒ 只测 PassiveTimingTracker / ExtractPassiveFromTracker 这些纯函数
// **永远抓不到**它（仓内原有的 passive_timing_tracker_test.go 就没抓到）。
//
// ★ 判据：走真实生产形态（**流式**：capture 非 nil + result.ResponseBody
// 为空）⇒ sink 必须收到 1 条特征。
func TestEmitTelemetry_SubmitsPassiveFeaturesOnStreamingLane(t *testing.T) {
	ch := &ChatHandler{}
	spy := &passiveSinkSpy{}
	ch.SetPassiveTiming(passiveFactory{}, spy)

	// 流式 lane 的真实形态：capture 非 nil（tracker 挂在 capture 的观察者槽）。
	capture := audit.NewStreamCapture()
	tr := NewPassiveTimingTracker(time.Now())
	capture.SetTextObserver(tr)
	// 模拟流式增量：至少两次文本增量 ⇒ tracker 才有 TTFT + ITT 样本。
	tr.ObserveText("hello ")
	time.Sleep(2 * time.Millisecond)
	tr.ObserveText("world")
	// 流式 usage 走 capture（ObserveUsage）⇒ 采样点从 capture 摘要读 token。
	ct := 40
	capture.ObserveUsage(nil, &ct, nil, nil)

	// ★ 流式成功路径的 ExecuteResult **不含 ResponseBody**（这是缺陷现场）。
	result := &executors.ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 3, ProviderID: 5},
		LatencyMs: 42,
		Response:  &http.Response{StatusCode: http.StatusOK, Header: http.Header{}},
	}

	evt := audit.Event{
		RequestID:     "passive-e2e-1",
		ClientModel:   "auto",
		OutboundModel: "glm-5.2",
		CanonicalName: "glm-5.2",
		IdentityHash:  "hash-p1",
	}
	logCtx := &RequestLogContext{RequestID: "passive-e2e-1"}

	require.NotPanics(t, func() {
		ch.emitTelemetry(evt, result, "user-p1", nil, capture, "chat", nil,
			[]byte(`{"model":"auto"}`), nil, logCtx)
	})

	require.Len(t, spy.feats, 1,
		"the passive sampler must submit exactly one feature set on the streaming lane (before the fix: 0 — the sink was never called)")
	f := spy.feats[0]
	require.Equal(t, "glm-5.2", f.ModelName)
	require.Equal(t, int64(3), f.CredentialID)
	require.Equal(t, "passive-e2e-1", f.RequestID)
}

// TestEmitTelemetry_PassiveSamplingIsNoOpWhenUnwired 钉住「零开销旁路」：
// 工厂或 sink 任一未接线 ⇒ 不产出、不 panic、legacy 行为不变。
func TestEmitTelemetry_PassiveSamplingIsNoOpWhenUnwired(t *testing.T) {
	// sink 未接线（工厂也不接线）
	ch := &ChatHandler{}
	capture := audit.NewStreamCapture()
	tr := NewPassiveTimingTracker(time.Now())
	capture.SetTextObserver(tr)
	tr.ObserveText("x")

	result := &executors.ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 3, ProviderID: 5},
		LatencyMs: 42,
		Response:  &http.Response{StatusCode: http.StatusOK, Header: http.Header{}},
	}
	evt := audit.Event{RequestID: "passive-e2e-2", CanonicalName: "glm-5.2"}
	logCtx := &RequestLogContext{RequestID: "passive-e2e-2"}

	require.NotPanics(t, func() {
		ch.emitTelemetry(evt, result, "user-p2", nil, capture, "chat", nil,
			[]byte(`{"model":"auto"}`), nil, logCtx)
	})

	// 工厂接线但 sink 未接线 ⇒ 同样旁路
	ch2 := &ChatHandler{}
	ch2.SetPassiveTiming(passiveFactory{}, nil)
	require.NotPanics(t, func() {
		ch2.emitTelemetry(evt, result, "user-p2", nil, capture, "chat", nil,
			[]byte(`{"model":"auto"}`), nil, logCtx)
	})
}
