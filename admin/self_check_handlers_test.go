package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// TestHandleTriggerAvailability_NoWorker verifies GET /trigger/availability
// reports available=false with the right reason when the legacy worker is
// not initialized (the default in new probe mode since 2026-07-14).
func TestHandleTriggerAvailability_NoWorker(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{} // worker nil

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("availability endpoint must return 200 even when worker is nil (got %d)", rr.Code)
	}
	var body struct {
		Available    bool   `json:"available"`
		NewProbeMode bool   `json:"new_probe_mode"`
		Reason       string `json:"reason"`
		ErrorCode    string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Available {
		t.Fatalf("available should be false when worker is nil")
	}
	if !body.NewProbeMode {
		t.Fatalf("new_probe_mode should mirror LLM_GATEWAY_USE_NEW_PROBE_MODE")
	}
	if body.ErrorCode != "self_check.trigger.no_probe_path" {
		t.Fatalf("unexpected error_code: %q", body.ErrorCode)
	}
	if !strings.Contains(body.Reason, "节点探测队列") {
		t.Fatalf("reason should mention probe queue migration (cn text), got: %q", body.Reason)
	}
}

// TestHandleTriggerAvailability_OldProbeFallback verifies that when
// LLM_GATEWAY_USE_NEW_PROBE_MODE is false but worker is still nil, we
// fall back to the generic "worker_unavailable" error code.
func TestHandleTriggerAvailability_OldProbeFallback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d)", rr.Code)
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.worker_unavailable" {
		t.Fatalf("expected generic worker_unavailable code, got %q", body.ErrorCode)
	}
}

// TestHandleTrigger_NoWorkerReturnsGone verifies POST /trigger returns 410
// (Gone) instead of 503 when the worker is nil. 503 misled the UI into
// thinking it was a transient outage; 410 is the correct semantic for
// "endpoint retired under current probe mode".
func TestHandleTrigger_NoWorkerReturnsGone(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"minimax-m2.7"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.no_probe_path" {
		t.Fatalf("expected no_probe_path, got %q", body.ErrorCode)
	}
	if !strings.Contains(body.Message, "durable probe queue") {
		t.Fatalf("message should mention the missing durable probe queue, got: %q", body.Message)
	}
}

// TestHandleTrigger_ProbeEnqueue (2026-08-18): under the new probe mode the
// trigger now fans out through the durable queue instead of 410-ing. This is
// the glm-5.2 incident follow-up: operators must always have a manual way to
// demand fresh probe evidence.
func TestHandleTrigger_ProbeEnqueue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) {
		if model != "glm-5.2" {
			t.Errorf("unexpected model %q", model)
		}
		return 3, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"glm-5.2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		OK       bool   `json:"ok"`
		Mode     string `json:"mode"`
		Enqueued int    `json:"enqueued"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Mode != "probe_queue" || body.Enqueued != 3 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

// TestHandleTrigger_ProbeEnqueueError reports individual enqueue failures in
// the unified fan-out response envelope. Both single-model and multi-model
// callers receive the same shape: 200 OK with a per-model results map that
// records which models failed and why. The single-model 503 fast-path that
// earlier lived in handleTrigger was removed by f8bf429ec
// (fix(admin): harden batch self-check model selection), so the handler now
// treats a single named model as a one-element fan-out.
//
// 2026-09-01 sync update (commit 24dead9d1 / audit bc48559b4 contract): when
// EVERY requested model fails to enqueue, handleTrigger returns 503 with
// {error, message, models_failed, results} so the UI doesn't render a
// misleading 200 OK banner. Partial failures still return 200 with per-model
// details.
func TestHandleTrigger_ProbeEnqueueError(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) {
		return 0, errors.New("db down")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{"model":"glm-5.2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on all-models enqueue failure (got %d, body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Error        string `json:"error"`
		Message      string `json:"message"`
		Enqueued     int    `json:"enqueued"`
		ModelsFailed int    `json:"models_failed"`
		Results      map[string]struct {
			Error    string `json:"error"`
			Enqueued int    `json:"enqueued"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "trigger failed" || body.Message != "db down" || body.Enqueued != 0 || body.ModelsFailed != 1 {
		t.Fatalf("unexpected envelope: %+v", body)
	}
	if body.Results["glm-5.2"].Error != "db down" || body.Results["glm-5.2"].Enqueued != 0 {
		t.Fatalf("unexpected per-model result: %+v", body.Results["glm-5.2"])
	}
}

// TestHandleTriggerAvailability_ProbeEnqueue verifies the availability
// endpoint reports available=true with mode=probe_queue once the enqueue
// path is wired (drives the UI button state).
func TestHandleTriggerAvailability_ProbeEnqueue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	h := &SelfCheckHandler{}
	h.SetProbeEnqueue(func(ctx context.Context, model string) (int, error) { return 0, nil })

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger/availability", nil)
	rr := httptest.NewRecorder()
	h.handleTriggerAvailability(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (got %d)", rr.Code)
	}
	var body struct {
		Available bool   `json:"available"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Available || body.Mode != "probe_queue" {
		t.Fatalf("expected available probe_queue mode, got: %+v", body)
	}
}

// TestHandleTrigger_OldProbeFallbackGone verifies that under old probe mode
// (rollback path) with no worker, we still return 410 but with the generic
// worker_unavailable error code so operators know to inspect worker startup.
func TestHandleTrigger_OldProbeFallbackGone(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	h := &SelfCheckHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/self-check/trigger",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone (got %d)", rr.Code)
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ErrorCode != "self_check.trigger.worker_unavailable" {
		t.Fatalf("expected generic worker_unavailable, got %q", body.ErrorCode)
	}
}

// TestHandleTrigger_MethodNotAllowed guards against non-POST traffic.
func TestHandleTrigger_MethodNotAllowed(t *testing.T) {
	h := &SelfCheckHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/self-check/trigger", nil)
	rr := httptest.NewRecorder()
	h.handleTrigger(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on POST endpoint, got %d", rr.Code)
	}
}

// TestScNewProbeMode_EnvMatrix locks down the env-parsing contract so future
// refactors in either cmd/gateway/main.go or admin/self_check_handlers.go
// don't drift.
func TestScNewProbeMode_EnvMatrix(t *testing.T) {
	cases := []struct {
		env   string
		unset bool
		want  bool
	}{
		{"", true, true},  // unset → default true
		{"", false, true}, // empty → default true
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", false, false},
		{"0", false, false},
		{"no", false, false},
		{"off", false, false},
		{"random", false, false},
	}
	for _, c := range cases {
		name := c.env
		if c.unset {
			name = "<unset>"
		}
		t.Run(name, func(t *testing.T) {
			if c.unset {
				// best-effort unset for the duration of the test
				old, had := os.LookupEnv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
				os.Unsetenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")
				defer func() {
					if had {
						os.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", old)
					}
				}()
			} else {
				t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", c.env)
			}
			if got := scNewProbeMode(); got != c.want {
				t.Fatalf("env=%q want=%v got=%v", c.env, c.want, got)
			}
		})
	}
}

// 2026-10-16 audit #155: tenant scope on self-check handlers.
//
// `self_check_runs` has a `tenant_id` column (migration 338) but the
// pre-existing SQL had no WHERE on it. The run-detail endpoint
// (handleGetRun) is the load-bearing case: it returns `request_body` and
// `response_preview` from `self_check_round_results`, which has no
// tenant_id of its own and is FK-linked to runs. Without the run-level
// tenant filter a tenant_admin can fetch another tenant's round results
// by guessing the run id.

func TestHandleListRuns_TenantFilterInSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// Expect the SELECT (with tenantID + limit) and the COUNT (with tenantID).
	mock.ExpectQuery(`tenant_id`).
		WithArgs("acme", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "model_name", "started_at", "completed_at", "duration_ms",
			"status", "rounds_total", "rounds_success", "had_tool_call",
			"total_tokens", "avg_latency_ms",
			"error_type", "error_detail",
			"upstream_tested", "upstream_result",
			"upstream_latency_ms", "upstream_error",
			"selection_strategy", "attempted_models",
		}))
	mock.ExpectQuery(`COUNT`).
		WithArgs("acme").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/runs", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("list-runs must hit SQL with tenant filter: %v", err)
	}
}

func TestHandleGetRun_TenantAdminCrossTenant_404(t *testing.T) {
	// tenant_admin asks for a run that exists in another tenant. The
	// run-level WHERE filter rejects it (ErrNoRows), handler returns 404
	// without ever reaching the rounds query — proves request_body /
	// response_preview are not leaked.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// No second ExpectQuery here on purpose: if the handler reaches the
	// rounds query, the test will fail because pgxmock sees an unexpected
	// call. That's the security invariant: cross-tenant lookup must not
	// even attempt to read rounds.
	mock.ExpectQuery(`FROM self_check_runs WHERE id=\$1 AND \(\$2 = '' OR tenant_id = \$2\)`).
		WithArgs(int64(42), "acme").
		WillReturnError(pgx.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/runs/42", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleGetRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cross-tenant lookup must not reach rounds query: %v", err)
	}
}

func TestHandleGetRun_TenantAdminOwnTenant_OK(t *testing.T) {
	// tenant_admin asks for a run that belongs to its own tenant — the
	// run query returns a row, the rounds query fires, the handler
	// returns 200 with rounds.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	runCols := []string{
		"id", "model_name", "started_at", "completed_at", "duration_ms",
		"status", "rounds_total", "rounds_success", "had_tool_call",
		"total_tokens", "avg_latency_ms",
		"error_type", "error_detail",
		"upstream_tested", "upstream_result",
		"upstream_latency_ms", "upstream_error",
		"selection_strategy", "attempted_models",
	}
	startedAt := time.Now()
	mock.ExpectQuery(`FROM self_check_runs`).
		WithArgs(int64(42), "acme").
		WillReturnRows(pgxmock.NewRows(runCols).AddRow(
			int64(42), "claude-sonnet-5", startedAt, nil, 100,
			"success", 3, 3, false, 1000, 50,
			"", "", true, "success", 50, "",
			"", []byte("[]")))
	roundCols := []string{
		"id", "round_index", "is_ping", "is_tool_call", "latency_ms",
		"prompt_tokens", "completion_tokens", "total_tokens",
		"success", "http_code", "error_message",
		"request_body", "response_preview", "created_at",
	}
	mock.ExpectQuery(`FROM self_check_round_results`).
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(roundCols).AddRow(
			int64(1), 0, true, false, 50, 0, 0, 0, true, 200, "",
			"ping body", "pong preview", time.Now()))

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/runs/42", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleGetRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("own-tenant flow expectations: %v", err)
	}
}

func TestHandleGetRun_SuperAdminEmptyTenant_OK(t *testing.T) {
	// super_admin caller (tenant_id "") must still see runs — the
	// `($2 = '' OR tenant_id = $2)` predicate matches because of the
	// empty-string branch.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	runCols := []string{
		"id", "model_name", "started_at", "completed_at", "duration_ms",
		"status", "rounds_total", "rounds_success", "had_tool_call",
		"total_tokens", "avg_latency_ms",
		"error_type", "error_detail",
		"upstream_tested", "upstream_result",
		"upstream_latency_ms", "upstream_error",
		"selection_strategy", "attempted_models",
	}
	mock.ExpectQuery(`FROM self_check_runs`).
		WithArgs(int64(42), "").
		WillReturnRows(pgxmock.NewRows(runCols).AddRow(
			int64(42), "claude-sonnet-5", time.Now(), nil, 100,
			"success", 3, 3, false, 1000, 50,
			"", "", true, "success", 50, "",
			"", []byte("[]")))
	roundCols := []string{
		"id", "round_index", "is_ping", "is_tool_call", "latency_ms",
		"prompt_tokens", "completion_tokens", "total_tokens",
		"success", "http_code", "error_message",
		"request_body", "response_preview", "created_at",
	}
	mock.ExpectQuery(`FROM self_check_round_results`).
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(roundCols))

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/runs/42", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: ""})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleGetRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleStats_TenantFilterInSQL(t *testing.T) {
	// 2026-10-16 audit #155: handleStats has 4 SQL queries against
	// self_check_runs — summary / per-model / error_breakdown / trend.
	// Independent verifier caught that the original #155 commit only
	// updated 2 of 4, leaving error_breakdown and trend unfiltered.
	// This test now pins all 4 so a future scope-tight regression lights
	// up the strict pgxmock "unexpected query" path.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	// 1. summary
	mock.ExpectQuery(`tenant_id`).
		WithArgs(pgxmock.AnyArg(), "acme").
		WillReturnRows(pgxmock.NewRows([]string{
			"count", "s_count", "p_count", "f_count",
		}).AddRow(0, 0, 0, 0))
	// 2. per-model
	mock.ExpectQuery(`tenant_id`).
		WithArgs(pgxmock.AnyArg(), "acme").
		WillReturnRows(pgxmock.NewRows([]string{
			"model_name", "total", "s_count", "p_count", "f_count", "avg_lat",
		}))
	// 3. error_breakdown (must carry tenant_id too — formerly unscoped)
	mock.ExpectQuery(`status != 'success'`).
		WithArgs(pgxmock.AnyArg(), "acme").
		WillReturnRows(pgxmock.NewRows([]string{"error_type", "count"}))
	// 4. trend (must carry tenant_id too — formerly unscoped)
	mock.ExpectQuery(`date_trunc`).
		WithArgs(pgxmock.AnyArg(), "acme").
		WillReturnRows(pgxmock.NewRows([]string{"ts", "success_rate", "total"}))

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/stats?range=24h", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleStats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("stats must hit SQL 4× with tenant filter (any missing → unexpected call): %v", err)
	}
}

func TestHandleModels_TenantFilterInSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`tenant_id`).
		WithArgs("acme").
		WillReturnRows(pgxmock.NewRows([]string{
			"model_name", "total", "s_count", "f_count", "last_run",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/self-check/models", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
	rr := httptest.NewRecorder()
	(&SelfCheckHandler{db: mock}).handleModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("models must hit SQL with tenant filter: %v", err)
	}
}
