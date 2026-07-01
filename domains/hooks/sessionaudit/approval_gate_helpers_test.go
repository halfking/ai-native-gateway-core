package sessionaudithook

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// TestApprovalGateHook_BuildSnapshot_NilEnvelope buildSnapshot 必须不 panic。
func TestApprovalGateHook_BuildSnapshot_NilEnvelope(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 0)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	store := pending.NewStore(nil, 0)
	h := NewApprovalGateHook(store, mgr, bus)

	env := &domain.PipelineRequest{Metadata: map[string]any{}}
	snap := h.buildSnapshot(env, &sessionaudit.DetectResult{Score: 9})
	if snap != nil {
		t.Errorf("expected nil snapshot for nil envelope, got %+v", snap)
	}
}

// TestApprovalGateHook_BuildSnapshot_WithTransport 验证字段映射。
func TestApprovalGateHook_BuildSnapshot_WithTransport(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 0)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	store := pending.NewStore(nil, 0)
	h := NewApprovalGateHook(store, mgr, bus)

	env := domain.NewRequestEnvelope(context.Background(), &domain.RequestEnvelope{
		RequestID: "req-xyz",
		Transport: &domain.TransportContext{ClientModel: "gpt-4o"},
	})
	env.SessionID = "sess-X"
	env.TenantID = "tenant-X"

	snap := h.buildSnapshot(env, &sessionaudit.DetectResult{Score: 8})
	if snap == nil {
		t.Fatal("snapshot is nil")
	}
	if snap.SessionID != "sess-X" || snap.TenantID != "tenant-X" {
		t.Errorf("SessionID/TenantID mismatch: %+v", snap)
	}
	if snap.RequestID != "req-xyz" {
		t.Errorf("RequestID=%s", snap.RequestID)
	}
	if snap.ClientModel != "gpt-4o" {
		t.Errorf("ClientModel=%s", snap.ClientModel)
	}
	if snap.DetectResult == nil || snap.DetectResult.Score != 8 {
		t.Errorf("DetectResult=%+v", snap.DetectResult)
	}
	if snap.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

// TestApprovalGateHook_CacheSnapshot_NilStore cacheSnapshot 在 store=nil 时返回 error。
func TestApprovalGateHook_CacheSnapshot_NilStore(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 0)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	h := NewApprovalGateHook(nil, mgr, bus)

	snap := &sessionaudit.RequestSnapshot{
		SessionID: "sess-1",
		TenantID:  "tenant-1",
		RequestID: "req-1",
	}
	err := h.cacheSnapshot(context.Background(), snap)
	if err == nil {
		t.Error("expected error when pendingStore is nil")
	}
}

// 防御：cacheSnapshot 即便对合法 snapshot 也不会 panic。
func TestApprovalGateHook_CacheSnapshot_NilRedisDegrades(t *testing.T) {
	mgr := sessionaudit.NewApprovalManager(nil, 0)
	bus := eventbus.NewMemoryBus(10)
	defer bus.Close()
	store := pending.NewStore(nil, 0) // nil redis → ErrUnavailable
	h := NewApprovalGateHook(store, mgr, bus)

	snap := &sessionaudit.RequestSnapshot{
		SessionID: "sess-1",
		TenantID:  "tenant-1",
		RequestID: "req-1",
	}
	if err := h.cacheSnapshot(context.Background(), snap); !errors.Is(err, pending.ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}
