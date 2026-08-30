package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

type healthTestConnector struct{ err error }

func (c healthTestConnector) Ping(context.Context) error { return c.err }

func TestHealthHandlerReadyzRequiresBothDependencies(t *testing.T) {
	tests := []struct {
		name  string
		db    dbConnector
		redis redisConnector
		want  int
	}{
		{name: "healthy", db: healthTestConnector{}, redis: healthTestConnector{}, want: http.StatusOK},
		{name: "missing database", redis: healthTestConnector{}, want: http.StatusServiceUnavailable},
		{name: "missing redis", db: healthTestConnector{}, want: http.StatusServiceUnavailable},
		{name: "database error", db: healthTestConnector{err: errors.New("private db error")}, redis: healthTestConnector{}, want: http.StatusServiceUnavailable},
		{name: "redis error", db: healthTestConnector{}, redis: healthTestConnector{err: errors.New("private redis error")}, want: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHandler(nil, nil, nil, tt.db, tt.redis)
			r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tt.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private ") {
				t.Fatalf("anonymous readiness leaked dependency error: %s", w.Body.String())
			}
		})
	}
}

func TestHealthHandlerHealthzIsLivenessWhenDependencyFails(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, healthTestConnector{}, healthTestConnector{err: errors.New("private redis error")})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode healthz response: %v", err)
	}
	if body.Ready {
		t.Fatalf("healthz ready = true, want false")
	}
	if strings.Contains(w.Body.String(), "private redis error") {
		t.Fatalf("anonymous liveness leaked dependency error: %s", w.Body.String())
	}
}

func TestHealthHandlerVersionDoesNotRequireDependencies(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	h.SetRuntimeIdentity("traffic-only", ":8782")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))
	if !strings.Contains(w.Body.String(), `"runtime_role":"traffic-only"`) || !strings.Contains(w.Body.String(), `"listen":":8782"`) {
		t.Fatalf("version response missing runtime identity: %s", w.Body.String())
	}
	for _, key := range []string{"version", "git_sha", "build_seq", "build_date", "module", "runtime_role", "listen"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("version response missing %q: %s", key, w.Body.String())
		}
	}
}

func TestApplyActualOutboundBodyPrefersExecutorBody(t *testing.T) {
	logCtx := &RequestLogContext{OutboundBody: []byte(`{"messages":[{"role":"system","content":"old"}]}`)}
	result := &executors.ExecuteResult{RequestBody: []byte(`{"system":"new","messages":[{"role":"user","content":"hi"}]}`)}

	applyActualOutboundBody(logCtx, result)

	if got := string(logCtx.OutboundBody); got != string(result.RequestBody) {
		t.Fatalf("OutboundBody = %s, want executor request body %s", got, result.RequestBody)
	}
}

func TestApplyActualOutboundBodyKeepsSnapshotWithoutExecutorBody(t *testing.T) {
	original := []byte(`{"messages":[{"role":"user","content":"snapshot"}]}`)
	logCtx := &RequestLogContext{OutboundBody: append([]byte(nil), original...)}

	applyActualOutboundBody(logCtx, &executors.ExecuteResult{})

	if got := string(logCtx.OutboundBody); got != string(original) {
		t.Fatalf("OutboundBody = %s, want existing snapshot %s", got, original)
	}
}

func TestSuccessUpstreamStatusCode(t *testing.T) {
	if got := successUpstreamStatusCode(&executors.ExecuteResult{Response: &http.Response{StatusCode: http.StatusCreated}}); got != http.StatusCreated {
		t.Fatalf("successUpstreamStatusCode() = %d, want %d", got, http.StatusCreated)
	}
	if got := successUpstreamStatusCode(&executors.ExecuteResult{}); got != http.StatusOK {
		t.Fatalf("successUpstreamStatusCode() without response = %d, want %d", got, http.StatusOK)
	}
	if got := successUpstreamStatusCode(nil); got != http.StatusOK {
		t.Fatalf("successUpstreamStatusCode(nil) = %d, want %d", got, http.StatusOK)
	}
}

func TestDetectEmptyStreamResponse_ToolOnlyStructuredCallIsNotEmpty(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	if detectEmptyStreamResponse(map[string]any{
		"stream_chunk_count": 2,
		"tool_calls":         []map[string]any{{"id": "call_1"}},
	}, entry) {
		t.Fatal("tool-only structured response must not be classified as empty")
	}
}

func TestDetectEmptyStreamResponse_EmptyStreamIsEmpty(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	if !detectEmptyStreamResponse(map[string]any{"stream_chunk_count": 2}, entry) {
		t.Fatal("contentless stream should be classified as empty")
	}
}

func TestShouldSkipAutoTitleGeneration(t *testing.T) {
	tests := []struct {
		name     string
		logCtx   *RequestLogContext
		expected bool
	}{
		{name: "nil logCtx treated as normal request", logCtx: nil, expected: false},
		{name: "normal request not skipped", logCtx: &RequestLogContext{}, expected: false},
		{name: "auto request skipped", logCtx: &RequestLogContext{IsAutoRequest: true}, expected: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSkipAutoTitleGeneration(tc.logCtx); got != tc.expected {
				t.Fatalf("shouldSkipAutoTitleGeneration() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// TestApplyParentCorrelationFields (2026-08-06) — guards the flow from
// logCtx.ParentRequestID / OriginActor into the persisted RequestLogEntry.
// Without this, request_logs_hot.parent_request_id is always NULL on
// auto-title loopback rows, so operators cannot SQL JOIN
// "08aa2a8a → 3a03f7db".
func TestApplyParentCorrelationFields(t *testing.T) {
	tests := []struct {
		name            string
		logCtx          *RequestLogContext
		wantParentID    *string
		wantOriginActor *string
	}{
		{
			name:            "nil logCtx is no-op",
			logCtx:          nil,
			wantParentID:    nil,
			wantOriginActor: nil,
		},
		{
			name:            "empty fields → nil entry pointers",
			logCtx:          &RequestLogContext{},
			wantParentID:    nil,
			wantOriginActor: nil,
		},
		{
			name: "both fields populated → both entry pointers filled",
			logCtx: &RequestLogContext{
				ParentRequestID: "08aa2a8af42ef05eb87c97973f467519",
				OriginActor:     "auto-title-generator",
			},
			wantParentID:    strPtrLocal("08aa2a8af42ef05eb87c97973f467519"),
			wantOriginActor: strPtrLocal("auto-title-generator"),
		},
		{
			name: "only parent → origin stays nil",
			logCtx: &RequestLogContext{
				ParentRequestID: "p",
			},
			wantParentID:    strPtrLocal("p"),
			wantOriginActor: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{}
			applyParentCorrelationFields(entry, tc.logCtx)
			if !ptrEqualString(entry.ParentRequestID, tc.wantParentID) {
				t.Errorf("ParentRequestID = %v, want %v", entry.ParentRequestID, tc.wantParentID)
			}
			if !ptrEqualString(entry.OriginActor, tc.wantOriginActor) {
				t.Errorf("OriginActor = %v, want %v", entry.OriginActor, tc.wantOriginActor)
			}
		})
	}
}

func strPtrLocal(s string) *string { return &s }

// ptrEqualString reports whether two *string are both nil or both point to
// the same string value (Go pointer comparison is fine for *string built by
// strPtrLocal in this test scope).
func ptrEqualString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestSanitizeGwSessionHeader (2026-08-06) — guards the three gateway
// session-id namespaces:
//
//	gw_<uuid>  — user main session (legacy)
//	gt_<...>   — auto-title branch (admin/auto_title_generator.go)
//	gs_<...>   — auto-summary branch (admin/auto_summary_generator.go)
//
// Strip the prefix to recover the parent user session id:
//
//	strings.TrimPrefix("gt_gw_abc", "gt_") == "gw_abc"
//	strings.TrimPrefix("gs_gw_abc", "gs_") == "gw_abc"
func TestSanitizeGwSessionHeader(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "whitespace stays empty", input: "   ", want: ""},
		{name: "plain uuid is rejected", input: "abc-123-def", want: ""},
		{name: "random session-tag-like is rejected", input: "sess_abc", want: ""},
		{name: "uppercase GW is rejected (case-sensitive)", input: "GW_abc", want: ""},
		{name: "main gw_ accepted", input: "gw_abc-123", want: "gw_abc-123"},
		{name: "title branch gt_ accepted", input: "gt_gw_abc-123", want: "gt_gw_abc-123"},
		{name: "summary branch gs_ accepted", input: "gs_gw_abc-123", want: "gs_gw_abc-123"},
		{name: "gt_ without trailing chars accepted", input: "gt_x", want: "gt_x"},
		{name: "gs_ without trailing chars accepted", input: "gs_x", want: "gs_x"},
		{name: "trailing whitespace trimmed", input: "  gw_abc  ", want: "gw_abc"},
		{name: "gt_ with whitespace preserved on content", input: "  gt_gw_abc  ", want: "gt_gw_abc"},
		// 2026-08-15 regression: the auto-title/auto-summary loopback used to
		// set "gt:" / "gs:" (colon) prefixes, which the sanitizer silently
		// dropped → the child row got a fresh gw_<uuid> and its parent_request_id
		// link was unusable. Colon forms MUST be rejected by the contract.
		{name: "title colon gt: rejected (bug class)", input: "gt:gw_abc", want: ""},
		{name: "summary colon gs: rejected (bug class)", input: "gs:gw_abc", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeGwSessionHeader(tc.input); got != tc.want {
				t.Fatalf("sanitizeGwSessionHeader(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestIsBranchSessionID (OBS-DV2 #4) — guards the predicate that decides
// whether an already-sanitized session id is an auto-title/auto-summary branch
// rather than a user main session. Branch ids MUST stay gt_/gs_ (matching the
// loopback callers) so the chat handler can preserve them verbatim instead of
// auto-creating a fresh gw_<uuid> and so they are never written to the
// no-session lastSystemSession resume pointer.
func TestIsBranchSessionID(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		want      bool
	}{
		{name: "empty is not branch", sessionID: "", want: false},
		{name: "main gw_ is not branch", sessionID: "gw_abc-123", want: false},
		{name: "title branch gt_ recognized", sessionID: "gt_gw_abc-123", want: true},
		{name: "summary branch gs_ recognized", sessionID: "gs_gw_abc-123", want: true},
		{name: "gt_ bare recognized", sessionID: "gt_x", want: true},
		{name: "gs_ bare recognized", sessionID: "gs_x", want: true},
		{name: "gt: colon is NOT branch (rejected upstream)", sessionID: "gt:gw_abc", want: false},
		{name: "gs: colon is NOT branch (rejected upstream)", sessionID: "gs:gw_abc", want: false},
		{name: "other prefix not branch", sessionID: "sess_abc", want: false},
		{name: "uppercase GT_ is NOT branch (case-sensitive)", sessionID: "GT_gw_abc", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBranchSessionID(tc.sessionID); got != tc.want {
				t.Fatalf("isBranchSessionID(%q) = %v, want %v", tc.sessionID, got, tc.want)
			}
		})
	}
}

// TestResolveEndUser (2026-08-06) — guards the end-user resolver that
// closes the "no user info at all" gap (dc767386f... incident). The
// priority chain must be:
//
//  1. bodyUser argument (already-parsed typed request body)
//  2. X-End-User-Id header
//  3. bodyBytes sniff for "user":"..."
//  4. r.Body sniff (fallback when body not yet drained)
//  5. "anonymous"
func TestResolveEndUser(t *testing.T) {
	mkReq := func(body string, header string) *http.Request {
		r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		if header != "" {
			r.Header.Set("X-End-User-Id", header)
		}
		return r
	}
	tests := []struct {
		name     string
		bodyUser string
		req      *http.Request
		body     []byte
		want     string
	}{
		{
			name:     "bodyUser wins over header",
			bodyUser: "alice@corp.com",
			req:      mkReq(`{"user":"bob"}`, "carol"),
			want:     "alice@corp.com",
		},
		{
			name:     "bodyUser whitespace falls through to header",
			bodyUser: "   ",
			req:      mkReq("", "header@corp.com"),
			want:     "header@corp.com",
		},
		{
			name:     "X-End-User-Id header wins when bodyUser empty",
			bodyUser: "",
			req:      mkReq(`{"user":"bob"}`, "carol@corp.com"),
			want:     "carol@corp.com",
		},
		{
			name:     "header whitespace-only falls through to body sniff",
			bodyUser: "",
			req:      mkReq("", "   "),
			body:     []byte(`{"user":"dave@corp.com"}`),
			want:     "dave@corp.com",
		},
		{
			name:     "body sniff wins when header absent",
			bodyUser: "",
			req:      mkReq("", ""),
			body:     []byte(`{"user":"dave@corp.com"}`),
			want:     "dave@corp.com",
		},
		{
			name:     "anonymous fallback when nothing available",
			bodyUser: "",
			req:      mkReq(`{"model":"gpt-4o"}`, ""),
			want:     "anonymous",
		},
		{
			name:     "header whitespace trimmed",
			bodyUser: "",
			req:      mkReq("", "  eve@corp.com  "),
			want:     "eve@corp.com",
		},
		{
			name:     "bodyBytes arg beats r.Body when header empty",
			bodyUser: "",
			req:      mkReq(`{"user":"from-r-body"}`, ""),
			body:     []byte(`{"user":"from-bodyBytes"}`),
			want:     "from-bodyBytes",
		},
		{
			name:     "nil request with valid bodyBytes returns user (audit fix)",
			bodyUser: "",
			req:      nil,
			body:     []byte(`{"user":"alice@corp.com"}`),
			want:     "alice@corp.com",
		},
		{
			name:     "nil request and no bodyBytes returns anonymous",
			bodyUser: "",
			req:      nil,
			want:     "anonymous",
		},
		{
			name:     "loose scan — body bytes with malformed JSON but user field present",
			bodyUser: "",
			req:      nil, // r is nil so we don't confuse with the r.Body path
			body:     []byte(`{"model":"gpt-4o","user":"trun"`),
			want:     "trun",
		},
		{
			name:     "body with non-string user field falls through to anonymous",
			bodyUser: "",
			req:      mkReq(`{"user":12345}`, ""),
			want:     "anonymous",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if tc.body != nil {
				got = resolveEndUser(tc.bodyUser, tc.req, tc.body)
			} else {
				got = resolveEndUser(tc.bodyUser, tc.req)
			}
			if got != tc.want {
				t.Fatalf("resolveEndUser() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveEndUser_DoesNotConsumeRBody (2026-08-06 audit fix) —
// the audit identified that the previous resolveEndUser read r.Body
// directly via io.ReadAll, which silently drained the body. After
// the audit fix, the function only reads caller-supplied bodyBytes.
// This test pins that contract by verifying r.Body is still readable
// after resolveEndUser returns.
func TestResolveEndUser_DoesNotConsumeRBody(t *testing.T) {
	body := `{"model":"gpt-4o","user":"alice"}`
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	// Call resolveEndUser — must NOT consume r.Body.
	got := resolveEndUser("", r, []byte(body))
	if got != "alice" {
		t.Fatalf("resolveEndUser returned %q, want alice", got)
	}

	// Verify r.Body is still readable end-to-end.
	rest, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read r.Body: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("r.Body was consumed/drained: got %q, want %q", rest, body)
	}
}

// TestExtractEndUserFromBody (2026-08-06) — unit-tests the JSON body
// sniffer used by the failure-path buildEntry to recover end-user
// identity from the captured body.
func TestExtractEndUserFromBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "valid JSON with user",
			body: `{"model":"gpt-4o","user":"alice@corp.com"}`,
			want: "alice@corp.com",
		},
		{
			name: "no user field",
			body: `{"model":"gpt-4o"}`,
			want: "",
		},
		{
			name: "loose scan — body with malformed JSON but user field present",
			body: `{"model":"gpt-4o","user":"trun"`,
			want: "trun",
		},
		{
			name: "loose scan — no closing quote returns empty",
			body: `{"user":abc`,
			want: "",
		},
		{
			name: "non-string user falls through to empty",
			body: `{"user":12345}`,
			want: "",
		},
		{
			// Strict-JSON path unescapes the value, so "a\"b" in the
			// body becomes the Go string `a"b` (3 bytes). Loose scan
			// would return the raw 4-byte sequence, but it never
			// triggers because strict unmarshal succeeds.
			name: "escaped quote inside user value (audit fix)",
			body: "{\"user\":\"a\\\"b\",\"model\":\"gpt-4o\"}",
			want: "a\"b",
		},
		{
			name: "nested user field is rejected (audit fix)",
			body: `{"messages":[{"role":"user","content":"hi","user":"nested"}]}`,
			want: "",
		},
		{
			name: "loose-scan gate — body not starting with '{' is rejected",
			body: `[{"user":"alice"}]`,
			want: "",
		},
		{
			name: "top-level user wins over nested user (audit fix)",
			body: `{"user":"top","messages":[{"user":"nested"}]}`,
			want: "top",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractEndUserFromBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("extractEndUserFromBody(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestShouldSkipAutoSummaryGeneration (2026-08-06) — symmetric to
// TestShouldSkipAutoTitleGeneration. The summary loopback sets
// X-Gw-Is-Auto: true so on its own emitTelemetry pass IsAutoRequest is
// true and the generator skips itself.
func TestShouldSkipAutoSummaryGeneration(t *testing.T) {
	tests := []struct {
		name     string
		logCtx   *RequestLogContext
		expected bool
	}{
		{name: "nil logCtx treated as normal request", logCtx: nil, expected: false},
		{name: "normal request not skipped", logCtx: &RequestLogContext{}, expected: false},
		{name: "auto request skipped", logCtx: &RequestLogContext{IsAutoRequest: true}, expected: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSkipAutoSummaryGeneration(tc.logCtx); got != tc.expected {
				t.Fatalf("shouldSkipAutoSummaryGeneration() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestDetectUpstreamContextLoss(t *testing.T) {
	tests := []struct {
		name            string
		bodyBytes       int
		promptTokens    int
		promptSet       bool // false → leave PromptTokens nil
		completion      int
		completionSet   bool // false → leave CompletionTokens nil
		finishReason    string
		includeFinish   bool // false → omit upstream_finish_reason from m
		toolCalls       bool
		wantContextLoss bool
	}{
		{
			name:            "incident parameters are detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: true,
		},
		{
			name:            "healthy sibling with full prompt is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    256000,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "small request is not detected",
			bodyBytes:       40 * 1024,
			promptTokens:    100,
			promptSet:       true,
			completion:      10,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "substantial completion is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      50,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "completion just below threshold still flagged",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      49,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: true,
		},
		{
			name:            "interrupted stream is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "eof_without_done",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "missing upstream_finish_reason is not detected",
			bodyBytes:       919 * 1024,
			promptTokens:    337,
			promptSet:       true,
			completion:      21,
			completionSet:   true,
			finishReason:    "",
			includeFinish:   false,
			wantContextLoss: false,
		},
		{
			name:            "missing prompt_tokens is not detected",
			bodyBytes:       919 * 1024,
			promptSet:       false,
			completion:      21,
			completionSet:   true,
			finishReason:    "end_turn",
			includeFinish:   true,
			wantContextLoss: false,
		},
		{
			name:            "tool call with short usage is not detected",
			bodyBytes:       129774,
			promptTokens:    33,
			promptSet:       true,
			completion:      39,
			completionSet:   true,
			finishReason:    "tool_calls",
			includeFinish:   true,
			toolCalls:       true,
			wantContextLoss: false,
		},
		{
			name:      "stop terminator with full body still flags when prompt drops",
			bodyBytes: 919 * 1024,

			promptTokens:    500,
			promptSet:       true,
			completion:      30,
			completionSet:   true,
			finishReason:    "stop",
			includeFinish:   true,
			wantContextLoss: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{
				RequestBytes: intPtr(tt.bodyBytes),
			}
			if tt.promptSet {
				entry.PromptTokens = intPtr(tt.promptTokens)
			}
			if tt.completionSet {
				entry.CompletionTokens = intPtr(tt.completion)
			}

			m := map[string]any{}
			if tt.includeFinish {
				m["upstream_finish_reason"] = tt.finishReason
			}
			if tt.toolCalls {
				m["tool_calls"] = []map[string]any{{
					"id":   "toolu_test",
					"type": "function",
				}}
			}

			got := detectUpstreamContextLoss(m, entry)
			if got != tt.wantContextLoss {

				t.Fatalf("detectUpstreamContextLoss() = %v, want %v", got, tt.wantContextLoss)
			}
		})
	}
}

func TestEstimatePromptTokensFromBytes(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero body", 0, 0},
		{"negative body clamps to zero", -1024, 0},
		{"1 byte is zero", 1, 0},
		{"4 bytes is one token", 4, 1},
		{"919 KB mirrors incident body", 919 * 1024, 919 * 1024 / 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimatePromptTokensFromBytes(tc.in); got != tc.want {
				t.Fatalf("estimatePromptTokensFromBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
