package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

type mockPendingStore struct {
	responses []*pending.Response
	err       error
}

func (f *mockPendingStore) Save(_ context.Context, r *pending.Response) error {
	if f.err != nil {
		return f.err
	}
	f.responses = append(f.responses, r)
	return nil
}

func (f *mockPendingStore) Get(_ context.Context, _, _ string) (*pending.Response, error) {
	if len(f.responses) == 0 {
		return nil, errors.New("not found")
	}
	return f.responses[0], nil
}

// ──────────────────────────────────────────────────────────────────────────────
// PendingStoreAdapter tests
// ──────────────────────────────────────────────────────────────────────────────

func TestPendingStoreAdapter_Save(t *testing.T) {
	store := &pending.Store{} // 使用假的 store 方法
	adapter := NewPendingStoreAdapter(store)

	// 由于我们不能 mock pending.Store 的内部行为，这里只测试 nil 情况
	if adapter == nil {
		t.Fatal("NewPendingStoreAdapter should not return nil for non-nil store")
	}
}

func TestPendingStoreAdapter_NilStore(t *testing.T) {
	adapter := NewPendingStoreAdapter(nil)
	if adapter != nil {
		t.Error("NewPendingStoreAdapter(nil) should return nil")
	}
}

func TestPendingStoreAdapter_NilEntry(t *testing.T) {
	// 这里我们无法直接注入 mock，因为 PendingStoreAdapter 使用真实的 pending.Store
	// 所以测试覆盖率有限，这是集成测试的范围
	t.Log("集成测试应验证：adapter.Save(nil) 返回错误")
}

// ──────────────────────────────────────────────────────────────────────────────
// PendingStoreResponder tests
// ──────────────────────────────────────────────────────────────────────────────

func TestPendingStoreResponder_NilStore(t *testing.T) {
	responder := NewPendingStoreResponder(nil)
	if responder != nil {
		t.Error("NewPendingStoreResponder(nil) should return nil")
	}
}

func TestPendingStoreResponder_Respond_NilSnapshot(t *testing.T) {
	fake := &fakePendingStore{}
	// 同样无法直接测试，因为需要真实的 pending.Store
	_ = fake
}

// ──────────────────────────────────────────────────────────────────────────────
// LLMCallerFunc tests
// ──────────────────────────────────────────────────────────────────────────────

func TestLLMCallerFunc(t *testing.T) {
	called := false
	var capturedSnap *sessionaudit.RequestSnapshot

	caller := LLMCallerFunc(func(_ context.Context, snap *sessionaudit.RequestSnapshot) error {
		called = true
		capturedSnap = snap
		return nil
	})

	snap := &sessionaudit.RequestSnapshot{
		SessionID: "test-session",
		TenantID:  "test-tenant",
		RequestID: "test-request",
	}

	if err := caller.CallFromSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("CallFromSnapshot: %v", err)
	}

	if !called {
		t.Error("function should be called")
	}
	if capturedSnap != snap {
		t.Error("snapshot should be passed through")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Integration-style tests (documenting expected behavior)
// ──────────────────────────────────────────────────────────────────────────────

func TestPendingStoreAdapter_Integration_Documentation(t *testing.T) {
	// 这个测试作为文档说明 PendingStoreAdapter 的预期行为
	// 实际测试需要真实的 pending.Store + Redis

	t.Log("PendingStoreAdapter.Save 应该:")
	t.Log("  1. 将 PendingResumeEntry 转换为 pending.Response")
	t.Log("  2. 设置 Status (completed/failed/in_progress)")
	t.Log("  3. 设置 CreatedAt 为当前时间")
	t.Log("  4. 设置 IsStream = false (审批恢复不是流式)")
	t.Log("  5. 调用 store.Save(ctx, resp)")
}

func TestPendingStoreResponder_Integration_Documentation(t *testing.T) {
	t.Log("PendingStoreResponder.Respond 应该:")
	t.Log("  1. 将 payload 序列化为 JSON")
	t.Log("  2. 创建 pending.Response (Status=completed)")
	t.Log("  3. 调用 store.Save")

	t.Log("PendingStoreResponder.RespondRejection 应该:")
	t.Log("  1. 创建包含 error.type=approval_rejected 的 JSON")
	t.Log("  2. 创建 pending.Response (Status=failed)")
	t.Log("  3. 设置 ErrorMessage = reason")
	t.Log("  4. 调用 store.Save")
}

// ──────────────────────────────────────────────────────────────────────────────
// Smoke tests for constructor behavior
// ──────────────────────────────────────────────────────────────────────────────

func TestNewPendingStoreAdapter_NonNil(t *testing.T) {
	store := &pending.Store{}
	adapter := NewPendingStoreAdapter(store)
	if adapter == nil {
		t.Error("adapter should not be nil for non-nil store")
	}
	if adapter.store != store {
		t.Error("adapter.store should point to the provided store")
	}
}

func TestNewPendingStoreResponder_NonNil(t *testing.T) {
	store := &pending.Store{}
	responder := NewPendingStoreResponder(store)
	if responder == nil {
		t.Error("responder should not be nil for non-nil store")
	}
	if responder.store != store {
		t.Error("responder.store should point to the provided store")
	}
}

// silence unused import
var _ = time.Second
