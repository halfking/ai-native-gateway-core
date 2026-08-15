package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/durable"
)

// SR-12 wiring unit tests: the durable handoff is glue over the tested
// store/worker底座; what needs pinning here is the frozen §9.4 202 wire
// contract, the fail-closed branches, and that the handler stays inert
// until SetDurableExecution arms it.

func TestRenderDurableAcceptedMatchesFrozenContract(t *testing.T) {
	rec := httptest.NewRecorder()
	renderDurableAccepted(rec, "sess-1", "req-9", "task-7", 5)

	if rec.Code != httpAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Location"); got != "/v1/sessions/sess-1/pending-response?request_id=req-9" {
		t.Fatalf("Location = %q", got)
	}
	if got := h.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if got := h.Get("Preference-Applied"); got != "respond-async" {
		t.Fatalf("Preference-Applied = %q", got)
	}
	if got := h.Get("X-Gw-Pending"); got != "sess-1" {
		t.Fatalf("X-Gw-Pending = %q", got)
	}
	if got := h.Get("X-Gw-Pending-Request"); got != "req-9" {
		t.Fatalf("X-Gw-Pending-Request = %q", got)
	}
	if got := h.Get("X-Gw-Task-Id"); got != "task-7" {
		t.Fatalf("X-Gw-Task-Id = %q", got)
	}
	var env struct {
		Status     string `json:"status"`
		TaskID     string `json:"task_id"`
		SessionID  string `json:"session_id"`
		RequestID  string `json:"request_id"`
		RetryAfter int    `json:"retry_after"`
		PollURL    string `json:"poll_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if env.Status != "in_progress" || env.TaskID != "task-7" || env.SessionID != "sess-1" ||
		env.RequestID != "req-9" || env.RetryAfter != 5 ||
		env.PollURL != "/v1/sessions/sess-1/pending-response?request_id=req-9" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
}

// fakeDurableHandlerStore records the CreateAndClaim/Reschedule calls.
type fakeDurableHandlerStore struct {
	created  []durableCreatedRecord
	resched  []durable.RescheduleParams
	createFn func(durableCreatedRecord) (*durable.Task, error)
}

type durableCreatedRecord struct {
	snapshot DurableRequestSnapshotV1
	task     durable.NewTask
}

func (f *fakeDurableHandlerStore) CreateAndClaim(_ context.Context, n durable.NewTask) (*durable.Task, error) {
	snap, err := UnmarshalDurableSnapshotV1(n.Snapshot)
	if err != nil {
		return nil, err
	}
	rec := durableCreatedRecord{snapshot: *snap, task: n}
	f.created = append(f.created, rec)
	if f.createFn != nil {
		return f.createFn(rec)
	}
	return &durable.Task{ID: "task-7", LeaseOwner: n.LeaseOwner, FencingToken: 1}, nil
}

func (f *fakeDurableHandlerStore) Reschedule(_ context.Context, p durable.RescheduleParams) error {
	f.resched = append(f.resched, p)
	return nil
}

func armedDurableHandler(store DurableHandlerStore) *ChatHandler {
	h := &ChatHandler{}
	h.SetDurableExecution(store, func(string) bool { return true }, DurableExecutionOptions{})
	return h
}

func durableTestInput() DurableSnapshotInput {
	return DurableSnapshotInput{
		Protocol:      "openai-completions",
		Endpoint:      "/v1/chat/completions",
		TenantID:      "tenant-1",
		ApplicationID: 7,
		APIKeyID:      42,
		SessionID:     "sess-1",
		SessionSource: "body",
		ClientModel:   "gpt-4o",
		Body:          []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		IdentityHash:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		RequestID:     "req-9",
		ClientProfile: "p1",
	}
}

func TestMaybeStartDurableHandsOffToWorkerAndRenders202(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	h := armedDurableHandler(store)

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	r.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()

	if got := h.maybeStartDurable(rec, r, durableTestInput(), false); got != durableHandled {
		t.Fatalf("decision = %v, want durableHandled", got)
	}
	if rec.Code != httpAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(store.created) != 1 {
		t.Fatalf("CreateAndClaim calls = %d, want 1", len(store.created))
	}
	snap := store.created[0].snapshot
	if snap.RequestHash == "" || snap.RequestHash != store.created[0].task.RequestHash {
		t.Fatalf("snapshot request hash not bound: %+v", snap)
	}
	if snap.Version != DurableSnapshotVersionV1 || snap.TaskCorrelationID == "" || snap.PolicyVersion == "" {
		t.Fatalf("snapshot incomplete: %+v", snap)
	}
	if len(store.resched) != 1 {
		t.Fatalf("Reschedule calls = %d, want 1 (handoff to worker)", len(store.resched))
	}
	rs := store.resched[0]
	if rs.TaskID != "task-7" || rs.LeaseOwner == "" || rs.FencingToken != 1 || rs.Reason != "handoff_to_worker" {
		t.Fatalf("handoff reschedule mismatch: %+v", rs)
	}
}

// The handler must stay inert until SetDurableExecution arms it — a nil
// store never diverts a request into the durable path, even with the
// capability header + respond-async present.
func TestMaybeStartDurableInertUntilArmed(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	r.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()
	if got := h.maybeStartDurable(rec, r, durableTestInput(), false); got != durableProceed {
		t.Fatalf("zero-value handler decision = %v, want durableProceed", got)
	}
	if rec.Code != 200 { // recorder default — nothing written
		t.Fatalf("inert handler wrote status %d", rec.Code)
	}
}

// Streaming durable requests fail closed with an explicit unsupported
// response until checkpoint binding lands.
func TestMaybeStartDurableStreamingFailsClosed(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	h := armedDurableHandler(store)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	rec := httptest.NewRecorder()
	if got := h.maybeStartDurable(rec, r, durableTestInput(), true); got != durableHandled {
		t.Fatalf("decision = %v, want durableHandled", got)
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "streaming_durable_unsupported") {
		t.Fatalf("body missing stable code: %s", rec.Body.String())
	}
	if len(store.created) != 0 {
		t.Fatal("streaming fail-closed must not create a durable task")
	}
}

// No completed handshake → the request keeps sync semantics (§9.4: no 202
// without capability + respond-async).
func TestMaybeStartDurableRequiresFullHandshake(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	h := armedDurableHandler(store)
	in := durableTestInput()

	cases := []struct {
		name   string
		caps   string
		prefer string
	}{
		{"no headers", "", ""},
		{"capability only (non-stream needs respond-async too)", CapabilityDurableRecovery, ""},
		{"prefer only (no capability)", "", "respond-async"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if tc.caps != "" {
				r.Header.Set(GatewayCapabilitiesHeader, tc.caps)
			}
			if tc.prefer != "" {
				r.Header.Set("Prefer", tc.prefer)
			}
			rec := httptest.NewRecorder()
			if got := h.maybeStartDurable(rec, r, in, false); got != durableProceed {
				t.Fatalf("decision = %v, want durableProceed", got)
			}
			if rec.Code != 200 {
				t.Fatalf("sync path must not write; got %d", rec.Code)
			}
		})
	}
	if len(store.created) != 0 {
		t.Fatalf("created = %d, want 0", len(store.created))
	}
}

// A failed create transaction must not claim durable ownership and must
// not silently downgrade an explicitly-async request to sync.
func TestMaybeStartDurableStoreErrorFailsClosed(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	store.createFn = func(durableCreatedRecord) (*durable.Task, error) {
		return nil, errors.New("db down")
	}
	h := armedDurableHandler(store)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	r.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()
	if got := h.maybeStartDurable(rec, r, durableTestInput(), false); got != durableHandled {
		t.Fatalf("decision = %v, want durableHandled", got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "durable_unavailable") {
		t.Fatalf("body missing stable code: %s", rec.Body.String())
	}
}

// Duplicate (tenant, request) is an idempotent replay: 202 pointing at the
// existing pending response, no second task.
func TestMaybeStartDurableDuplicateIsIdempotentReplay(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	store.createFn = func(durableCreatedRecord) (*durable.Task, error) {
		return nil, durable.ErrDuplicateTask
	}
	h := armedDurableHandler(store)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	r.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()
	if got := h.maybeStartDurable(rec, r, durableTestInput(), false); got != durableHandled {
		t.Fatalf("decision = %v, want durableHandled", got)
	}
	if rec.Code != httpAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if rec.Header().Get("Location") == "" {
		t.Fatal("replay must still carry the poll Location")
	}
	if len(store.resched) != 0 {
		t.Fatal("replay must not reschedule anything")
	}
}

// Tenant outside the durable allowlist keeps sync semantics.
func TestMaybeStartDurableTenantAllowlist(t *testing.T) {
	store := &fakeDurableHandlerStore{}
	h := &ChatHandler{}
	h.SetDurableExecution(store, func(tenantID string) bool { return false }, DurableExecutionOptions{})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	r.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()
	if got := h.maybeStartDurable(rec, r, durableTestInput(), false); got != durableProceed {
		t.Fatalf("decision = %v, want durableProceed", got)
	}
	if len(store.created) != 0 {
		t.Fatal("disallowed tenant must not create a durable task")
	}
}
