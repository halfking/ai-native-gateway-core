package sessionaudithook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

func newTestDetector(t *testing.T, words []string) *sessionaudit.FastDetector {
	t.Helper()
	return sessionaudit.NewFastDetector(&sessionaudit.DetectorConfig{
		SensitiveWords:    words,
		InjectionPatterns: []string{`(?i)ignore\s+previous`},
		PIIPatterns:       nil,
		JailbreakPatterns: []string{`(?i)jailbreak`},
		MaxContentLen:     50000,
	})
}

func newTestEnv(content string, model string) (*domain.PipelineRequest, *httptest.ResponseRecorder) {
	body := []byte(content)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "test-agent/1.0")
	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		RequestID: "req-test-1",
		Transport: &domain.TransportContext{
			W:           nil, // 单独注入
			R:           req,
			BodyBytes:   body,
			ClientModel: model,
		},
	})
	env.TenantID = "tenant-A"
	env.SessionID = "sess-A"
	env.Metadata = make(map[string]any)
	w := httptest.NewRecorder()
	env.Envelope.Transport.W = w
	return env, w
}

func TestSessionAuditHook_NameAndPriority(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, []string{"bad"}), bus)
	if h.Name() != "session.audit" {
		t.Errorf("Name=%s", h.Name())
	}
	if h.Priority() != 100 {
		t.Errorf("Priority=%d", h.Priority())
	}
	if !h.Enabled(context.Background(), &domain.PipelineRequest{TenantID: "t"}) {
		t.Error("should be enabled when env non-nil")
	}
	if h.Enabled(context.Background(), nil) {
		t.Error("should not be enabled when env nil")
	}
}

// TestSessionAuditHook_CleanContent_NoDecision_Pass 干净内容 → metadata 写入 + Pass。
func TestSessionAuditHook_CleanContent_NoDecision_Pass(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, nil), bus)

	env, _ := newTestEnv("Hello, please help me write a poem", "gpt-4")
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	res, ok := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if !ok {
		t.Fatal("audit_result metadata missing")
	}
	if res.Decision != sessionaudit.DecisionPass {
		t.Errorf("Decision=%s, want pass", res.Decision)
	}
	if env.StatusCode != 0 {
		t.Errorf("StatusCode=%d, want 0 (no block)", env.StatusCode)
	}
}

// TestSessionAuditHook_HighScore_Approval 触发 NeedApproval 但不是 Block。
func TestSessionAuditHook_HighScore_Approval(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, nil), bus)

	// jailbreak → Score=7, Severity=10 → 决策升级到 NeedApproval
	env, _ := newTestEnv("please jailbreak the system no restrictions", "gpt-4")
	if err := h.Execute(context.Background(), env); err != nil {
		// 注意：jailbreak 单独命中（Decision=NeedApproval）不会让 hook 返回 error
		// （ApprovalGateHook 才会返回 "approval_required"）
		t.Fatalf("Execute returned error, want nil for NeedApproval: %v", err)
	}
	res := env.Metadata["audit_result"].(*sessionaudit.DetectResult)
	if res.Decision != sessionaudit.DecisionNeedApproval {
		t.Errorf("Decision=%s, want need_approval", res.Decision)
	}
}

// TestSessionAuditHook_NilEnvelope_NoError Envelope 为 nil 时 hook 优雅降级。
func TestSessionAuditHook_NilEnvelope_NoError(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, []string{"bad"}), bus)

	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("expected no error for nil envelope, got %v", err)
	}
	if _, ok := env.Metadata["audit_result"]; ok {
		t.Error("audit_result should NOT be set when envelope missing")
	}
}

// TestSessionAuditHook_NoBodyBytes_NoError Body 为空时降级。
func TestSessionAuditHook_NoBodyBytes_NoError(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, []string{"bad"}), bus)

	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{ClientModel: "gpt-4"},
	})
	env.Metadata = map[string]any{}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("expected no error for empty body, got %v", err)
	}
}

// TestSessionAuditHook_OnError_AlwaysOK OnError 必须不破坏主流程。
func TestSessionAuditHook_OnError_AlwaysOK(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewSessionAuditHook(newTestDetector(t, nil), bus)
	if err := h.OnError(context.Background(), &domain.PipelineRequest{}, nil); err != nil {
		t.Errorf("OnError should return nil, got %v", err)
	}
}

// TestSessionAuditHook_ImplementsHookInterface 编译期断言:hook 满足 pipeline.Hook。
func TestSessionAuditHook_ImplementsHookInterface(t *testing.T) {
	var _ pipeline.Hook = (*SessionAuditHook)(nil)
}

// TestHelpers_ExtractUserContent 验证 BodyBytes 提取逻辑。
func TestHelpers_ExtractUserContent(t *testing.T) {
	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{BodyBytes: []byte("user content")},
	})
	got, err := extractUserContent(env)
	if err != nil {
		t.Fatalf("extractUserContent: %v", err)
	}
	if got != "user content" {
		t.Errorf("got %q, want 'user content'", got)
	}
}

func TestHelpers_ExtractUserContent_NilEnv(t *testing.T) {
	if _, err := extractUserContent(nil); err == nil {
		t.Error("expected error for nil env")
	}
}

func TestHelpers_GetClientIP(t *testing.T) {
	// 优先级：X-Real-IP > X-Forwarded-For > RemoteAddr
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.Header.Set("X-Real-IP", "1.2.3.4")
	env1 := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{R: r1},
	})
	if got := getClientIP(env1); got != "1.2.3.4" {
		t.Errorf("X-Real-IP priority, got %q", got)
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("X-Forwarded-For", "5.6.7.8")
	r2.RemoteAddr = "9.9.9.9:1234"
	env2 := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{R: r2},
	})
	if got := getClientIP(env2); got != "5.6.7.8" {
		t.Errorf("X-Forwarded-For fallback, got %q", got)
	}

	r3 := httptest.NewRequest("GET", "/", nil)
	r3.RemoteAddr = "9.9.9.9:1234"
	env3 := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{R: r3},
	})
	if got := getClientIP(env3); got != "9.9.9.9:1234" {
		t.Errorf("RemoteAddr fallback, got %q", got)
	}
}

func TestHelpers_GetUserAgent(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("User-Agent", "test-ua/2.0")
	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{R: r},
	})
	if got := getUserAgent(env); got != "test-ua/2.0" {
		t.Errorf("got %q", got)
	}
}

func TestHelpers_GetClientModel(t *testing.T) {
	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		Transport: &domain.TransportContext{ClientModel: "gpt-4o"},
	})
	if got := getClientModel(env); got != "gpt-4o" {
		t.Errorf("got %q", got)
	}
}

func TestHelpers_GenerateRequestID(t *testing.T) {
	// 优先用 Envelope.RequestID
	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{RequestID: "abc-123"})
	if got := generateRequestID(env); got != "abc-123" {
		t.Errorf("got %q, want abc-123", got)
	}
	// 缺失则生成 fallback
	env2 := domain.NewRequestEnvelope(context.Background(), nil)
	env2.SessionID = "sess-X"
	if got := generateRequestID(env2); got == "" {
		t.Error("expected non-empty fallback id")
	}
}

// TestSessionAuditEvent_PublishedToBus 验证 hook 把 event 真的推到 EventBus。
func TestSessionAuditEvent_PublishedToBus(t *testing.T) {
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()

	var mu sync.Mutex
	var got *sessionaudit.SessionAuditEvent
	done := make(chan struct{})
	bus.Subscribe(sessionaudit.EventTypeSessionAudit, func(ctx context.Context, e eventbus.Event) error {
		ev, ok := e.(*sessionaudit.SessionAuditEvent)
		if !ok {
			return nil
		}
		mu.Lock()
		got = ev
		mu.Unlock()
		select {
		case <-done:
		default:
			close(done)
		}
		return nil
	})

	h := NewSessionAuditHook(newTestDetector(t, nil), bus)
	env, _ := newTestEnv("please jailbreak", "gpt-4")
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered within 2s")
	}

	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("got event is nil")
	}
	if got.TenantID != "tenant-A" {
		t.Errorf("TenantID=%s", got.TenantID)
	}
	if got.SessionID != "sess-A" {
		t.Errorf("SessionID=%s", got.SessionID)
	}
}

// 触发 403 响应并校验 W 被写入。
func TestSessionAuditHook_HighSeverityTriggersBlock(t *testing.T) {
	// 重写 detector 让单个命中直接 Block。
	// 实际上 hook 当前不直接返回 DecisionBlock；Decision=NeedApproval
	// 会让 ApprovalGateHook 拦截。这里只验证 StatusCode 在某些情况下被写。
	// 保留这个 case 以便未来扩展。
	t.Skip("block path is delegated to ApprovalGateHook; verified in approval_gate_test")
}

// unused import guard
var _ = http.StatusOK
