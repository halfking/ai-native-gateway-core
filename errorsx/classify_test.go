package errorsx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestClassifyError_ContextCanceled(t *testing.T) {
	kind := ClassifyError(context.Canceled, nil)
	if kind != KindCanceled {
		t.Errorf("expected KindCanceled for context.Canceled, got %q", kind)
	}
}

func TestClassifyError_Timeout(t *testing.T) {
	kind := ClassifyError(context.DeadlineExceeded, nil)
	if kind != KindTimeout {
		t.Errorf("expected KindTimeout, got %q", kind)
	}
}

func TestClassifyError_ConnectionRefused(t *testing.T) {
	err := errors.New("dial tcp 127.0.0.1:443: connection refused")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for connection refused, got %q", kind)
	}
}

func TestClassifyError_ConnectionReset(t *testing.T) {
	err := errors.New("read tcp: connection reset by peer")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for connection reset, got %q", kind)
	}
}

func TestClassifyError_DNSFailure(t *testing.T) {
	err := errors.New("lookup nonexistent.example.com: no such host")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for DNS failure, got %q", kind)
	}
}

func TestClassifyError_GenericError(t *testing.T) {
	err := errors.New("some random error")
	kind := ClassifyError(err, nil)
	if kind != KindTransient {
		t.Errorf("expected KindTransient for generic error, got %q", kind)
	}
}

func TestClassifyError_NilError_NilResponse(t *testing.T) {
	kind := ClassifyError(nil, nil)
	if kind != KindUpstreamDown {
		t.Errorf("expected KindUpstreamDown for nil err and nil resp, got %q", kind)
	}
}

func TestClassifyError_429(t *testing.T) {
	resp := &http.Response{StatusCode: 429}
	kind := ClassifyError(nil, resp)
	if kind != KindRateLimit {
		t.Errorf("expected KindRateLimit for 429, got %q", kind)
	}
}

func TestClassifyError_401(t *testing.T) {
	resp := &http.Response{StatusCode: 401}
	kind := ClassifyError(nil, resp)
	if kind != KindAuth {
		t.Errorf("expected KindAuth for 401, got %q", kind)
	}
}

func TestClassifyError_403(t *testing.T) {
	resp := &http.Response{StatusCode: 403}
	kind := ClassifyError(nil, resp)
	if kind != KindAuth {
		t.Errorf("expected KindAuth for 403, got %q", kind)
	}
}

func TestClassifyError_402(t *testing.T) {
	resp := &http.Response{StatusCode: 402}
	kind := ClassifyError(nil, resp)
	if kind != KindQuota {
		t.Errorf("expected KindQuota for 402, got %q", kind)
	}
}

func TestClassifyError_500(t *testing.T) {
	resp := &http.Response{StatusCode: 500}
	kind := ClassifyError(nil, resp)
	if kind != KindUpstreamDown {
		t.Errorf("expected KindUpstreamDown for 500, got %q", kind)
	}
}

func TestClassifyError_200(t *testing.T) {
	resp := &http.Response{StatusCode: 200}
	kind := ClassifyError(nil, resp)
	if kind != KindTransient {
		t.Errorf("expected KindTransient for 200, got %q", kind)
	}
}

func TestClassifyError_503_Overloaded(t *testing.T) {
	resp := &http.Response{StatusCode: 503}
	kind := ClassifyError(nil, resp)
	if kind != KindConcurrent {
		t.Errorf("expected KindConcurrent for 503, got %q", kind)
	}
}

func TestClassifyError_529_Overloaded(t *testing.T) {
	resp := &http.Response{StatusCode: 529}
	kind := ClassifyError(nil, resp)
	if kind != KindConcurrent {
		t.Errorf("expected KindConcurrent for 529, got %q", kind)
	}
}

func TestClassifyErrorWithBody_429_Overload(t *testing.T) {
	// 429 + "concurrent limit exceeded" body must escalate to KindConcurrent,
	// not stay as the quota-style KindRateLimit.
	body := []byte(`{"error":{"type":"rate_limit_error","message":"concurrent limit exceeded for this account"}}`)
	kind := ClassifyErrorWithBody(429, body)
	if kind != KindConcurrent {
		t.Errorf("expected KindConcurrent for 429+overload body, got %q", kind)
	}
}

func TestClassifyErrorWithBody_429_Plain(t *testing.T) {
	// 429 with a plain body stays as KindRateLimit (quota-style).
	kind := ClassifyErrorWithBody(429, []byte(`{"error":"rate limit"}`))
	if kind != KindRateLimit {
		t.Errorf("expected KindRateLimit for plain 429, got %q", kind)
	}
}

func TestClassifyErrorWithBody_503_Overload(t *testing.T) {
	kind := ClassifyErrorWithBody(503, []byte(`{"error":"server overloaded"}`))
	if kind != KindConcurrent {
		t.Errorf("expected KindConcurrent for 503+overload, got %q", kind)
	}
}

func TestClassifyError_ConcurrentErrMessage(t *testing.T) {
	tests := []string{
		"concurrent limit exceeded",
		"too many concurrent requests",
		"server overloaded, try again later",
		"engine busy, please slow down",
		"并发请求超限",
		"服务繁忙，请稍后重试",
	}
	for _, msg := range tests {
		kind := ClassifyError(errors.New(msg), nil)
		if kind != KindConcurrent {
			t.Errorf("expected KindConcurrent for %q, got %q", msg, kind)
		}
	}
}

func TestClassifyError_EOFWithoutDoneIsStreamTimeout(t *testing.T) {
	// eof_without_done / eof without / unexpected eof / etc. are
	// provider quirks (MiniMax omits [DONE] on successful streams)
	// and must NOT be classified as KindConcurrent (which would
	// trigger 5-min cooling). They map to KindStreamTimeout instead.
	tests := []string{
		"upstream sent EOF without [DONE] (eof_without_done)",
		"io: unexpected EOF",
		"stream closed before completion",
	}
	for _, msg := range tests {
		kind := ClassifyError(errors.New(msg), nil)
		if kind == KindConcurrent {
			t.Errorf("eof-shaped error %q must NOT be KindConcurrent (would trigger 5-min cooling), got %q", msg, kind)
		}
		if kind != KindStreamTimeout {
			t.Errorf("expected KindStreamTimeout for %q, got %q", msg, kind)
		}
	}
}

func TestClassifyResponseBody_GoDefault404_IsTransient(t *testing.T) {
	// Even with a 404 status, a generic web-server 404 body ("404 page not
	// found") must NOT be classified as KindModelNotFound — the body
	// doesn't reference a model or endpoint identifier (P5 tightening).
	if got := ClassifyResponseBody(404, []byte("404 page not found")); got != "" {
		t.Fatalf("expected empty (no match) for Go default 404 body, got %q; generic web-server 404 must NOT be classified as KindModelNotFound", got)
	}
}

func TestClassifyResponseBody_ConcurrentOverload(t *testing.T) {
	tests := []struct {
		body string
		kind ErrorKind
	}{
		{`{"error":"concurrent limit reached"}`, KindConcurrent},
		{`upstream: too many requests, slow down`, KindConcurrent},
		{`并发过大，达到上限`, KindConcurrent},
		{`eof_without_done`, KindStreamTimeout},
	}
	for _, tc := range tests {
		kind := ClassifyResponseBody(429, []byte(tc.body))
		if kind != tc.kind {
			t.Errorf("for %q expected %q, got %q", tc.body, tc.kind, kind)
		}
	}
}

// TestClassifyResponseBody_ModelNotFound_P5Tightened covers the regex / status
// tightening from 2026-06-18-model-match-and-404-plan.md §P5.
//
// Negative cases: bodies that USED to mis-classify as KindModelNotFound
// (region errors, deprecation notices, training-job errors) must now stay
// unmatched. Positive cases: legitimate "model X is not found" bodies on
// 400/404/422 must still be detected.
func TestClassifyResponseBody_ModelNotFound_P5Tightened(t *testing.T) {
	positive := []struct {
		name   string
		status int
		body   string
	}{
		{name: "anthropic_model_not_found", status: 404, body: `{"type":"error","error":{"type":"not_found_error","message":"model: claude-opus-4-6 not found"}}`},
		{name: "openai_model_not_found", status: 404, body: `{"error":{"message":"The model 'gpt-5-fake' does not exist","type":"invalid_request_error"}}`},
		{name: "model_unknown", status: 404, body: `model glm-5.1 is unknown`},
		{name: "no_such_model", status: 400, body: `{"error":"no such model: foo"}`},
		{name: "endpoint_does_not_exist", status: 422, body: `endpoint /v1/foo does not exist`},
		{name: "cjk_no_match", status: 404, body: `模型不存在`},
		{name: "cjk_no_match_with_name", status: 404, body: `模型 glm-5.1 不存在`},
	}
	for _, tc := range positive {
		t.Run("pos/"+tc.name, func(t *testing.T) {
			got := ClassifyResponseBody(tc.status, []byte(tc.body))
			if got != KindModelNotFound {
				t.Errorf("ClassifyResponseBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, KindModelNotFound)
			}
		})
	}

	negative := []struct {
		name   string
		status int
		body   string
	}{
		// region / tier restriction — looks like "model is not available" but isn't model_not_found
		{name: "region_unavailable", status: 403, body: `{"error":"Model glm-5.1 is not available in your region"}`},
		// deprecation — a working model, not missing
		{name: "deprecated", status: 200, body: `{"error":"Model glm-4 has been deprecated, please use glm-5"}`},
		// training job error — "model" and "not found" appear far apart in unrelated context
		{name: "training_job_error", status: 500, body: `Your previous model training run was not found, please retry with a new model id`},
		// 5xx upstream with a body that mentions a model — should NOT be model_not_found (P5)
		{name: "502_with_model_in_body", status: 502, body: `model glm-5.1 not found`},
		// generic 4xx with no model reference
		{name: "404_no_model", status: 404, body: `404 page not found`},
		// 200 OK with deprecation advisory — should be a different kind, not model_not_found
		{name: "200_with_deprecation", status: 200, body: `model glm-4 has been retired`},
	}
	for _, tc := range negative {
		t.Run("neg/"+tc.name, func(t *testing.T) {
			got := ClassifyResponseBody(tc.status, []byte(tc.body))
			if got == KindModelNotFound {
				t.Errorf("ClassifyResponseBody(%d, %q) = KindModelNotFound, want NOT model_not_found (P5 misclassification guard)",
					tc.status, tc.body)
			}
		})
	}
}

// TestClassifyErrorWithBody_ModelNotFound_P5StatusGate verifies the P5 status
// gate in ClassifyErrorWithBody matches ClassifyResponseBody: model_not_found
// is only returned for 400/404/422. A 5xx body that mentions "model not found"
// must be KindUpstreamDown (connectivity failure, not model existence).
func TestClassifyErrorWithBody_ModelNotFound_P5StatusGate(t *testing.T) {
	// 502 with "model not found" body → NOT KindModelNotFound
	kind := ClassifyErrorWithBody(502, []byte(`model glm-5.1 not found`))
	if kind == KindModelNotFound {
		t.Errorf("ClassifyErrorWithBody(502, 'model glm-5.1 not found') = KindModelNotFound, want KindUpstreamDown (P5 status gate)")
	}
	if kind != KindUpstreamDown {
		t.Errorf("ClassifyErrorWithBody(502, 'model glm-5.1 not found') = %q, want KindUpstreamDown", kind)
	}

	// 404 with "model not found" body → KindModelNotFound (within gate)
	kind = ClassifyErrorWithBody(404, []byte(`model glm-5.1 not found`))
	if kind != KindModelNotFound {
		t.Errorf("ClassifyErrorWithBody(404, 'model glm-5.1 not found') = %q, want KindModelNotFound", kind)
	}

	// 400 with "no such model" body → KindModelNotFound (within gate)
	kind = ClassifyErrorWithBody(400, []byte(`{"error":"no such model: foo"}`))
	if kind != KindModelNotFound {
		t.Errorf("ClassifyErrorWithBody(400, ...) = %q, want KindModelNotFound", kind)
	}
}

func TestIsConcurrentOverload(t *testing.T) {
	if IsConcurrentOverload("eof_without_done") {
		t.Fatal("eof_without_done should NOT be treated as concurrent overload")
	}
	if !IsConcurrentOverload("concurrent limit exceeded") {
		t.Fatal("concurrent limit exceeded should be treated as concurrent overload")
	}
	if !IsConcurrentOverload("engine busy, too many requests") {
		t.Fatal("engine busy should be treated as concurrent overload")
	}
	if IsConcurrentOverload("") {
		t.Fatal("empty reason should not be treated as concurrent overload")
	}
	if IsConcurrentOverload("client_cancel") {
		t.Fatal("client_cancel should not be treated as concurrent overload")
	}
}

func TestClassifyErrorWithBody_ToolCallIdMismatch(t *testing.T) {
	// MiniMax 4xx body that surfaced during a multi-turn tool flow.
	// The gateway must recognise this as a client-side bug and
	// return KindToolCallIdMismatch so the credential is NOT cooled
	// and the audit row carries an explicit "client bug" annotation
	// for the operator to find.
	tests := []struct {
		name string
		body string
	}{
		{
			"minimax-2013",
			`{"error":{"code":2013,"message":"invalid params, tool result's tool id (call_function_poab3apjo8kn_1) not found","type":"invalid_request"}}`,
		},
		{
			"plain-2013",
			`{"type":"error","error":{"type":"invalid_request_error","message":"2013"}}`,
		},
		{
			"anthropic-style",
			`{"type":"error","error":{"type":"invalid_request_error","message":"tool_use_id not found"}}`,
		},
		{
			"openai-style",
			`{"error":{"message":"Invalid tool_call_id: call_xxx","type":"invalid_request_error","param":"messages.2.tool_call_id","code":"invalid_value"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind := ClassifyErrorWithBody(400, []byte(tc.body))
			if kind != KindToolCallIdMismatch {
				t.Errorf("body %q: kind = %q, want %q", tc.body[:min(80, len(tc.body))], kind, KindToolCallIdMismatch)
			}
		})
	}
}

func TestIsClientBug(t *testing.T) {
	if !IsClientBug(KindToolCallIdMismatch) {
		t.Error("KindToolCallIdMismatch must be flagged as a client bug")
	}
	// 2026-07-03: Bug #10 fix - KindModelNotFound is no longer a client bug.
	// model_not_found is a provider-side issue (model removed/renamed upstream)
	// and must trigger binding unavailability via the dedicated mnf branch,
	// not skip state writes via IsClientBug.
	if IsClientBug(KindModelNotFound) {
		t.Error("KindModelNotFound must NOT be flagged as a client bug (Bug #10 fix)")
	}
	if !IsClientBug(KindCanceled) {
		t.Error("KindCanceled must be flagged as a client bug")
	}
	if IsClientBug(KindConcurrent) {
		t.Error("KindConcurrent must NOT be a client bug")
	}
	if IsClientBug(KindAuth) {
		t.Error("KindAuth must NOT be a client bug")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		kind     ErrorKind
		expected bool
	}{
		{KindTransient, true},
		{KindTimeout, true},
		{KindNetwork, true},
		{KindUpstreamDown, true},
		{KindRateLimit, false},
		{KindAuth, false},
		{KindQuota, false},
		{ErrorKind(""), false},
	}
	for _, tt := range tests {
		got := IsRetryable(tt.kind)
		if got != tt.expected {
			t.Errorf("IsRetryable(%q) = %v, want %v", tt.kind, got, tt.expected)
		}
	}
}

// 2026-06-13: protocol/shape 4xx codes must NOT be classified as
// KindTransient (which would trigger cross-credential retry and 10-20s
// stalls when an upstream intermittently returns 405).
func TestClassifyErrorWithBody_Protocol4xx(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"405_method_not_allowed", 405, `Method Not Allowed`, KindUnsupportedFeature},
		{"406_not_acceptable", 406, `not acceptable`, KindUnsupportedFeature},
		{"415_unsupported_media_type", 415, `unsupported media type`, KindUnsupportedFeature},
		{"409_conflict", 409, `conflict`, KindUnsupportedFeature},
		{"410_gone", 410, `gone`, KindUnsupportedFeature},
		{"422_unprocessable", 422, `unprocessable entity`, KindUnsupportedFeature},
		{"408_request_timeout_still_timeout", 408, `request timeout`, KindTimeout},
		{"401_unauthorized_still_auth", 401, `unauthorized`, KindAuth},
		// 429 with overload-shaped body intentionally upgrades to
		// KindConcurrent per the comment in ClassifyResponseStatus; only
		// a plain 429 stays as KindRateLimit.
		{"429_plain_still_rate_limit", 429, `quota exceeded`, KindRateLimit},
		{"500_still_upstream_down", 500, `internal server error`, KindUpstreamDown},
		{"502_still_upstream_down", 502, `bad gateway`, KindUpstreamDown},
		{"503_still_concurrent", 503, `service unavailable`, KindConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, tc.want)
			}
			// All these should also NOT be retryable except
			// 408/500/502/503/429 (which are intentionally retryable).
			retryable := IsRetryable(got)
			retryableWant := tc.want == KindTimeout ||
				tc.want == KindUpstreamDown ||
				tc.want == KindConcurrent ||
				tc.want == KindTransient
			if retryable != retryableWant {
				t.Errorf("IsRetryable(%q) = %v, want %v",
					got, retryable, retryableWant)
			}
		})
	}
}

// 2026-07-03 Bug #N regression test (defense-in-depth for the
// `fmt.Errorf("upstream %d: %s", status, body)` re-wrap path).
//
// Production: 326 transient errors in 24h, 325 of which were
// minimax-m3 tool_call_id_mismatch 4xx re-classified as KindTransient
// because ClassifyError(err, nil) was a pure text-match fallback that
// did not include toolCallIdMismatchRe / contextLengthRe. The primary
// fix in executor_chat.go / executor_anthropic.go returns a typed
// *upstream.Error (so ClassifyError is never even called). This test
// pins the secondary fix in ClassifyError itself: even if a future code
// path re-wraps with fmt.Errorf, the kind is still recoverable from
// err.Error(). The patterns are model-agnostic — they cover OpenAI,
// Anthropic, MiniMax, deepseek, zhipu bodies alike.
func TestClassifyError_BodyDrivenKindsFromWrappedString(t *testing.T) {
	toolCallBodies := []string{
		// MiniMax code 2013 (the production incident)
		`{"error":{"code":2013,"message":"invalid params, tool result's tool id (call_function_poab3apjo8kn_1) not found","type":"invalid_request"}}`,
		// Anthropic-style tool_use_id
		`{"type":"error","error":{"type":"invalid_request_error","message":"tool_use_id not found"}}`,
		// OpenAI-style tool_call_id
		`{"error":{"message":"Invalid tool_call_id: call_xxx","type":"invalid_request_error"}}`,
		// Generic phrasing
		`tool call id call_abc123 not found`,
	}
	for _, body := range toolCallBodies {
		wrapped := fmt.Errorf("upstream 400: %s", body)
		got := ClassifyError(wrapped, nil)
		if got != KindToolCallIdMismatch {
			t.Errorf("ClassifyError(wrapped body %q) = %q, want %q — body-driven tool_call_id_mismatch must NOT regress to %q",
				body[:min(80, len(body))], got, KindToolCallIdMismatch, KindTransient)
		}
		if !IsClientBug(got) {
			t.Errorf("ClassifyError(wrapped body %q) = %q, expected IsClientBug=true so the credential is NOT cooled for a client bug",
				body[:min(80, len(body))], got)
		}
	}

	contextLengthBodies := []string{
		`{"error":{"message":"This model's maximum context length is 8192 tokens."}}`,
		`{"error":"prompt is too long"}`,
		`{"error":"input is too long"}`,
		`{"error":"上下文长度超出限制"}`,
		`{"error":"tokens exceed model limit"}`,
	}
	for _, body := range contextLengthBodies {
		wrapped := fmt.Errorf("upstream 400: %s", body)
		got := ClassifyError(wrapped, nil)
		if got != KindContextLength {
			t.Errorf("ClassifyError(wrapped body %q) = %q, want %q — body-driven context_length_exceeded must NOT regress to %q",
				body[:min(80, len(body))], got, KindContextLength, KindTransient)
		}
		if IsRetryable(got) {
			t.Errorf("ClassifyError(wrapped body %q) = %q, expected IsRetryable=false (context-length has its own trim+retry path in the executor)",
				body[:min(80, len(body))], got)
		}
	}
}

// 2026-07-03: explicit pin for the production incident. The exact body
// from the 184 production logs (326/325 cases) must classify correctly
// even when wrapped via fmt.Errorf.
func TestClassifyError_ProductionIncident_MinimaxM3_ToolCallIdMismatch(t *testing.T) {
	body := `{"error":{"code":2013,"message":"invalid params, tool result's tool id (call_function_poab3apjo8kn_1) not found","type":"invalid_request"}}`
	// This is the exact wrapping the executor used to emit before the
	// primary fix. It must still classify correctly (secondary fix).
	wrapped := fmt.Errorf("upstream 400: %s", body)
	got := ClassifyError(wrapped, nil)
	if got != KindToolCallIdMismatch {
		t.Fatalf("production regression: ClassifyError(MiniMax 2013 body wrapped in fmt.Errorf) = %q, want %q. "+
			"This is the bug that polluted request_logs.error_kind with 'transient' for 325 of 326 transient rows in 24h.",
			got, KindToolCallIdMismatch)
	}
}

// 2026-07-04 V20: test generic web-server 404 exclusion.
func TestClassifyErrorWithBody_GenericWeb404(t *testing.T) {
	// Generic Nginx 404
	kind := ClassifyErrorWithBody(404, []byte("404 Not Found\nnginx/1.18.0"))
	if kind == KindModelNotFound {
		t.Error("generic Nginx 404 should NOT be KindModelNotFound")
	}

	// Generic "page not found"
	kind = ClassifyErrorWithBody(404, []byte("<html><body><h1>404 page not found</h1></body></html>"))
	if kind == KindModelNotFound {
		t.Error("generic HTML 404 should NOT be KindModelNotFound")
	}

	// Generic "The page you requested was not found"
	kind = ClassifyErrorWithBody(404, []byte("The page you requested was not found"))
	if kind == KindModelNotFound {
		t.Error("generic web error message should NOT be KindModelNotFound")
	}

	// Real LLM provider model_not_found (should still match)
	kind = ClassifyErrorWithBody(404, []byte(`{"error": "model gpt-4-turbo is not found"}`))
	if kind != KindModelNotFound {
		t.Errorf("LLM provider model error should be KindModelNotFound, got %q", kind)
	}

	// Real provider with model name
	kind = ClassifyErrorWithBody(400, []byte(`{"code": 1002, "message": "no such model: glm-5.1"}`))
	if kind != KindModelNotFound {
		t.Errorf("provider 'no such model' should be KindModelNotFound, got %q", kind)
	}
}
