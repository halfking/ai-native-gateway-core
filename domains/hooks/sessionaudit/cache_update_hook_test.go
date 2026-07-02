package sessionaudithook

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

func newAuditEnv(sessionID, tenantID string, decision sessionaudit.Decision, score int, sensitiveWords []string) *domain.PipelineRequest {
	return &domain.PipelineRequest{
		TenantID:  tenantID,
		SessionID: sessionID,
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Score:          score,
				SensitiveWords: sensitiveWords,
				Decision:       decision,
				Reason:         "test",
			},
		},
		Envelope: &domain.RequestEnvelope{
			Transport: &domain.TransportContext{
				W:         httptest.NewRecorder(),
				BodyBytes: []byte(`{"model":"gpt-4o"}`),
			},
		},
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestCacheUpdateHook_NameAndPriority(t *testing.T) {
	h := NewCacheUpdateHook(nil)
	if h.Name() != "sessionaudit.cache_update" {
		t.Errorf("Name: got %q want sessionaudit.cache_update", h.Name())
	}
	if h.Priority() != 400 {
		t.Errorf("Priority: got %d want 400", h.Priority())
	}
}

func TestCacheUpdateHook_Enabled_NoCache(t *testing.T) {
	h := NewCacheUpdateHook(nil)
	env := newAuditEnv("s", "t", sessionaudit.DecisionPass, 1, nil)
	if h.Enabled(context.Background(), env) {
		t.Error("should be disabled when cache is nil")
	}
}

func TestCacheUpdateHook_Enabled_NoSessionID(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)
	env := newAuditEnv("", "t", sessionaudit.DecisionPass, 1, nil)
	if h.Enabled(context.Background(), env) {
		t.Error("should be disabled when session_id is empty")
	}
}

func TestCacheUpdateHook_Enabled_NoAuditMetadata(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)
	env := &domain.PipelineRequest{TenantID: "t", SessionID: "s"}
	if h.Enabled(context.Background(), env) {
		t.Error("should be disabled without audit_result metadata")
	}
}

func TestCacheUpdateHook_Enabled_WithMetadata(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)
	env := newAuditEnv("s", "t", sessionaudit.DecisionPass, 1, nil)
	if !h.Enabled(context.Background(), env) {
		t.Error("should be enabled with audit_result metadata")
	}
}

func TestCacheUpdateHook_Execute_NilCache(t *testing.T) {
	h := NewCacheUpdateHook(nil)
	env := newAuditEnv("s", "t", sessionaudit.DecisionPass, 1, nil)
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("nil cache should be no-op, got: %v", err)
	}
}

func TestCacheUpdateHook_Execute_StampsV6Fields(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	now := time.Unix(1700000000, 0)
	h := NewCacheUpdateHook(cache)
	h.now = func() time.Time { return now }

	env := newAuditEnv("sess-1", "tenant-1", sessionaudit.DecisionWarn, 7, []string{"bad"})

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	state, _, err := cache.GetOrLoad(context.Background(), "tenant-1", "sess-1")
	if err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if state == nil {
		t.Fatal("state should be created")
	}

	if state.AuditedAt != now.Unix() {
		t.Errorf("AuditedAt: got %d want %d", state.AuditedAt, now.Unix())
	}
	if state.AuditScore != 7 {
		t.Errorf("AuditScore: got %d want 7", state.AuditScore)
	}
	if state.SecurityScore == 0 {
		t.Error("SecurityScore should be > 0 (calculate from score)")
	}
	if !state.SensitiveDetected {
		t.Error("SensitiveDetected should be true (sensitive words present)")
	}
	if state.ApprovalStatus != "" {
		t.Errorf("ApprovalStatus should be empty for Warn decision, got %q", state.ApprovalStatus)
	}
}

func TestCacheUpdateHook_Execute_NeedApprovalSetsPending(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)

	env := newAuditEnv("sess-2", "tenant-2", sessionaudit.DecisionNeedApproval, 9, []string{"x"})

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "tenant-2", "sess-2")
	if !state.IsApprovalPending() {
		t.Errorf("ApprovalStatus should be pending after need_approval, got %q", state.ApprovalStatus)
	}
}

func TestCacheUpdateHook_Execute_PIIStripped(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)

	env := newAuditEnv("s", "t", sessionaudit.DecisionPass, 1, nil)
	env.Metadata["pii_stripped"] = true

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "t", "s")
	if !state.PIIStripped {
		t.Error("PIIStripped should be true")
	}
}

func TestCacheUpdateHook_Execute_OptimizationTag(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)

	env := newAuditEnv("s", "t", sessionaudit.DecisionPass, 1, nil)
	env.Metadata["optimization_applied"] = compression.OptStripTools

	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "t", "s")
	if state.OptimizationApplied != compression.OptStripTools {
		t.Errorf("OptimizationApplied: got %q want %q", state.OptimizationApplied, compression.OptStripTools)
	}
}

func TestCacheUpdateHook_Execute_NewSession(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)

	env := newAuditEnv("new-sess", "new-tenant", sessionaudit.DecisionPass, 2, nil)
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "new-tenant", "new-sess")
	if state == nil {
		t.Fatal("state should be created for new session")
	}
	if state.AuditScore != 2 {
		t.Errorf("AuditScore: got %d want 2", state.AuditScore)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// UpdateApprovalID
// ──────────────────────────────────────────────────────────────────────────────

func TestCacheUpdateHook_UpdateApprovalID(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)

	ctx := context.Background()
	if err := h.UpdateApprovalID(ctx, "t", "s", "appr-xyz"); err != nil {
		t.Fatalf("UpdateApprovalID: %v", err)
	}

	state, _, _ := cache.GetOrLoad(ctx, "t", "s")
	if state.ApprovalID != "appr-xyz" {
		t.Errorf("ApprovalID: got %q want appr-xyz", state.ApprovalID)
	}
	if state.ApprovalStatus != compression.ApprovalStatePending {
		t.Errorf("ApprovalStatus: got %q want pending", state.ApprovalStatus)
	}
}

func TestCacheUpdateHook_UpdateApprovalID_NilCache(t *testing.T) {
	h := NewCacheUpdateHook(nil)
	if err := h.UpdateApprovalID(context.Background(), "t", "s", "x"); err == nil {
		t.Error("nil cache should return error")
	}
}

func TestCacheUpdateHook_UpdateApprovalID_EmptyArgs(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)
	if err := h.UpdateApprovalID(context.Background(), "", "s", "x"); err == nil {
		t.Error("empty tenant should return error")
	}
	if err := h.UpdateApprovalID(context.Background(), "t", "", "x"); err == nil {
		t.Error("empty session should return error")
	}
}

func TestCacheUpdateHook_UpdateApprovalID_OverwriteExisting(t *testing.T) {
	cache := compression.NewSessionCache(nil, nil)
	h := NewCacheUpdateHook(cache)
	ctx := context.Background()

	// 第一次写入
	if err := h.UpdateApprovalID(ctx, "t", "s", "appr-1"); err != nil {
		t.Fatal(err)
	}
	// 第二次覆盖
	if err := h.UpdateApprovalID(ctx, "t", "s", "appr-2"); err != nil {
		t.Fatal(err)
	}

	state, _, _ := cache.GetOrLoad(ctx, "t", "s")
	if state.ApprovalID != "appr-2" {
		t.Errorf("ApprovalID should be overwritten, got %q", state.ApprovalID)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// OnError
// ──────────────────────────────────────────────────────────────────────────────

func TestCacheUpdateHook_OnError_NoOp(t *testing.T) {
	h := NewCacheUpdateHook(nil)
	// 不应修改 env.Error
	env := &domain.PipelineRequest{TenantID: "t", SessionID: "s"}
	if err := h.OnError(context.Background(), env, nil); err != nil {
		t.Errorf("OnError should be no-op, got: %v", err)
	}
}
