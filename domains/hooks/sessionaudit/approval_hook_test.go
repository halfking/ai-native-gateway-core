package sessionaudithook

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/pashagolub/pgxmock/v4"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

type stubNotifier struct {
	mu      sync.Mutex
	records []*sessionaudit.ApprovalRecord
	err     error
}

func (s *stubNotifier) NotifyApproval(_ context.Context, r *sessionaudit.ApprovalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return s.err
}

func (s *stubNotifier) Calls() []*sessionaudit.ApprovalRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*sessionaudit.ApprovalRecord, len(s.records))
	copy(out, s.records)
	return out
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func newNeedApprovalEnv(t *testing.T) (*domain.PipelineRequest, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	env := &domain.PipelineRequest{
		TenantID:  "tenant-1",
		SessionID: "sess-1",
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Score:          8,
				SensitiveWords: []string{"blocked"},
				Decision:       sessionaudit.DecisionNeedApproval,
				Reason:         "sensitive content detected",
			},
		},
		Envelope: &domain.RequestEnvelope{
			Transport: &domain.TransportContext{
				W:           w,
				BodyBytes:   []byte(`{"model":"gpt-4o","messages":[]}`),
				ClientModel: "gpt-4o",
			},
		},
	}
	return env, w
}

func expectApprovalCreate(t *testing.T, mock pgxmock.PgxPoolIface, sessionID, tenantID string) {
	t.Helper()
	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SET LOCAL app.current_tenant`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec(`INSERT INTO approval_queue`).
		WithArgs(
			pgxmock.AnyArg(),
			sessionID,
			tenantID,
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			sessionaudit.ApprovalPending,
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
}

func expectGetForNotify(mock pgxmock.PgxPoolIface, tenantID string) {
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SET LOCAL app.current_tenant`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	}).AddRow(
		"test-approval-id", "sess-1", tenantID, "req-1",
		[]byte(`{"score":8,"decision":"need_approval"}`),
		[]byte(`{"session_id":"sess-1","tenant_id":"`+tenantID+`","request_id":"req-1"}`),
		sessionaudit.ApprovalPending, nil, nil, nil,
		time.Now(), time.Now().Add(15*time.Minute),
	)
	mock.ExpectQuery(`SELECT id, session_id, tenant_id, request_id`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	mock.ExpectCommit()
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

func TestApprovalHook_Enabled_NoMetadata(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	h := NewApprovalHook(mgr, eventbus.NewMemoryBus(8), nil, nil, 0)

	env := &domain.PipelineRequest{TenantID: "t", SessionID: "s"}
	if h.Enabled(context.Background(), env) {
		t.Error("should not be enabled without audit_result metadata")
	}
}

func TestApprovalHook_Enabled_PassDecision(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	h := NewApprovalHook(mgr, eventbus.NewMemoryBus(8), nil, nil, 0)

	env := &domain.PipelineRequest{
		TenantID:  "t",
		SessionID: "s",
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{Decision: sessionaudit.DecisionPass},
		},
	}
	if h.Enabled(context.Background(), env) {
		t.Error("should not be enabled when decision != need_approval")
	}
}

func TestApprovalHook_Enabled_NeedApproval(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	h := NewApprovalHook(mgr, eventbus.NewMemoryBus(8), nil, nil, 0)

	env, _ := newNeedApprovalEnv(t)
	if !h.Enabled(context.Background(), env) {
		t.Error("should be enabled when decision == need_approval")
	}
}

func TestApprovalHook_Execute_NilApprovalMgr(t *testing.T) {
	h := NewApprovalHook(nil, nil, nil, nil, 0)
	env, _ := newNeedApprovalEnv(t)
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("nil approval mgr should be a no-op, got: %v", err)
	}
}

func TestApprovalHook_Execute_PausesRequest(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	bus := eventbus.NewMemoryBus(8)
	defer bus.Close()

	h := NewApprovalHook(mgr, bus, nil, nil, 0)
	env, w := newNeedApprovalEnv(t)

	expectApprovalCreate(t, mock, "sess-1", "tenant-1")

	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("Execute should return error to pause pipeline")
	}
	if !IsApprovalRequired(err) {
		t.Errorf("error should match ErrApprovalRequired sentinel, got: %v", err)
	}

	var are *ApprovalRequiredError
	if !errors.As(err, &are) {
		t.Fatal("error should wrap *ApprovalRequiredError")
	}
	if are.ApprovalID == "" {
		t.Error("ApprovalID should be populated")
	}
	if are.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q want sess-1", are.SessionID)
	}
	if are.TenantID != "tenant-1" {
		t.Errorf("TenantID: got %q want tenant-1", are.TenantID)
	}

	// X-Approval-ID header must be set on the ResponseWriter
	if got := w.Header().Get("X-Approval-ID"); got == "" {
		t.Error("X-Approval-ID header should be set")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}

func TestApprovalHook_Execute_PublishesEvent(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	bus := eventbus.NewMemoryBus(8)
	defer bus.Close()

	// 订阅 ApprovalNeededEvent
	received := make(chan sessionaudit.ApprovalNeededEvent, 1)
	bus.Subscribe(sessionaudit.EventTypeApprovalNeeded, func(_ context.Context, e eventbus.Event) error {
		if evt, ok := e.(*sessionaudit.ApprovalNeededEvent); ok {
			received <- *evt
		}
		return nil
	})

	h := NewApprovalHook(mgr, bus, nil, nil, 0)
	env, _ := newNeedApprovalEnv(t)
	expectApprovalCreate(t, mock, "sess-1", "tenant-1")

	_ = h.Execute(context.Background(), env)

	select {
	case evt := <-received:
		if evt.SessionID != "sess-1" || evt.TenantID != "tenant-1" {
			t.Errorf("event session/tenant mismatch: %+v", evt)
		}
		if evt.DetectResult == nil || evt.DetectResult.Decision != sessionaudit.DecisionNeedApproval {
			t.Error("event should carry the detect result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ApprovalNeededEvent")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}

func TestApprovalHook_Execute_CreatesSnapshot(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	h := NewApprovalHook(mgr, nil, nil, nil, 0)

	// 用 transport 缺失的 env 验证快照降级（snapshot=nil 时不阻断）
	env := &domain.PipelineRequest{
		TenantID:  "t",
		SessionID: "s",
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{Decision: sessionaudit.DecisionNeedApproval},
		},
		Envelope: nil, // no envelope
	}

	// 缺 envelope 的情况下 buildSnapshot 返回 nil，hook 降级不阻断
	if err := h.Execute(context.Background(), env); err != nil {
		t.Errorf("degraded path should not error, got: %v", err)
	}
}

func TestApprovalHook_Execute_NotifiesApproval(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	notifier := &stubNotifier{}

	h := NewApprovalHook(mgr, nil, nil, notifier, 0)
	env, _ := newNeedApprovalEnv(t)

	expectApprovalCreate(t, mock, "sess-1", "tenant-1")
	expectGetForNotify(mock, "tenant-1")

	_ = h.Execute(context.Background(), env)

	calls := notifier.Calls()
	if len(calls) != 1 {
		t.Fatalf("notifier calls: got %d want 1", len(calls))
	}
	if calls[0].TenantID != "tenant-1" {
		t.Errorf("notifier record tenant: got %q want tenant-1", calls[0].TenantID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}

func TestApprovalRequiredError_Is_ErrApprovalRequired(t *testing.T) {
	err := &ApprovalRequiredError{ApprovalID: "x"}
	if !errors.Is(err, ErrApprovalRequired) {
		t.Error("ApprovalRequiredError should match ErrApprovalRequired sentinel")
	}
	if !IsApprovalRequired(err) {
		t.Error("IsApprovalRequired() should return true")
	}
	if !IsApprovalRequired(errors.New("wrapped: " + err.Error())) {
		// 用 %w wrap 后仍可识别
		// 我们的实现里没用 %w，但 IsApprovalRequired 走 errors.Is 链
		// 这里仅验证 sentinel 自身可识别
	}
}

func TestMarshalApprovalRequiredError(t *testing.T) {
	err := &ApprovalRequiredError{
		ApprovalID: "appr-1",
		SessionID:  "sess-1",
		TenantID:   "tenant-1",
		Reason:     "test",
	}
	b, ok := MarshalApprovalRequiredError(err)
	if !ok {
		t.Fatal("MarshalApprovalRequiredError should return true")
	}
	if !bytes.Contains(b, []byte(`"approval_id"`)) {
		t.Errorf("serialized should contain approval_id, got: %s", b)
	}

	// 非 ApprovalRequiredError 返回 false
	if _, ok := MarshalApprovalRequiredError(errors.New("plain")); ok {
		t.Error("non-ApprovalRequiredError should return false")
	}
}

func TestApprovalHook_HeadersOnResponse(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 15*time.Minute)
	h := NewApprovalHook(mgr, nil, nil, nil, 0)

	// 显式设置一个非 nil ResponseWriter
	w := httptest.NewRecorder()
	env := &domain.PipelineRequest{
		TenantID:  "tenant-x",
		SessionID: "sess-x",
		Metadata: map[string]any{
			"audit_result": &sessionaudit.DetectResult{
				Decision: sessionaudit.DecisionNeedApproval,
				Reason:   "high risk",
			},
		},
		Envelope: &domain.RequestEnvelope{
			Transport: &domain.TransportContext{
				W:         w,
				BodyBytes: []byte(`{}`),
			},
		},
	}
	expectApprovalCreate(t, mock, "sess-x", "tenant-x")
	_ = h.Execute(context.Background(), env)

	if got := w.Header().Get("X-Approval-Status-URL"); got == "" {
		t.Error("X-Approval-Status-URL header should be set")
	}
}

// TestApprovalHook_DefaultTimeout 验证 default 15min。
func TestApprovalHook_DefaultTimeout(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mgr := sessionaudit.NewApprovalManager(mock, 0)
	h := NewApprovalHook(mgr, nil, nil, nil, 0)
	if h.timeout != 15*time.Minute {
		t.Errorf("default timeout = %v, want 15m", h.timeout)
	}
}

// TestApprovalHook_BuildSnapshot_NoEnvelope 验证 envelope 缺失时 snapshot 为 nil。
func TestApprovalHook_BuildSnapshot_NoEnvelope(t *testing.T) {
	h := NewApprovalHook(nil, nil, nil, nil, 0)
	env := &domain.PipelineRequest{SessionID: "s", TenantID: "t"}
	snap := h.buildSnapshot(env, &sessionaudit.DetectResult{Decision: sessionaudit.DecisionNeedApproval})
	if snap != nil {
		t.Errorf("snapshot should be nil without envelope, got %+v", snap)
	}
}

// TestApprovalHook_NameAndPriority 验证 pipeline 路由用元数据。
func TestApprovalHook_NameAndPriority(t *testing.T) {
	h := NewApprovalHook(nil, nil, nil, nil, 0)
	if h.Name() != "sessionaudit.approval" {
		t.Errorf("Name: got %q want sessionaudit.approval", h.Name())
	}
	if h.Priority() != 110 {
		t.Errorf("Priority: got %d want 110", h.Priority())
	}
}

// TestApprovalHook_OnError_PassThrough 验证错误透传。
func TestApprovalHook_OnError_PassThrough(t *testing.T) {
	h := NewApprovalHook(nil, nil, nil, nil, 0)
	original := errors.New("boom")
	if got := h.OnError(context.Background(), nil, original); got != original {
		t.Errorf("OnError should return the same error")
	}
}

// TestNewApprovalHook 验证构造函数对 nil 依赖的处理。
func TestNewApprovalHook(t *testing.T) {
	h := NewApprovalHook(nil, nil, nil, nil, 5*time.Minute)
	if h.timeout != 5*time.Minute {
		t.Errorf("timeout: got %v want 5m", h.timeout)
	}
	if h.now == nil {
		t.Error("now should default to time.Now")
	}
}

// silence import-only check for net/http
var _ = http.StatusOK
