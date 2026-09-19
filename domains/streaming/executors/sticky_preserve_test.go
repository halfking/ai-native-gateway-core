package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// TestStickyPreserveBindingDecision 钉住"瞬时失败保持 / 严重失败迁移"的
// 决策矩阵（2026-09-19 会话保持）。
func TestStickyPreserveBindingDecision(t *testing.T) {
	stickyID := 100
	tests := []struct {
		name string
		dctx *dispatchCtx
		served int
		want  bool
	}{
		{name: "nil dctx migrates", dctx: nil, served: 200, want: false},
		{name: "no sticky pin migrates", dctx: &dispatchCtx{}, served: 200, want: false},
		{name: "same credential refreshes normally", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindTimeout}, served: 100, want: false},
		{name: "sticky never attempted (filtered) migrates", dctx: &dispatchCtx{stickyCredID: &stickyID}, served: 200, want: false},
		{name: "transient timeout preserves", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindTimeout}, served: 200, want: true},
		{name: "rate limit preserves", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindRateLimit}, served: 200, want: true},
		{name: "network preserves", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindNetwork}, served: 200, want: true},
		{name: "auth fatal migrates", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindAuth}, served: 200, want: false},
		{name: "quota fatal migrates", dctx: &dispatchCtx{stickyCredID: &stickyID, stickyFailed: true, stickyFailKind: errorsx.KindQuota}, served: 200, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stickyPreserveBinding(tt.dctx, tt.served); got != tt.want {
				t.Fatalf("stickyPreserveBinding() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newStickyPreserveFixture(t *testing.T) (*Executor, *StickyCache, *StickyLoadTracker, *ExecParams) {
	t.Helper()
	ratelimit.EnableRateLimit()
	t.Cleanup(func() { ratelimit.EnableRateLimit() })

	sticky := NewStickyCache()
	t.Cleanup(sticky.Close)
	tracker := NewStickyLoadTracker()
	t.Cleanup(tracker.Close)

	router := &Router{Sticky: sticky, StickyLoad: tracker}
	e := &Executor{Router: router}

	appID, keyID := 1, 2
	params := &ExecParams{
		TenantID:  "t1",
		AppID:     &appID,
		ApiKeyID:  &keyID,
		ClientID:  identity.ClientIdentity{Fingerprint: identity.ClientFingerprint{ClientProfile: "default"}},
		SessionID: "sess1",
		Model:     "gpt-x",
		RequestID: "req1",
	}
	return e, sticky, tracker, params
}

// TestRecordStickySuccessTransientFailoverKeepsBinding: sticky 节点瞬时失败
// 后由其他节点服务 → 会话绑定保持在原节点，会话计数仍归属原节点。
func TestRecordStickySuccessTransientFailoverKeepsBinding(t *testing.T) {
	e, sticky, tracker, params := newStickyPreserveFixture(t)

	// 会话先绑定在 100。
	e.recordStickySuccess(params, 100, nil)

	// 本请求 sticky=100 尝试失败（超时），failover 到 200 成功。
	stickyID := 100
	dctx := &dispatchCtx{
		stickyCredID:   &stickyID,
		stickyFailed:   true,
		stickyFailKind: errorsx.KindTimeout,
	}
	e.recordStickySuccess(params, 200, dctx)

	res := sticky.GetMultiLevel("t1", params.AppID, params.ApiKeyID, "default", "sess1", "gpt-x")
	if !res.Found || res.CredentialID != 100 {
		t.Fatalf("binding should stay on credential 100 after transient failover, got %+v", res)
	}
	if got := tracker.Info(100).Sessions; got != 1 {
		t.Fatalf("session count should stay on 100, got %d", got)
	}
	if got := tracker.Info(200).Sessions; got != 0 {
		t.Fatalf("served-but-unbound node must not absorb the session count, got %d", got)
	}
	if tracker.Info(200).LastActivityMs == 0 {
		t.Fatal("served node activity should still be recorded")
	}
}

// TestRecordStickySuccessFatalFailoverMovesBinding: 严重错误（auth）后
// failover 成功 → 会话绑定迁移到实际服务节点。
func TestRecordStickySuccessFatalFailoverMovesBinding(t *testing.T) {
	e, sticky, tracker, params := newStickyPreserveFixture(t)

	e.recordStickySuccess(params, 100, nil)

	stickyID := 100
	dctx := &dispatchCtx{
		stickyCredID:   &stickyID,
		stickyFailed:   true,
		stickyFailKind: errorsx.KindAuth,
	}
	e.recordStickySuccess(params, 200, dctx)

	res := sticky.GetMultiLevel("t1", params.AppID, params.ApiKeyID, "default", "sess1", "gpt-x")
	if !res.Found || res.CredentialID != 200 {
		t.Fatalf("binding should move to credential 200 after fatal failover, got %+v", res)
	}
	if got := tracker.Info(200).Sessions; got != 1 {
		t.Fatalf("session count should follow the binding to 200, got %d", got)
	}
}

// TestRecordStickySuccessFilteredStickyNodeMovesBinding: sticky 节点被
// 可用性过滤（冷却/熔断），从未被尝试 → 会话迁移（严重情形）。
func TestRecordStickySuccessFilteredStickyNodeMovesBinding(t *testing.T) {
	e, sticky, _, params := newStickyPreserveFixture(t)

	e.recordStickySuccess(params, 100, nil)

	stickyID := 100
	dctx := &dispatchCtx{stickyCredID: &stickyID} // stickyFailed=false
	e.recordStickySuccess(params, 200, dctx)

	res := sticky.GetMultiLevel("t1", params.AppID, params.ApiKeyID, "default", "sess1", "gpt-x")
	if !res.Found || res.CredentialID != 200 {
		t.Fatalf("binding should move when the pinned node was never attempted, got %+v", res)
	}
}

// TestRecordStickySuccessNormalPathObservesSession: 正常成功（无迁移）
// 刷新绑定并把会话计入滑窗。
func TestRecordStickySuccessNormalPathObservesSession(t *testing.T) {
	e, sticky, tracker, params := newStickyPreserveFixture(t)

	e.recordStickySuccess(params, 100, nil)

	res := sticky.GetMultiLevel("t1", params.AppID, params.ApiKeyID, "default", "sess1", "gpt-x")
	if !res.Found || res.CredentialID != 100 {
		t.Fatalf("binding should be on 100, got %+v", res)
	}
	if got := tracker.Info(100).Sessions; got != 1 {
		t.Fatalf("session should be observed on binding write, got %d", got)
	}
}
