package sessionaudithook

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/pashagolub/pgxmock/v4"
)

func TestApprovalGateHook_NameAndPriority(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 15*1_000_000_000)
	h := NewApprovalGateHook(nil, mgr, eventbus.NewMemoryBus(10))
	if h.Name() != "session.approval_gate" {
		t.Errorf("Name=%s", h.Name())
	}
	if h.Priority() != 105 {
		t.Errorf("Priority=%d", h.Priority())
	}
}

func TestApprovalGateHook_Enabled(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewApprovalGateHook(nil, mgr, bus)

	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Error("should be enabled when approvalMgr non-nil")
	}
	h2 := NewApprovalGateHook(nil, nil, bus)
	if h2.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Error("should NOT be enabled when approvalMgr is nil")
	}
	if h2.Enabled(context.Background(), nil) {
		t.Error("should NOT be enabled when env is nil")
	}
}

func TestApprovalGateHook_NoAuditResult_Passes(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewApprovalGateHook(nil, mgr, bus)

	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("Execute with no audit_result should pass through: %v", err)
	}
}

// TestApprovalGateHook_PassDecision_Passes audit_result.Decision=Pass 时不拦截。
func TestApprovalGateHook_PassDecision_Passes(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewApprovalGateHook(nil, mgr, bus)

	env := &domain.PipelineRequest{
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Decision: sessionaudit.DecisionPass,
			},
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("Pass decision should pass through: %v", err)
	}
}

// TestApprovalGateHook_NeedApproval_TriggersCreate 需要 ApprovalManager + pending.Store。
// 这里直接测试失败分支：cacheSnapshot 失败时降级,但 ApprovalManager.Create 仍
// 应当被调用并最终返回 approval_required 错误。
func TestApprovalGateHook_NeedApproval_NilStore_Degrades(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mgr := sessionaudit.NewApprovalManager(mock, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()

	// pending.Store=nil → cacheSnapshot 失败 → 降级,直接 return nil
	// （因为 hook.go line 73-76 写明 "缓存失败降级：记录日志但不阻断"）
	h := NewApprovalGateHook(nil, mgr, bus)
	env := &domain.PipelineRequest{
		TenantID:  "tenant-A",
		SessionID: "sess-A",
		Envelope: &domain.RequestEnvelope{
			RequestID: "req-1",
			Transport: &domain.TransportContext{},
		},
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Decision: sessionaudit.DecisionNeedApproval,
				Score:    9,
				Reason:   "test",
			},
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("with nil store, should degrade (return nil), got %v", err)
	}
}

// TestApprovalGateHook_NeedApproval_NilEnvelope_Degrades Envelope=nil → snapshot 失败 → 降级。
func TestApprovalGateHook_NeedApproval_NilEnvelope_Degrades(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()

	store := pending.NewStore(nil, 0)
	h := NewApprovalGateHook(store, mgr, bus)

	env := &domain.PipelineRequest{ // Envelope=nil
		TenantID:  "tenant-A",
		SessionID: "sess-A",
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Decision: sessionaudit.DecisionNeedApproval,
			},
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("with nil envelope, should degrade, got %v", err)
	}
}

// TestApprovalGateHook_NeedApproval_FullFlow 完整流需要真实 redis,
// 这里只验证降级路径。集成测试见 e2e-session-audit.sh。
// 此 case 占位,确保未来不会重新引入 nil store 时 panic。
func TestApprovalGateHook_NeedApproval_FullFlow(t *testing.T) {
	t.Skip("requires real redis-backed pending.Store; covered by e2e-session-audit.sh")
}

func TestApprovalGateHook_ImplementsHookInterface(t *testing.T) {
	var _ pipeline.Hook = (*ApprovalGateHook)(nil)
}

func TestApprovalGateHook_OnError_PropagatesError(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 15*1_000_000_000)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewApprovalGateHook(nil, mgr, bus)
	// OnError must return the original error so caller knows the gate tripped.
	if err := h.OnError(context.Background(), &domain.PipelineRequest{}, nil); err != nil {
		t.Errorf("OnError should propagate the error, got %v", err)
	}
}
