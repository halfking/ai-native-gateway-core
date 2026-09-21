package bg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestRunOne_NoRoutableModels_InsertsFailedPlaceholder is the regression
// guard for the 2026-07-22 deadlock fix.  Before the fix, runOne returned
// fmt.Errorf("credential %d has no routable models") without inserting a
// self_check_runs row, which meant pickDueCredential kept re-picking the
// same broken credential every 5-min tick forever.
//
// After the fix, runOne must:
//  1. call insertRun to create a self_check_runs row
//  2. call finalizeRun with status='failed' and a non-empty error_detail
//     so completed_at advances (unblocking the next due-credential pick)
//  3. still return a non-nil error so cycleOnce logs it
func TestRunOne_NoRoutableModels_InsertsFailedPlaceholder(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 2 // the credential that triggered the bug on prod 154

	// pickModels returns empty: active credential, no available bindings,
	// no due failed bindings, and no policy featured models.
	mock.ExpectQuery("SELECT c.tenant_id").WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id"}).AddRow("default"))
	mock.ExpectQuery("COALESCE\\(cmb.available, FALSE\\) = \\$2").WithArgs(credID, true).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name", "standardized_name"}))
	mock.ExpectQuery("COALESCE\\(cmb.available, FALSE\\) = \\$2").WithArgs(credID, false).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name", "standardized_name"}))
	mock.ExpectQuery("FROM routing_policy").WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{"featured_models"}).AddRow([]string{}))
	mock.ExpectQuery("FROM request_logs_hot rl").WithArgs(credID, "default").
		WillReturnRows(pgxmock.NewRows([]string{"model", "count"}))

	// insertRun: INSERT INTO self_check_runs (...) RETURNING id
	// Args: ($1=cred-N, $2=startedAt time.Time).
	mock.ExpectQuery("INSERT INTO self_check_runs").
		WithArgs(fmt.Sprintf("cred-%d", credID), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(42)))

	// finalizeRun: UPDATE self_check_runs SET ... WHERE id=$1
	mock.ExpectExec("UPDATE self_check_runs SET[\\s\\S]*attempted_models = \\$13::text::jsonb").
		WithArgs(
			int64(42),           // run id
			pgxmock.AnyArg(),    // completed_at
			pgxmock.AnyArg(),    // duration_ms
			"failed",            // status
			0,                   // rounds_total
			0,                   // rounds_success
			false,               // had_tool_call
			0,                   // total_tokens
			0,                   // avg_latency_ms
			"none",              // error_type — must satisfy 338 CHECK on prod
			pgxmock.AnyArg(),    // error_detail
			"no_eligible_model", // selection_strategy — no ranked or due failed model
			pgxmock.AnyArg(),    // attempted_models jsonb
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := w.runOne(context.Background(), credID)
	if err == nil {
		t.Fatalf("runOne: expected error (credential %d has no routable models) but got nil", credID)
	}
	// Cycle must still propagate the error so cycleOnce logs it.
	if err.Error() != fmt.Sprintf("credential %d has no routable models", credID) {
		t.Errorf("runOne error: got %q want %q", err.Error(),
			fmt.Sprintf("credential %d has no routable models", credID))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestAuditToSystemProbeRunsUsesCompatibleSchema guards the migration audit
// contract: task_id is required and task_type must satisfy migration 344.
func TestAuditToSystemProbeRunsUsesCompatibleSchema(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	startedAt := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("INSERT INTO system_probe_runs").WithArgs(
		int64(42),
		"chat_tool",
		"automatic",
		7,
		"gpt-test",
		"legacy_selfcheck",
		"credential-selfcheck-worker",
		"success",
		startedAt,
		pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := w.auditToSystemProbeRuns(context.Background(), 42, 7, []string{"gpt-test"}, startedAt, "success"); err != nil {
		t.Fatalf("auditToSystemProbeRuns: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRunOne_PickModelsError_DoesNotInsertPlaceholder verifies the OTHER
// failure path of runOne: when pickModels itself errors (e.g. SQL fails),
// we must NOT insert a placeholder row, because the failed run would be
// attributed to a credential we couldn't even analyze.
func TestRunOne_PickModelsError_DoesNotInsertPlaceholder(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 3
	const dbErr = "simulated pg boom"

	mock.ExpectQuery("SELECT c.tenant_id").
		WithArgs(credID).
		WillReturnError(errors.New(dbErr))

	err := w.runOne(context.Background(), credID)
	if err == nil {
		t.Fatal("runOne: expected error from pickModels but got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickDueCredential_PerCredentialLastAt is the regression guard for
// the second half of the 2026-07-22 fix: the LATERAL subquery must group
// last_at by credential_id (via model_name = 'cred-<id>'), NOT by
// tenant_id.  Pre-fix, all credentials sharing tenant_id='default' had
// the same last_at, so the worker kept picking c.id=2 forever.
func TestPickDueCredential_PerCredentialLastAt(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	// The query now references model_name='cred-' || c.id::text.  We don't
	// care which credential is returned (pgxmock matches on SQL substring),
	// only that the query doesn't reference tenant_id anymore — that's the
	// regression we are guarding.  Args: ($1 = "%d seconds" interval).
	mock.ExpectQuery("model_name = 'cred-'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(11))

	got, ok, err := w.pickDueCredential(context.Background())
	if err != nil {
		t.Fatalf("pickDueCredential: %v", err)
	}
	if !ok {
		t.Fatal("pickDueCredential: expected ok=true")
	}
	if got != 11 {
		t.Errorf("credential id: got %d want 11", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickDueCredential_NoDueCredentials returns (0, false, nil) when no
// credential has an aged-out last_at.  This is the path that keeps the
// worker quiet between cycles once all credentials are healthy and
// recently probed.
func TestPickDueCredential_NoDueCredentials(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	mock.ExpectQuery("model_name = 'cred-'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(scErrNoRows)

	id, ok, err := w.pickDueCredential(context.Background())
	if err != nil {
		t.Fatalf("pickDueCredential: %v", err)
	}
	if ok {
		t.Errorf("pickDueCredential: expected ok=false (no due creds), got id=%d", id)
	}
	if id != 0 {
		t.Errorf("pickDueCredential: expected id=0, got %d", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ── P1 fix verification (2026-08-18, Agent C): self-check pin attribution ──
//
// Regression: prior to the fix the credential self-check worker issued a
// /chat/completions request to the local gateway WITHOUT a pin header,
// so the router was free to pick any available credential. The verdict
// (success/failure/token count) was then attributed to that other
// credential, silently making the self-check false-positive. The fix
// stamps X-LLM-Pin-Credential on every outbound probe so the routing
// layer's filtered plan (executor_dispatch.go:107) reduces the
// candidate list to exactly the credential under check.
//
// These tests guard the contract:
//  1. doHTTP with credentialID <= 0 returns "unattributed" without
//     issuing an HTTP request (counters: a "no pin" path that still
//     shipped the round).
//  2. doHTTP with credentialID > 0 stamps X-LLM-Pin-Credential and
//     reports the credential as the round's attribution target (the
//     gateway filter will then have exactly one candidate).
//  3. The full doRequest path (ping + tool call) carries the pin on
//     BOTH rounds when the upstream echoes the request headers —
//     mirroring the active_probe_executor.RunGateway pattern.

// capturedProbeRequest captures the inbound /chat/completions
// request the worker issued so the test can assert both the trusted
// pin header AND the X-LLM-Origin-* identity.
type capturedProbeRequest struct {
	PinHeader           string
	AuthorizationHeader string
	OriginStage         string
	OriginActor         string
	Body                string
}

func newPinCapturingWorker(t *testing.T, baseURL string) (*CredentialSelfcheckWorker, *capturedProbeRequest, *httptest.Server) {
	t.Helper()
	captured := &capturedProbeRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.PinHeader = r.Header.Get("X-LLM-Pin-Credential")
		captured.AuthorizationHeader = r.Header.Get("Authorization")
		captured.OriginStage = r.Header.Get("X-LLM-Origin-Stage")
		captured.OriginActor = r.Header.Get("X-LLM-Origin-Actor")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		captured.Body = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Minimal valid response with one choice and a tool call so the
		// tool-call follow-up in doRequest can also fire.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"t1"}]}}],"usage":{"total_tokens":7}}`))
	}))
	w := &CredentialSelfcheckWorker{
		apiKey:  "test-not-pinned",
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
	return w, captured, srv
}

// TestDoHTTP_RejectsZeroCredentialID_Unattributed proves the worker
// refuses to attribute a round to a random available credential when
// no pin credential id is provided. The round must be marked
// err_type="unattributed" and the worker must NOT issue an HTTP
// request (the gateway round would otherwise silently pick a sibling).
func TestDoHTTP_RejectsZeroCredentialID_Unattributed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("doHTTP must not issue a request when credentialID is 0; got %v", r)
	}))
	defer srv.Close()

	w := &CredentialSelfcheckWorker{
		apiKey:  "test-not-pinned",
		baseURL: srv.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
	r := w.doHTTP(context.Background(), 0, "gpt-4o", `{"model":"gpt-4o"}`, false)
	if r.Success {
		t.Fatalf("unattributed round must not be marked success; got %+v", r)
	}
	if r.ErrType != "unattributed" {
		t.Errorf("err_type=%q, want %q", r.ErrType, "unattributed")
	}
	if !scContainsSubstr(r.ErrDetail, "missing credential_id") {
		t.Errorf("err_detail=%q, want substring %q", r.ErrDetail, "missing credential_id")
	}
}

// TestDoHTTP_StampsPinHeaderForTargetCredential is the happy-path
// guard: when the worker is asked to check credential 42, the
// outbound /chat/completions request MUST carry X-LLM-Pin-Credential
// 42 so the router filter narrows the candidate list to exactly that
// node. Without the pin the verdict can lock out the wrong node.
func TestDoHTTP_StampsPinHeaderForTargetCredential(t *testing.T) {
	w, captured, srv := newPinCapturingWorker(t, "")
	defer srv.Close()
	w.baseURL = srv.URL

	r := w.doHTTP(context.Background(), 42, "gpt-4o", `{"model":"gpt-4o"}`, false)
	if !r.Success {
		t.Fatalf("round should succeed against the fake gateway; got %+v", r)
	}
	if captured.PinHeader != "42" {
		t.Errorf("X-LLM-Pin-Credential=%q, want %q (router must see the credential under test)",
			captured.PinHeader, "42")
	}
	if captured.AuthorizationHeader != "Bearer test-not-pinned" {
		t.Errorf("Authorization=%q, want the system key path so the pin survives OriginMiddleware",
			captured.AuthorizationHeader)
	}
	if captured.OriginStage != "self_check" {
		t.Errorf("X-LLM-Origin-Stage=%q, want %q", captured.OriginStage, "self_check")
	}
	if captured.OriginActor != "credential-selfcheck-worker" {
		t.Errorf("X-LLM-Origin-Actor=%q, want %q", captured.OriginActor, "credential-selfcheck-worker")
	}
}

// TestDoRequest_BothRoundsCarryPin verifies that the two-round
// ping + tool-call path stamps the pin on the tool-call follow-up
// too. The router filter is per-request, so dropping the pin on the
// second round would let the gateway swap to a different node
// between rounds and split the verdict across credentials.
func TestDoRequest_BothRoundsCarryPin(t *testing.T) {
	w, captured, srv := newPinCapturingWorker(t, "")
	defer srv.Close()
	w.baseURL = srv.URL

	r := w.doRequest(context.Background(), 99, "gpt-4o")
	if !r.Success {
		t.Fatalf("doRequest should succeed against the fake gateway; got %+v", r)
	}
	if captured.PinHeader != "99" {
		t.Errorf("last captured pin=%q, want %q (follow-up round must also carry the pin)",
			captured.PinHeader, "99")
	}
}

// TestDoRequest_NoPinNoAttrib is the negative guard: a runOne caller
// that has no credentialID (e.g. a future refactor that picks a
// model before resolving the credential) must not be allowed to mark
// the round as success just because the gateway happened to pick a
// healthy sibling node. The round should be reported as
// "unattributed" so the self_check_runs row is honest.
func TestDoRequest_NoPinNoAttrib(t *testing.T) {
	r := (&CredentialSelfcheckWorker{client: &http.Client{Timeout: time.Second}}).
		doRequest(context.Background(), 0, "gpt-4o")
	if r.Success {
		t.Fatalf("doRequest with no credential must not report success; got %+v", r)
	}
	if r.ErrType != "unattributed" {
		t.Errorf("err_type=%q, want %q", r.ErrType, "unattributed")
	}
}

// contains is a tiny helper to avoid pulling strings into the test
// file just for one substring check.
func scContainsSubstr(haystack, needle string) bool {
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
