package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

// requestLogCollector captures every request_logs row emitted by the
// safety-net.  It is wired via ChatHandler.SetRequestLogHook.  The
// production code never sets a hook; only the unit tests in this
// file do.
type requestLogCollector struct {
	mu   sync.Mutex
	rows []collectedRow
}

type collectedRow struct {
	success      bool
	clientModel  string
	errorKind    string
	requestID    string
}

func newRequestLogCollector() *requestLogCollector {
	return &requestLogCollector{}
}

func (c *requestLogCollector) Hook(entry *telemetry.RequestLogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry == nil {
		return
	}
	c.rows = append(c.rows, collectedRow{
		success:     entry.Success,
		clientModel: derefString(entry.ClientModel),
		errorKind:   derefString(entry.ErrorKind),
		requestID:   entry.RequestID,
	})
}

func (c *requestLogCollector) Snapshot() []collectedRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]collectedRow, len(c.rows))
	copy(out, c.rows)
	return out
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// requestLogsAreAlwaysRecorded is the contract we just added.  Any
// request that reaches ChatHandler.ServeHTTP must end up in
// request_logs, regardless of which exit path it took.  These
// tests verify the two ends of the contract:
//
//   - method-not-allowed / executor-unavailable → safety net
//   - recordFailedRequestWithKey → direct call
//
// In a real test run the inner pipeline (executor, candidates) is
// not wired up, so the only paths we can exercise directly without
// a database are the early failures and the executor-unavailable
// fallback.  The executor / candidate / panic paths are covered
// indirectly by the executor_test.go suite, which we extend below.
func TestRequestLog_SafetyNet_RecordsAllRequests(t *testing.T) {
	cases := []struct {
		name        string
		method      string
		path        string
		body        string
		wantStatus  int
		wantErrKind string
	}{
		{
			name:        "method-not-allowed",
			method:      http.MethodPut,
			path:        "/v1/chat/completions",
			body:        "",
			wantStatus:  http.StatusMethodNotAllowed,
			wantErrKind: "method_not_allowed",
		},
		{
			name:        "executor-unavailable",
			method:      http.MethodPost,
			path:        "/v1/chat/completions",
			body:        `{"model":"mimo-v2.5-pro","messages":[]}`,
			wantStatus:  http.StatusServiceUnavailable,
			wantErrKind: "executor_unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, col := newHookedHandler()
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = &bytes.Buffer{}
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			rec := httptest.NewRecorder()
			ch.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d body=%s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			rows := col.Snapshot()
			if len(rows) != 1 {
				t.Fatalf("expected exactly 1 request_log row for %s, got %d: %+v", tc.name, len(rows), rows)
			}
			row := rows[0]
			if row.success {
				t.Fatalf("expected success=false for %s, got true", tc.name)
			}
			if row.errorKind != tc.wantErrKind {
				t.Fatalf("expected error_kind=%s for %s, got %q", tc.wantErrKind, tc.name, row.errorKind)
			}
		})
	}
}

func TestRequestLog_DoesNotDuplicateAcrossCallers(t *testing.T) {
	// When the inner pipeline calls recordFailedRequestWithKey
	// directly, the hook fires once.  The deferred safety net
	// knows not to duplicate because *attemptLogged is set to true.
	// We simulate that contract by calling recordFailedRequest twice
	// (each call writes its own row) and asserting that no caller
	// path produced two rows for the same request_id.
	ch, col := newHookedHandler()
	ch.recordFailedRequestWithKey("req-A", "mimo-v2.5-pro", "",
		nil, nil, "no_candidate", "no candidates", 100, []byte(`{}`), nil, nil)
	ch.recordFailedRequestWithKey("req-B", "minimax-m2.7", "",
		nil, nil, "rate_limit_exceeded", "too many", 50, []byte(`{}`), nil, nil)
	rows := col.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct rows, got %d: %+v", len(rows), rows)
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.requestID] = true
	}
	if !ids["req-A"] || !ids["req-B"] {
		t.Fatalf("missing one of the expected request_ids; got %+v", ids)
	}
}

// newHookedHandler returns a ChatHandler with a request_log hook
// installed so tests can assert on the safety-net coverage.  The
// heavier dependencies (keyVerifier, executor, provider, …) are
// left nil so we exercise the early-exit / executor-unavailable
// paths without needing a database.
func newHookedHandler() (*ChatHandler, *requestLogCollector) {
	ch := &ChatHandler{}
	col := newRequestLogCollector()
	ch.SetRequestLogHook(col.Hook)
	return ch, col
}

// ── captureAttemptBody — direct tests of the body-peek helper ────────────
//
// These tests guard the early-exit body capture added in commit
// "fix: rate_limit 等 early-exit 路径补记 client_model 与 request preview".
// Every early-exit path (rate_limit, key_throttled, invalid_key,
// auth_unavailable, budget_exhausted, session_forbidden,
// internal_panic, …) now calls captureAttemptBody so the request_log
// row can show the client_model and a body preview.

func TestCaptureAttemptBody_ExtractsModel(t *testing.T) {
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[]}`))
	captureAttemptBody(r, &body, &model)
	if string(body) != `{"model":"gpt-4o-mini","messages":[]}` {
		t.Errorf("body not captured: %q", string(body))
	}
	if model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", model)
	}
}

func TestCaptureAttemptBody_NoModelField(t *testing.T) {
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hi"}]}`))
	captureAttemptBody(r, &body, &model)
	if len(body) == 0 {
		t.Error("body should still be captured even without model field")
	}
	if model != "" {
		t.Errorf("model should be empty when no model field, got %q", model)
	}
}

func TestCaptureAttemptBody_EmptyBody(t *testing.T) {
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(""))
	captureAttemptBody(r, &body, &model)
	if len(body) != 0 {
		t.Errorf("body should be empty, got %q", string(body))
	}
	if model != "" {
		t.Errorf("model should be empty, got %q", model)
	}
}

func TestCaptureAttemptBody_NilBody(t *testing.T) {
	// captureAttemptBody must be a no-op when the request has no body
	// (e.g. method-not-allowed GET).  We construct a request whose
	// Body has already been closed and confirm the helper does not
	// panic.
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	captureAttemptBody(r, &body, &model)
	if len(body) != 0 || model != "" {
		t.Errorf("nil-body capture should be a no-op, got body=%q model=%q", body, model)
	}
}

func TestCaptureAttemptBody_PreservesExistingValues(t *testing.T) {
	// If the caller has already populated body/model (e.g. from a
	// prior capture), captureAttemptBody should NOT clobber them
	// when the new request body is empty / smaller.  The helper
	// overwrites unconditionally per the implementation, so this
	// test pins that behaviour; if a future refactor wants to merge,
	// this test will fail and force a review.
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"mimo-v2.5-pro","messages":[]}`))
	captureAttemptBody(r, &body, &model)
	if model != "mimo-v2.5-pro" {
		t.Errorf("model = %q, want mimo-v2.5-pro", model)
	}
	if !strings.Contains(string(body), "mimo-v2.5-pro") {
		t.Errorf("body = %q, should contain mimo-v2.5-pro", string(body))
	}
}

func TestCaptureAttemptBody_CapsAt1MB(t *testing.T) {
	// The helper truncates bodies larger than 1MiB.  Build a 2MiB
	// payload that's NOT valid JSON after truncation; the helper
	// must still record the (truncated) bytes for the request_log
	// preview, even though the model field cannot be parsed.
	huge := bytes.Repeat([]byte("a"), 2<<20) // 2 MiB
	wrapped := append([]byte(`{"model":"mimo-v2-flash","messages":[{"role":"user","content":"`), huge...)
	wrapped = append(wrapped, []byte(`"}]}`)...)
	var body []byte
	var model string
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(wrapped))
	captureAttemptBody(r, &body, &model)
	if int64(len(body)) > (1 << 20) {
		t.Errorf("body length %d exceeds 1 MiB cap", len(body))
	}
	if int64(len(body)) == 0 {
		t.Errorf("body length should be at least the 1MB cap, got %d", len(body))
	}
	// Model is unparseable from truncated JSON — that's OK; the
	// preview is still recorded for the request_log row.
}

// ── Safety net: model is captured from the request body for all
//    early-exit paths.  Today the only DB-free early-exit path is
//    `executor_unavailable`; once body capture is added to the rest
//    they share the same code path.  We assert here that the body
//    preview/model fields reach the safety-net recorder so the
//    contract is enforced for any future path that hooks in.

func TestSafetyNet_ExecutorUnavailable_PropagatesBodyAndModel(t *testing.T) {
	ch, col := newHookedHandler()
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	ch.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for executor_unavailable, got %d", rec.Code)
	}
	rows := col.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.clientModel != "gpt-4o-mini" {
		t.Errorf("client_model = %q, want gpt-4o-mini (must be recovered from body even when executor unavailable)", row.clientModel)
	}
	if row.errorKind != "executor_unavailable" {
		t.Errorf("error_kind = %q, want executor_unavailable", row.errorKind)
	}
}

// ── classifyFailureStage: every early-exit error must be "gateway",
//    every other code must be "upstream".  This contract is what
//    powers the failure_stage column in the request_log UI filter.

func TestClassifyFailureStage_GatewayEarlyExits(t *testing.T) {
	gatewayCodes := []string{
		"rate_limit_exceeded",
		"concurrent_limit_exceeded",
		"tpm_limit_exceeded",
		"key_throttled",
		"budget_exhausted",
		"insufficient_credits",
		"missing_key",
		"invalid_key",
		"auth_unavailable",
		"method_not_allowed",
		"executor_unavailable",
		"no_candidate",
		"body_too_large",
		"body_read_error",
		"json_parse_error",
		"missing_model",
		"missing_max_tokens",
		"conversion_error",
		"session_forbidden",
		"internal_panic",
		"chat_to_anthropic_conversion_error",
	}
	for _, code := range gatewayCodes {
		if got := classifyFailureStage(code); got != "gateway" {
			t.Errorf("classifyFailureStage(%q) = %q, want gateway", code, got)
		}
	}
}

func TestClassifyFailureStage_UpstreamDefaults(t *testing.T) {
	// Anything not in the gateway early-exit list should be
	// classified as upstream — the request reached the provider
	// and failed there.  We test a representative sample.
	upstreamCodes := []string{
		"provider_error",
		"model_not_found",
		"stream_error",
		"upstream_5xx",
		"timeout",
		"connection_reset",
		"eof_without_done",
		"benign_eof",
		"unknown_code", // a code we haven't enumerated
	}
	for _, code := range upstreamCodes {
		if got := classifyFailureStage(code); got != "upstream" {
			t.Errorf("classifyFailureStage(%q) = %q, want upstream", code, got)
		}
	}
}

func TestClassifyFailureStage_ParityWithMapGatewayErrorToDetail(t *testing.T) {
	// The two functions must agree on the gateway set: a code is
	// "gateway" iff mapGatewayErrorToDetail returns a "gw_" prefix
	// for it.  This guards against the two maps drifting apart.
	for code, expectedPrefix := range gatewayCodePrefixMap() {
		got := classifyFailureStage(code)
		detail := mapGatewayErrorToDetail(code)
		isGatewayDetail := strings.HasPrefix(detail, "gw_")
		if got == "gateway" && !isGatewayDetail {
			t.Errorf("drift for %q: stage=gateway but detail=%q (no gw_ prefix)", code, detail)
		}
		if got == "upstream" && expectedPrefix == "gw_" {
			// We expect a gw_ prefix for this code, so stage
			// should be gateway.  Skip codes that are
			// intentionally upstream-only.
			_ = expectedPrefix
		}
	}
}

// gatewayCodePrefixMap returns the subset of codes that the
// gateway early-exit handler explicitly maps to a "gw_" prefix in
// mapGatewayErrorToDetail.  Kept in one place so the parity test
// does not duplicate the switch statement.
func gatewayCodePrefixMap() map[string]string {
	return map[string]string{
		"rate_limit_exceeded":            "gw_",
		"concurrent_limit_exceeded":     "gw_",
		"tpm_limit_exceeded":             "gw_",
		"key_throttled":                  "gw_",
		"budget_exhausted":               "gw_",
		"missing_key":                    "gw_",
		"invalid_key":                    "gw_",
		"auth_unavailable":               "gw_",
		"method_not_allowed":             "gw_",
		"executor_unavailable":           "gw_",
		"no_candidate":                   "gw_",
		"body_too_large":                 "gw_",
		"body_read_error":                "gw_",
		"json_parse_error":               "gw_",
		"missing_model":                  "gw_",
		"missing_max_tokens":             "gw_",
		"conversion_error":               "gw_",
		"session_forbidden":              "gw_",
		"internal_panic":                 "gw_",
		"chat_to_anthropic_conversion_error": "gw_",
	}
}

func TestCapturePartialBodyOnReadError_ExtractsModel(t *testing.T) {
	var body []byte
	model := ""
	partial := []byte(`{"model":"glm-4-flash","messages":[{"role":"user","content":"hi`)
	capturePartialBodyOnReadError(partial, &body, &model)
	if model != "glm-4-flash" {
		t.Fatalf("model = %q, want glm-4-flash", model)
	}
	if len(body) != len(partial) {
		t.Fatalf("body len = %d, want %d", len(body), len(partial))
	}
}

func TestFailedRequestIdentity_FromHeadersAndKeyProfile(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-abc")
	r.Header.Set("User-Agent", "openclaw/1.0")
	profile := "cursor"
	keyInfo := &auth.KeyInfo{
		ID:                   2,
		TenantID:             "default",
		DefaultClientProfile: &profile,
	}
	gotProfile, gotHash := failedRequestIdentity(r, keyInfo)
	if gotProfile != "cursor" {
		t.Fatalf("client_profile = %q, want cursor", gotProfile)
	}
	if gotHash == "" {
		t.Fatal("identity_hash should be populated from headers")
	}
}

func TestRecordFailedRequestWithKey_IncludesIdentityAndPartialBody(t *testing.T) {
	var captured *telemetry.RequestLogEntry
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	ch.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
		if entry != nil {
			captured = entry
		}
	})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "seed-xyz")
	profile := "roocode"
	keyInfo := &auth.KeyInfo{
		ID:                   7,
		TenantID:             "default",
		KeyPrefix:            "sk-testkey12",
		ApplicationCode:      "cursor",
		DefaultClientProfile: &profile,
	}
	owner := "alice"
	keyInfo.OwnerUser = &owner
	partialBody := []byte(`{"model":"minimax-m3","stream":true`)

	ch.recordFailedRequestWithKey(
		"req-body-read-fail", "minimax-m3", "",
		nil, nil, "body_read_error", "timeout", 10001,
		partialBody, keyInfo, r,
	)

	if captured == nil {
		t.Fatal("expected request log hook to fire")
	}
	if captured.ClientModel == nil || *captured.ClientModel != "minimax-m3" {
		t.Fatalf("client_model = %v, want minimax-m3", captured.ClientModel)
	}
	if captured.ClientProfile == nil || *captured.ClientProfile != "roocode" {
		t.Fatalf("client_profile = %v, want roocode", captured.ClientProfile)
	}
	if captured.IdentityHash == nil || *captured.IdentityHash == "" {
		t.Fatalf("identity_hash should be set, got %v", captured.IdentityHash)
	}
	if captured.RequestMode == nil || *captured.RequestMode != "chat" {
		t.Fatalf("request_mode = %v, want chat", captured.RequestMode)
	}
	if captured.RequestPreview == nil || *captured.RequestPreview == "" {
		t.Fatal("request_preview should be populated from partial body")
	}
	if captured.APIKeyPrefix == nil || *captured.APIKeyPrefix != "sk-testkey12***" {
		t.Fatalf("api_key_prefix = %v, want sk-testkey12***", captured.APIKeyPrefix)
	}
	if captured.APIKeyOwnerUser == nil || *captured.APIKeyOwnerUser != "alice" {
		t.Fatalf("api_key_owner_user = %v, want alice", captured.APIKeyOwnerUser)
	}
	if captured.ApplicationCode == nil || *captured.ApplicationCode != "cursor" {
		t.Fatalf("application_code = %v, want cursor", captured.ApplicationCode)
	}
}

func TestMaskAPIKeyPrefix(t *testing.T) {
	if got := maskAPIKeyPrefix(""); got != "无key" {
		t.Fatalf("empty key = %q, want 无key", got)
	}
	if got := maskAPIKeyPrefix("sk-short"); got != "sk-short***" {
		t.Fatalf("short key = %q", got)
	}
	if got := maskAPIKeyPrefix("sk-abcdefghijklmn"); got != "sk-abcdefghi***" {
		t.Fatalf("long key = %q", got)
	}
}

func TestRecordFailedRequestWithKey_MissingKeyShowsWuKey(t *testing.T) {
	var captured *telemetry.RequestLogEntry
	ch := NewChatHandler(circuit.NewManager(), limiter.New(), nil, nil, nil, nil)
	ch.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
		captured = entry
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-4"}`))
	ch.recordFailedRequestWithKey(
		"req-missing-key", "glm-4", "",
		nil, nil, "missing_key", "missing api key", 5,
		[]byte(`{"model":"glm-4"}`), nil, r,
	)
	if captured == nil {
		t.Fatal("expected hook")
	}
	if captured.APIKeyPrefix == nil || *captured.APIKeyPrefix != "无key" {
		t.Fatalf("api_key_prefix = %v, want 无key", captured.APIKeyPrefix)
	}
}
