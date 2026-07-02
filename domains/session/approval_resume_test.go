package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// ──────────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────────

type fakeApprovalMgr struct {
	mu     sync.Mutex
	record *sessionaudit.ApprovalRecord
	getErr error
	calls  int
}

func (f *fakeApprovalMgr) GetForTenant(_ context.Context, _, _ string) (*sessionaudit.ApprovalRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.record, f.getErr
}

type fakeLLMCaller struct {
	mu       sync.Mutex
	calls    int
	lastSnap *sessionaudit.RequestSnapshot
	err      error
}

func (f *fakeLLMCaller) CallFromSnapshot(_ context.Context, snap *sessionaudit.RequestSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSnap = snap
	return f.err
}

func (f *fakeLLMCaller) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeResponder struct {
	mu               sync.Mutex
	rejectionCalls   int
	rejectionReasons []string
	err              error
}

func (f *fakeResponder) Respond(_ context.Context, _ *sessionaudit.RequestSnapshot, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeResponder) RespondRejection(_ context.Context, _ *sessionaudit.RequestSnapshot, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectionCalls++
	f.rejectionReasons = append(f.rejectionReasons, reason)
	return f.err
}

type fakePendingStore struct {
	mu    sync.Mutex
	saves []*PendingResumeEntry
	err   error
}

func (f *fakePendingStore) Save(_ context.Context, e *PendingResumeEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saves = append(f.saves, e)
	return nil
}

func (f *fakePendingStore) Saves() []*PendingResumeEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*PendingResumeEntry, len(f.saves))
	copy(out, f.saves)
	return out
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func makeRecord(status sessionaudit.ApprovalStatus, withSnap bool, reason string) *sessionaudit.ApprovalRecord {
	r := &sessionaudit.ApprovalRecord{
		ID:        "appr-1",
		SessionID: "sess-1",
		TenantID:  "tenant-1",
		RequestID: "req-1",
		Status:    status,
		Reason:    reason,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	if withSnap {
		r.Snapshot = &sessionaudit.RequestSnapshot{
			SessionID:   "sess-1",
			TenantID:    "tenant-1",
			RequestID:   "req-1",
			BodyBytes:   []byte(`{"model":"gpt-4o","messages":[]}`),
			ClientModel: "gpt-4o",
			ClientInfo: sessionaudit.ClientInfo{
				IP:        "127.0.0.1",
				UserAgent: "test",
				Model:     "gpt-4o",
			},
			CreatedAt: time.Now(),
		}
	}
	return r
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests: ResumeAfterApproval dispatch
// ──────────────────────────────────────────────────────────────────────────────

func TestResumeAfterApproval_EmptyApprovalID(t *testing.T) {
	mgr := &fakeApprovalMgr{}
	h := NewApprovalResumeHandler(nil, mgr, nil, nil, nil)
	if err := h.ResumeAfterApproval(context.Background(), "", "t"); err == nil {
		t.Error("empty approval_id should return error")
	}
}

func TestResumeAfterApproval_GetError(t *testing.T) {
	mgr := &fakeApprovalMgr{getErr: errors.New("db down")}
	h := NewApprovalResumeHandler(nil, mgr, nil, nil, nil)
	err := h.ResumeAfterApproval(context.Background(), "x", "t")
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "db down") {
		t.Errorf("error should wrap db down: %v", err)
	}
}

func TestResumeAfterApproval_PendingStatus_NoOp(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalPending, true, "")}
	llm := &fakeLLMCaller{}
	resp := &fakeResponder{}
	h := NewApprovalResumeHandler(nil, mgr, llm, resp, nil)

	err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1")
	if !errors.Is(err, ErrResumeNotPending) {
		t.Errorf("pending should return ErrResumeNotPending, got: %v", err)
	}
	if llm.Calls() != 0 {
		t.Error("LLM should not be called for pending status")
	}
	if resp.rejectionCalls != 0 {
		t.Error("Responder should not be called for pending status")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests: approved path
// ──────────────────────────────────────────────────────────────────────────────

func TestResumeApproved_CallsLLM(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	llm := &fakeLLMCaller{}
	cache := compression.NewSessionCache(nil, nil)
	h := NewApprovalResumeHandler(cache, mgr, llm, nil, nil)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if llm.Calls() != 1 {
		t.Fatalf("LLM calls: got %d want 1", llm.Calls())
	}
	if string(llm.lastSnap.BodyBytes) != `{"model":"gpt-4o","messages":[]}` {
		t.Errorf("snapshot body mismatch: %q", llm.lastSnap.BodyBytes)
	}
}

func TestResumeApproved_MarksSessionStateApproved(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	cache := compression.NewSessionCache(nil, nil)
	llm := &fakeLLMCaller{}
	h := NewApprovalResumeHandler(cache, mgr, llm, nil, nil)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "tenant-1", "sess-1")
	if state.ApprovalStatus != compression.ApprovalStateApproved {
		t.Errorf("ApprovalStatus: got %q want approved", state.ApprovalStatus)
	}
}

func TestResumeApproved_MissingSnapshot(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, false, "")}
	llm := &fakeLLMCaller{}
	h := NewApprovalResumeHandler(nil, mgr, llm, nil, nil)

	err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1")
	if !errors.Is(err, ErrResumeSnapshotMissing) {
		t.Errorf("missing snapshot should return ErrResumeSnapshotMissing, got: %v", err)
	}
	if llm.Calls() != 0 {
		t.Error("LLM should not be called when snapshot missing")
	}
}

func TestResumeApproved_LLMError_WritesFailedPending(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	llm := &fakeLLMCaller{err: errors.New("upstream timeout")}
	pending := &fakePendingStore{}
	h := NewApprovalResumeHandler(nil, mgr, llm, nil, pending)

	err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1")
	if err == nil {
		t.Fatal("LLM error should propagate")
	}

	saves := pending.Saves()
	// 至少应该有一条 failed pending
	foundFailed := false
	for _, s := range saves {
		if s.Status == "failed" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Errorf("expected at least one failed pending, got: %+v", saves)
	}
}

func TestResumeApproved_LLMError_NoCaller(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	cache := compression.NewSessionCache(nil, nil)
	h := NewApprovalResumeHandler(cache, mgr, nil, nil, nil) // llmCaller = nil

	err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1")
	if err == nil {
		t.Fatal("nil LLM caller should error")
	}
}

func TestResumeApproved_WritesInProgressPending(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	pending := &fakePendingStore{}
	llm := &fakeLLMCaller{}
	h := NewApprovalResumeHandler(nil, mgr, llm, nil, pending)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatal(err)
	}

	saves := pending.Saves()
	if len(saves) < 1 {
		t.Fatal("expected in_progress pending")
	}
	if saves[0].Status != "in_progress" {
		t.Errorf("first save status: got %q want in_progress", saves[0].Status)
	}
	// 检查 body 是 JSON 且包含 approval_id
	var body map[string]any
	if err := json.Unmarshal([]byte(saves[0].Body), &body); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	if body["approval_id"] != "appr-1" {
		t.Errorf("approval_id missing in body: %v", body)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests: rejected path
// ──────────────────────────────────────────────────────────────────────────────

func TestResumeRejected_CallsResponder(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalRejected, true, "contains PII")}
	resp := &fakeResponder{}
	cache := compression.NewSessionCache(nil, nil)
	h := NewApprovalResumeHandler(cache, mgr, nil, resp, nil)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resp.rejectionCalls != 1 {
		t.Fatalf("rejection calls: got %d want 1", resp.rejectionCalls)
	}
	if resp.rejectionReasons[0] != "contains PII" {
		t.Errorf("rejection reason: got %q want 'contains PII'", resp.rejectionReasons[0])
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "tenant-1", "sess-1")
	if state.ApprovalStatus != compression.ApprovalStateRejected {
		t.Errorf("ApprovalStatus: got %q want rejected", state.ApprovalStatus)
	}
}

func TestResumeRejected_DefaultReason(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalRejected, true, "")}
	resp := &fakeResponder{}
	h := NewApprovalResumeHandler(nil, mgr, nil, resp, nil)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if resp.rejectionReasons[0] != "Request rejected by approval" {
		t.Errorf("default reason: got %q", resp.rejectionReasons[0])
	}
}

func TestResumeRejected_WritesPending(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalRejected, true, "no good")}
	pending := &fakePendingStore{}
	resp := &fakeResponder{}
	h := NewApprovalResumeHandler(nil, mgr, nil, resp, pending)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatal(err)
	}

	saves := pending.Saves()
	if len(saves) == 0 {
		t.Fatal("expected pending entry for rejection")
	}
	if saves[0].Status != "completed" {
		t.Errorf("rejection pending status: got %q want completed", saves[0].Status)
	}
	if saves[0].ErrorMessage != "no good" {
		t.Errorf("ErrorMessage: got %q want 'no good'", saves[0].ErrorMessage)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests: timeout path
// ──────────────────────────────────────────────────────────────────────────────

func TestResumeTimeout(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalTimeout, true, "")}
	resp := &fakeResponder{}
	cache := compression.NewSessionCache(nil, nil)
	h := NewApprovalResumeHandler(cache, mgr, nil, resp, nil)

	if err := h.ResumeAfterApproval(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resp.rejectionCalls != 1 {
		t.Errorf("timeout should call responder, got %d calls", resp.rejectionCalls)
	}

	state, _, _ := cache.GetOrLoad(context.Background(), "tenant-1", "sess-1")
	if state.ApprovalStatus != compression.ApprovalStateTimeout {
		t.Errorf("ApprovalStatus: got %q want timeout", state.ApprovalStatus)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests: helper functions
// ──────────────────────────────────────────────────────────────────────────────

func TestResumeApproved_ConvenienceMethod(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalApproved, true, "")}
	llm := &fakeLLMCaller{}
	h := NewApprovalResumeHandler(nil, mgr, llm, nil, nil)

	if err := h.ResumeApproved(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if llm.Calls() != 1 {
		t.Errorf("ResumeApproved should call LLM, got %d", llm.Calls())
	}
}

func TestResumeRejected_ConvenienceMethod(t *testing.T) {
	mgr := &fakeApprovalMgr{record: makeRecord(sessionaudit.ApprovalRejected, true, "no")}
	resp := &fakeResponder{}
	h := NewApprovalResumeHandler(nil, mgr, nil, resp, nil)

	if err := h.ResumeRejected(context.Background(), "appr-1", "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if resp.rejectionCalls != 1 {
		t.Errorf("ResumeRejected should call responder, got %d", resp.rejectionCalls)
	}
}

func TestBuildSnapshotFromApproval(t *testing.T) {
	r := makeRecord(sessionaudit.ApprovalApproved, true, "")
	snap := BuildSnapshotFromApproval(r)
	if snap == nil {
		t.Fatal("snapshot should be returned")
	}
	if snap.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q", snap.SessionID)
	}

	if BuildSnapshotFromApproval(nil) != nil {
		t.Error("nil record should return nil")
	}
}

func TestNewRejectionResponse(t *testing.T) {
	r := NewRejectionResponse("appr-1", "test reason")
	if r.ApprovalID != "appr-1" {
		t.Errorf("ApprovalID: got %q", r.ApprovalID)
	}
	if r.Reason != "test reason" {
		t.Errorf("Reason: got %q", r.Reason)
	}
	if r.StatusCode != 403 {
		t.Errorf("StatusCode: got %d want 403", r.StatusCode)
	}
	b, _ := json.Marshal(r)
	if !contains(string(b), `"approval_id":"appr-1"`) {
		t.Errorf("JSON should contain approval_id, got: %s", b)
	}
}

func TestSetNow_NilKeepsDefault(t *testing.T) {
	h := NewApprovalResumeHandler(nil, &fakeApprovalMgr{}, nil, nil, nil)
	h.SetNow(nil) // 不应 panic，now 保持 time.Now
	if h.now == nil {
		t.Error("now should not be nil after SetNow(nil)")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────────────

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
