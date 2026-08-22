package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// stubHandoffStore satisfies handoff.HandoffStore (the RecordHandoff /
// GetSession* surface) so NewTriggerHook accepts a composite db that also
// implements ConfirmationStore + GoalRestoreStore via embedding
// *handoff.MemoryConfirmationStore.
type stubHandoffStore struct{}

func (s *stubHandoffStore) RecordHandoff(_ context.Context, _ *handoff.HandoffRecord) error {
	return nil
}
func (s *stubHandoffStore) GetSessionTokens(_ context.Context, _ string) (int, error) { return 0, nil }
func (s *stubHandoffStore) GetSessionMessages(_ context.Context, _ string) (int, error) {
	return 0, nil
}
func (s *stubHandoffStore) GetSessionLastActivity(_ context.Context, _ string) (time.Time, error) {
	return time.Time{}, nil
}
func (s *stubHandoffStore) GetHandoffCount(_ context.Context, _ string) (int, error) { return 0, nil }
func (s *stubHandoffStore) GetLastHandoffAt(_ context.Context, _ string) (time.Time, error) {
	return time.Time{}, nil
}
func (s *stubHandoffStore) IsHandoffCooldownActive(_ context.Context, _ string, _ int) (bool, error) {
	return false, nil
}

// stubGoalStateSerializer is a no-snapshot serializer: Serialize returns nil so
// no goal handoff is produced, and Restore is a no-op. It lets the confirmation
// path exercise restoreGoalState and land on the "no_snapshot" branch.
type stubGoalStateSerializer struct{}

func (stubGoalStateSerializer) Serialize(_ context.Context, _ handoff.GoalStateInput) (*handoff.GoalState, error) {
	return nil, nil
}
func (stubGoalStateSerializer) Restore(_ context.Context, _ string, _ *handoff.GoalState) error {
	return nil
}

// handoffTestDB combines an in-memory HandoffStore with the
// MemoryConfirmationStore so a single value satisfies both the HandoffStore
// and ConfirmationStore/GoalRestoreStore interfaces that TriggerHook expects.
type handoffTestDB struct {
	*stubHandoffStore
	*handoff.MemoryConfirmationStore
}

// stubKeyVerifier satisfies requestKeyVerifier for the confirmation handler.
type stubKeyVerifier struct {
	info *authentication.KeyInfo
}

func (v *stubKeyVerifier) Enabled() bool { return v.info != nil }
func (v *stubKeyVerifier) Verify(_ context.Context, _ string) (*authentication.KeyInfo, error) {
	return v.info, nil
}
func (v *stubKeyVerifier) VerifyByID(_ context.Context, _ int) (*authentication.KeyInfo, error) {
	return v.info, nil
}
func (v *stubKeyVerifier) CheckBudget(_ context.Context, _ int) error { return nil }
func (v *stubKeyVerifier) LookupKeyMeta(_ context.Context, _ string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// buildConfirmationHarness wires a ChatHandler with a real handoff.TriggerHook
// backed by an in-memory confirmation store, a stub key verifier and a stub
// session getter. It returns the handler, the underlying store (so tests can
// seed proposals) and the confirmation token.
func buildConfirmationHarness(t *testing.T) (*ChatHandler, *handoff.MemoryConfirmationStore, string, string) {
	t.Helper()
	handoff.ResetMetrics()
	db := &handoffTestDB{
		stubHandoffStore:        &stubHandoffStore{},
		MemoryConfirmationStore: handoff.NewMemoryConfirmationStore(),
	}
	trigger := handoff.NewMemoryHandoffTrigger(5 * time.Minute)
	hook := handoff.NewTriggerHook(handoff.TriggerConfig{
		Enabled: true, TriggerMode: handoff.TriggerModeAuto, MaxPerSession: 5,
		GoalTrigger:          trigger,
		GoalStateSerializer: stubGoalStateSerializer{},
	}, db)
	keyInfo := &authentication.KeyInfo{ID: 42, TenantID: "tenant-a"}
	// The target session is created by the client AFTER receiving the 202
	// proposal, so its CreatedAt must be at-or-after the proposal's
	// CreatedAt. Use a fixed proposal time so the session can be 5s after it.
	proposalTime := time.Now().UTC().Truncate(time.Second)
	targetSession := &session.Session{
		SessionID: "gw_new", TenantID: "tenant-a", APIKeyID: 42,
		CreatedAt: proposalTime.Add(5 * time.Second),
	}
	h := &ChatHandler{
		handoffHook: hook,
		keyVerifier: &stubKeyVerifier{info: keyInfo},
		handoffSessionGetter: &stubSessionGetter{
			got: map[string]*session.Session{"gw_new": targetSession},
		},
	}
	// Seed a pending proposal directly into the memory store.
	record := &handoff.HandoffRecord{
		SessionKey: "gw_old", TenantID: "tenant-a",
		TriggerMode: "auto", TriggerReason: "context_pressure",
		CreatedAt: proposalTime,
	}
	proposal, token, err := handoff.NewConfirmationProposal(record, 42, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("NewConfirmationProposal: %v", err)
	}
	if err := db.SavePending(context.Background(), proposal); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	return h, db.MemoryConfirmationStore, proposal.ID, token
}

func TestHandleHandoffConfirmation_HappyPath(t *testing.T) {
	h, _, proposalID, token := buildConfirmationHarness(t)

	body := handoffConfirmationRequest{
		HandoffID: proposalID, ConfirmationToken: token, NewSessionID: "gw_new",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader(string(raw)))
	req.Header.Set("Idempotency-Key", "idempotency-key-1234567890")
	req.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "handoff_confirmed" {
		t.Errorf("status field = %v, want handoff_confirmed", resp["status"])
	}
	if resp["handoff_id"] != proposalID {
		t.Errorf("handoff_id = %v, want %s", resp["handoff_id"], proposalID)
	}
	if got := handoff.ConfirmationCount("first"); got != 1 {
		t.Errorf("handoff_confirmation_total{first} = %v, want 1", got)
	}
	if got := handoff.RestoreCount("no_snapshot"); got != 1 {
		t.Errorf("handoff_restore_total{no_snapshot} = %v, want 1", got)
	}
}

func TestHandleHandoffConfirmation_IdempotentReplay(t *testing.T) {
	h, _, proposalID, token := buildConfirmationHarness(t)

	body := handoffConfirmationRequest{
		HandoffID: proposalID, ConfirmationToken: token, NewSessionID: "gw_new",
	}
	raw, _ := json.Marshal(body)

	// First confirmation.
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader(string(raw)))
	req.Header.Set("Idempotency-Key", "idempotency-key-1234567890")
	req.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first confirm status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Replay with same idempotency key.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader(string(raw)))
	req2.Header.Set("Idempotency-Key", "idempotency-key-1234567890")
	req2.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec2 := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if got := handoff.ConfirmationCount("first"); got != 1 {
		t.Errorf("handoff_confirmation_total{first} = %v, want 1", got)
	}
	if got := handoff.ConfirmationCount("idempotent"); got != 1 {
		t.Errorf("handoff_confirmation_total{idempotent} = %v, want 1", got)
	}
}

func TestHandleHandoffConfirmation_MissingIdempotencyKey(t *testing.T) {
	h, _, proposalID, token := buildConfirmationHarness(t)

	body := handoffConfirmationRequest{
		HandoffID: proposalID, ConfirmationToken: token, NewSessionID: "gw_new",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleHandoffConfirmation_WrongTenant(t *testing.T) {
	h, _, proposalID, token := buildConfirmationHarness(t)
	// Override the session getter to return a session owned by a different tenant.
	proposalTime := time.Now().UTC().Truncate(time.Second)
	h.handoffSessionGetter = &stubSessionGetter{
		got: map[string]*session.Session{
			"gw_new": {
				SessionID: "gw_new", TenantID: "tenant-b", APIKeyID: 42,
				CreatedAt: proposalTime.Add(5 * time.Second),
			},
		},
	}

	body := handoffConfirmationRequest{
		HandoffID: proposalID, ConfirmationToken: token, NewSessionID: "gw_new",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader(string(raw)))
	req.Header.Set("Idempotency-Key", "idempotency-key-1234567890")
	req.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleHandoffConfirmation_MethodNotAllowed(t *testing.T) {
	h, _, _, _ := buildConfirmationHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/handoffs/confirm", nil)
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleHandoffConfirmation_InvalidBody(t *testing.T) {
	h, _, _, _ := buildConfirmationHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", strings.NewReader("not-json"))
	req.Header.Set("Idempotency-Key", "idempotency-key-1234567890")
	req.Header.Set("Authorization", "Bearer sk-test-1234567890abcdef")
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleHandoffConfirmation_HookUnavailable(t *testing.T) {
	h := &ChatHandler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/handoffs/confirm", nil)
	rec := httptest.NewRecorder()
	h.HandleHandoffConfirmation(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
