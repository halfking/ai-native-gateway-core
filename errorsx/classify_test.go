package errorsx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestKindConversion_IsValid ensures the KindConversion constant used
// by the audit-correlation error_kind taxonomy (2026-07-28 §5.8) is
// populated with the expected wire value.
func TestKindConversion_IsValid(t *testing.T) {
	if KindConversion == "" {
		t.Fatal("KindConversion must be non-empty")
	}
	if KindConversion != "conversion_error" {
		t.Errorf("KindConversion wire value changed: got %q want %q", KindConversion, "conversion_error")
	}
}

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

func TestClassifyError_EADDRNOTAVAIL(t *testing.T) {
	err := errors.New("connect EADDRNOTAVAIL 198.18.0.51:443 - Local (0.0.0.0:0): cannot assign requested address")
	kind := ClassifyError(err, nil)
	if kind != KindNetwork {
		t.Errorf("expected KindNetwork for EADDRNOTAVAIL, got %q", kind)
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

// TestClassifyErrorWithBody_503_NoAvailableChannel is the 2026-08-09 server-154
// incident regression. A OneAPI/new-api distributor answering 503 with
// "No available channel for model X under group Y" (plus its own
// `"code":"model_not_found"` label) was being classified as KindConcurrent —
// which IsRetryable, so the executor re-tried the SAME broken credential
// instead of failing over to a sibling distributor (62 occurrences/24h;
// provider 9271 accumulated 387 failed upstream attempts). It must classify as
// KindNoAvailableChannel: non-retryable, per-(credential,model), sibling
// failover.
func TestClassifyErrorWithBody_503_NoAvailableChannel(t *testing.T) {
	incidentBody := []byte(`{"error":{"message":"No available channel for model gpt-5.6-luna under group Codex-特价 (distributor)","type":"new_api_error","code":"model_not_found"}}`)
	if kind := ClassifyErrorWithBody(503, incidentBody); kind != KindNoAvailableChannel {
		t.Errorf("expected KindNoAvailableChannel for 503+no-available-channel body, got %q", kind)
	}

	// Core behavioral contract of the fix: the kind must NOT be retryable,
	// otherwise the same-credential retry loop that caused the incident is
	// preserved.
	if IsRetryable(KindNoAvailableChannel) {
		t.Error("KindNoAvailableChannel must NOT be retryable (would re-try the same broken credential)")
	}

	// "to serve model" phrasing variant.
	alt := []byte(`{"error":{"message":"No available channel to serve model gpt-5.6-luna","type":"new_api_error","code":"model_not_found"}}`)
	if kind := ClassifyErrorWithBody(503, alt); kind != KindNoAvailableChannel {
		t.Errorf("expected KindNoAvailableChannel for 'to serve model' variant, got %q", kind)
	}
}

// TestClassifyErrorWithBody_503_PlainOverloadStillConcurrent guards against
// over-broadening: a 503 whose body is a genuine overload message must STILL
// be KindConcurrent (the tuner.go concurrency ratchet is the correct response).
func TestClassifyErrorWithBody_503_PlainOverloadStillConcurrent(t *testing.T) {
	kind := ClassifyErrorWithBody(503, []byte(`{"error":{"message":"concurrent limit exceeded for this account","type":"rate_limit_error"}}`))
	if kind != KindConcurrent {
		t.Errorf("expected KindConcurrent for plain 503 overload, got %q", kind)
	}
}

// TestClassifyErrorWithBody_NoAvailableChannel_StatusGate verifies the 5xx gate:
// a 404 "no available channel" body must NOT be KindNoAvailableChannel (falls
// through to the transient path), and a 502 overload body must stay
// KindUpstreamOverloaded (never NoAvailableChannel).
func TestClassifyErrorWithBody_NoAvailableChannel_StatusGate(t *testing.T) {
	body := []byte(`No available channel for model gpt-5.6-luna under group Codex-特价`)
	if kind := ClassifyErrorWithBody(404, body); kind == KindNoAvailableChannel {
		t.Error("ClassifyErrorWithBody(404, no-available-channel) must NOT be KindNoAvailableChannel (5xx-only gate)")
	}
	overload := []byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"upstream_error"}}`)
	if kind := ClassifyErrorWithBody(502, overload); kind != KindUpstreamOverloaded {
		t.Errorf("expected KindUpstreamOverloaded for 502 overload, got %q", kind)
	}
}

// TestClassifyResponseBody_NoAvailableChannel ensures the body-only classifier
// agrees with ClassifyErrorWithBody on the same body (SSE error chunks,
// relayed body capture).
func TestClassifyResponseBody_NoAvailableChannel(t *testing.T) {
	incident := `{"error":{"message":"No available channel for model gpt-5.6-luna under group Codex-特价 (distributor)","type":"new_api_error","code":"model_not_found"}}`
	if got := ClassifyResponseBody(503, []byte(incident)); got != KindNoAvailableChannel {
		t.Errorf("ClassifyResponseBody(503, no-available-channel) = %q, want KindNoAvailableChannel", got)
	}
	if got := ClassifyResponseBody(429, []byte(incident)); got == KindNoAvailableChannel {
		t.Error("ClassifyResponseBody(429, no-available-channel) must NOT be KindNoAvailableChannel (5xx-only gate)")
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
		{KindContentFilter, false}, // content-determined, not retryable
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
		// 2026-08-05: 410 removed from protocol-4xx switch. A bare 410 with no
		// EOL/deprecation body now falls through to ClassifyResponseStatus →
		// KindTransient (retryable on a sibling credential). A 410 WITH an EOL
		// body is classified as KindModelDeprecated by the body-pattern path
		// (see TestClassifyErrorWithBody_ModelDeprecated).
		{"410_gone_no_body_now_transient", 410, `gone`, KindTransient},
		// 2026-07-08: 422 removed from protocol-4xx switch. A bare
		// "unprocessable entity" body does not match any known pattern
		// (modelNotFoundRe, unsupportedFeatureRe, contentFilterRe,
		// contextLengthRe), so it falls through to ClassifyResponseStatus
		// → default KindTransient.
		{"422_unprocessable_generic_now_transient", 422, `unprocessable entity`, KindTransient},
		// 422 with a content-filter body is KindContentFilter (not
		// KindUnsupportedFeature) — the body pattern takes priority.
		{"422_content_filter_new_sensitive", 422, `{"type":"error","error":{"type":"unprocessable_entity_error","message":"input new_sensitive (1026)","http_code":"422"}}`, KindContentFilter},
		// 422 with unsupported-feature body still KindUnsupportedFeature.
		{"422_unsupported_tools_body", 422, `{"error":{"message":"This model does not support tools","type":"invalid_request_error"}}`, KindUnsupportedFeature},
		{"400_unsupported_image_body", 400, `{"error":{"message":"Cannot read \"image.png\" (this model does not support image input). Inform the user.","type":"invalid_request_error"}}`, KindUnsupportedFeature},
		{"408_request_timeout_still_timeout", 408, `request timeout`, KindTimeout},
		{"401_unauthorized_still_auth", 401, `unauthorized`, KindAuth},
		// 2026-07-16 P0 fix: "quota exceeded" on 429 now maps to
		// KindQuotaPermanent (per budgetExceededRe match), not KindRateLimit.
		// Only a truly plain 429 (no quota/balance/budget text) stays rate_limit.
		{"429_quota_exceeded_now_permanent", 429, `quota exceeded`, KindQuotaPermanent},
		// 2026-07-21 P0 fix: a quota body that ALSO carries a reset
		// timestamp should route to KindQuotaPeriodic, not
		// KindQuotaPermanent. 智谱AI 1310 with "限额将在 ... 重置" is the
		// canonical example — it auto-recovers weekly/monthly, so the
		// credential must NOT be flagged permanently.
		{"429_zhipu_1310_with_reset_now_periodic", 429, `{"error":{"code":"1310","message":"您已达到每周/每月使用上限，您的限额将在 2026-07-19 21:32:20 重置。"}}`, KindQuotaPeriodic},
		{"429_anthropic_budget_exceeded_no_reset_still_permanent", 429, `{"error":{"message":"Organization balance insufficient","type":"rate_limit_error","code":"budget_exceeded"}}`, KindQuotaPermanent},
		{"429_quota_exceeded_with_reset_now_periodic", 429, `quota exceeded, will reset at 2026-08-01 00:00:00`, KindQuotaPeriodic},
		// 2026-08-07 P0 fix: 智码(zhima) 的配额用尽报文用 window_type 标注
		// 窗口语义（total/daily/weekly/monthly），不给显式 reset 时间戳。
		// 实测（154 生产 cred 34）：HTTP 429 {"error":"usage limit exceeded","window_type":"total"}
		// 旧逻辑落到 KindQuotaPermanent → recover_at=NULL → 永久卡死。但
		// window_type 表明这是会按周期重置的用量窗口，应走 KindQuotaPeriodic。
		{"429_zhima_window_type_total_now_periodic", 429, `{"error":"usage limit exceeded","window_type":"total"}`, KindQuotaPeriodic},
		{"429_window_type_daily_now_periodic", 429, `usage limit exceeded, window_type: "daily"`, KindQuotaPeriodic},
		// 2026-08-08 P0 fix: apiclaude.cc / 智码 / OneAPI-family relays
		// return HTTP 403 with body {"code":"INSUFFICIENT_BALANCE",
		// "message":"Insufficient account balance"} when the user's
		// account balance is exhausted. Previously classified as
		// KindAuth because the status gate was 429-only, surfacing the
		// misleading "Upstream credential API key invalid" message and
		// opening the circuit breaker with exponential backoff on a
		// healthy-looking key. Now correctly routed to KindQuotaPermanent.
		{"403_apiclaude_insufficient_balance_now_permanent", 403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, KindQuotaPermanent},
		{"403_insufficient_account_balance_now_permanent", 403, `{"error":{"message":"Insufficient account balance","type":"insufficient_credit"}}`, KindQuotaPermanent},
		{"402_payment_required_quota_now_permanent", 402, `{"error":{"message":"Your account balance is insufficient for this operation.","type":"insufficient_quota"}}`, KindQuotaPermanent},
		// Defensive: a bare 403 with no quota-suggesting body still
		// falls through to ClassifyResponseStatus → KindAuth (the
		// pre-fix behaviour for plain 403s).
		{"403_bare_no_body_still_auth", 403, `forbidden`, KindAuth},
		{"500_still_upstream_down", 500, `internal server error`, KindUpstreamDown},
		{"502_still_upstream_down", 502, `bad gateway`, KindUpstreamDown},
		{"503_still_concurrent", 503, `service unavailable`, KindConcurrent},
		// 2026-08-08: a 5xx whose BODY reports transient load is not the
		// same failure as a dead upstream. Observed on 154 against the
		// apiclaude.cc relay: 14 occurrences in 24h, every one logged as
		// err_kind=upstream_down because classification never looked at the
		// body. 500/502 now carry KindUpstreamOverloaded so the cooling
		// window, breaker policy, and dashboards can tell "busy" from
		// "down"; routing behaviour stays identical to KindUpstreamDown.
		{"502_overload_body_now_overloaded", 502, `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"upstream_error"}}`, KindUpstreamOverloaded},
		{"500_overload_body_now_overloaded", 500, `{"error":{"message":"server is overloaded, please try again later"}}`, KindUpstreamOverloaded},
		// Scope boundary: 503/529 keep KindConcurrent even with the same
		// body. Those statuses ARE the upstream stating a concurrency
		// limit, and credentialhealth/tuner.go deliberately ratchets
		// concurrency_limit_auto down on KindConcurrent. Widening the new
		// kind to cover them would silently disable that ratchet.
		{"503_overload_body_still_concurrent", 503, `{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`, KindConcurrent},
		{"529_overload_body_still_concurrent", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded, try again later"}}`, KindConcurrent},
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
				tc.want == KindUpstreamOverloaded ||
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

func TestClassifyError_Code2013ContextWindowIsNotToolMismatch(t *testing.T) {
	body := `{"type":"error","error":{"type":"bad_request_error","message":"invalid params, context window exceeds limit (2013)","http_code":"400"}}`
	got := ClassifyError(fmt.Errorf("upstream 400: %s", body), nil)
	if got != KindContextLength {
		t.Fatalf("ClassifyError(context-window code 2013) = %q, want %q", got, KindContextLength)
	}
	if IsClientBug(got) {
		t.Fatal("context-window exhaustion must not be treated as a client tool-call bug")
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

// 2026-07-11: gpt-5.6-* multi-turn tool-call error must classify as
// KindToolCallIdMismatch (a client bug), NOT KindTransient. Classifying
// as transient causes the credential to be cooled for 5 minutes after
// 3 failures, blocking all subsequent requests with "unknown" reason.
func TestClassifyError_GPT56FunctionCallOutputMismatch(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			"openai-400-item_reference",
			`{"error":{"message":"function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation requires previous_response_id or replayable tool-call context","type":"invalid_request_error"}}`,
		},
		{
			"openai-continuation-required",
			`{"error":{"message":"continuation requires previous_response_id or replayable tool-call context","type":"invalid_request_error"}}`,
		},
		{
			"wrapped-fmt-errorf",
			`function_call_output requires item_reference ids matching each call_id`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Body path
			if got := ClassifyErrorWithBody(400, []byte(tc.body)); got != KindToolCallIdMismatch {
				t.Errorf("ClassifyErrorWithBody(400, %q) = %q, want %q", tc.body[:min(60, len(tc.body))], got, KindToolCallIdMismatch)
			}
			// Wrapped path (executor re-wraps via fmt.Errorf)
			wrapped := fmt.Errorf("upstream 400: %s", tc.body)
			if got := ClassifyError(wrapped, nil); got != KindToolCallIdMismatch {
				t.Errorf("ClassifyError(wrapped %q) = %q, want %q", tc.body[:min(60, len(tc.body))], got, KindToolCallIdMismatch)
			}
		})
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

// 2026-07-08: content-moderation / safety-policy rejections must be
// classified as KindContentFilter — not KindUnsupportedFeature (which
// would be retried across all candidates) and not KindTransient (which
// is also retryable). The credential is healthy; the content is the
// problem.
func TestClassifyErrorWithBody_ContentFilter(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			"minimax-422-new-sensitive",
			422,
			`{"type":"error","error":{"type":"unprocessable_entity_error","message":"input new_sensitive (1026)","http_code":"422"},"request_id":"069d1dd94707745c39dc52c1898000ac"}`,
		},
		{
			// MiniMax content-moderation code 1027 (another new_sensitive code).
			// Note: code 2013 overlaps with toolCallIdMismatchRe (which
			// runs first) and is a genuine tool-call error, NOT content
			// moderation — so we don't use 2013 here.
			"minimax-422-sensitive-code",
			422,
			`{"type":"error","error":{"type":"unprocessable_entity_error","message":"new_sensitive (1027)","http_code":"422"}}`,
		},
		{
			"openai-400-content-filter",
			400,
			`{"error":{"message":"This request has been blocked by our content filter","type":"content_filter"}}`,
		},
		{
			"openai-400-content-policy",
			400,
			`{"error":{"message":"Your request was rejected due to content policy","type":"invalid_request_error"}}`,
		},
		{
			"anthropic-403-content-moderation",
			403,
			`{"type":"error","error":{"type":"content_moderation_error","message":"Request was rejected by content moderation"}}`,
		},
		{
			"generic-400-policy-violation",
			400,
			`{"error":"policy_violation: prohibited content detected"}`,
		},
		{
			"cjk-422-content-sensitive",
			422,
			`{"error":"包含敏感词，请修改后重试"}`,
		},
		{
			"cjk-422-content-illegal",
			422,
			`{"error":"内容违规，无法处理"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if kind != KindContentFilter {
				t.Errorf("ClassifyErrorWithBody(%d, ...) = %q, want %q",
					tc.status, kind, KindContentFilter)
			}
			// Content filter is NOT retryable — same content will
			// be rejected on every candidate.
			if IsRetryable(kind) {
				t.Errorf("IsRetryable(%q) = true, want false", kind)
			}
			// Content filter is NOT a client bug — it retries next
			// candidate (same content, same rejection), which is
			// wasteful. Content filter uses its own short-circuit.
			if IsClientBug(kind) {
				t.Errorf("IsClientBug(%q) = true, want false — content_filter must NOT be IsClientBug", kind)
			}
		})
	}
}

// Negative tests: bodies that should NOT be classified as content filter.
func TestClassifyErrorWithBody_ContentFilter_Negative(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		// "case-sensitive" in a normal error should NOT match
		{"case-sensitive-phrase", 400, `{"error":"comparison must be case sensitive"}`},
		// "sensitive" alone (no code, no new_sensitive) should NOT match
		{"sensitive-alone", 400, `{"error":"this is a sensitive topic"}`},
		// 200 body with "sensitive" (status not in 400/403/422/451 gate)
		{"200-sensitive-ignored", 200, `{"result":"input new_sensitive (1026)"}`},
		// Bare "filter" should NOT match (no "content" prefix)
		{"filter-alone", 400, `{"error":"filter function not found"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if kind == KindContentFilter {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = KindContentFilter, want something else — false positive",
					tc.status, tc.body)
			}
		})
	}
}

func TestIsContentFilter(t *testing.T) {
	if !IsContentFilter(KindContentFilter) {
		t.Error("IsContentFilter(KindContentFilter) = false, want true")
	}
	if IsContentFilter(KindUnsupportedFeature) {
		t.Error("IsContentFilter(KindUnsupportedFeature) = true, want false")
	}
	if IsContentFilter(KindTransient) {
		t.Error("IsContentFilter(KindTransient) = true, want false")
	}
	if IsContentFilter(ErrorKind("")) {
		t.Error("IsContentFilter('') = true, want false")
	}
}

// TestClassifyErrorWithBody_ModelDeprecated verifies that an upstream
// "model permanently removed / end-of-life" body is classified as
// KindModelDeprecated, NOT KindUnsupportedFeature (the pre-2026-08-05
// behaviour that caused the executor to hammer a dead model 22×).
//
// Regression context: request d679b7e7285a9bbe5fce953cd6c935a8 hit NVIDIA NIM
// provider_id=18 / minimaxai/minimax-m2.7, which returned HTTP 410:
//
//	{"detail":"The model 'minimaxai/minimax-m2.7' has reached its end of life
//	 on 2026-07-27T00:00:00Z and is no longer available."}
//
// The gateway surfaced this as "unsupported_feature" (HTTP 400) and retried
// the same dead credential for 20s because IsClientBug(KindUnsupportedFeature)
// short-circuits cross-credential failover.
func TestClassifyErrorWithBody_ModelDeprecated(t *testing.T) {
	positive := []struct {
		name   string
		status int
		body   string
	}{
		// Real production body (NVIDIA NIM, the request that triggered this fix).
		{"nvidia_nim_410_eol", 410, `{"type":"about:blank","title":"Gone","status":410,"detail":"The model 'minimaxai/minimax-m2.7' has reached its end of life on 2026-07-27T00:00:00Z and is no longer available."}`},
		// OpenAI-style deprecation advisory on 404.
		{"openai_404_deprecated", 404, `{"error":{"message":"The model 'gpt-4' has been deprecated, please use gpt-4o","type":"invalid_request_error"}}`},
		// 422 variant.
		{"422_no_longer_available", 422, `{"error":"model foo is no longer available"}`},
		// 400 variant.
		{"400_retired", 400, `{"error":{"message":"model bar has been retired"}}`},
		// Bare phrase, no JSON wrapper.
		{"410_bare_end_of_life", 410, `this model has reached end of life`},
		// CJK variant.
		{"410_cjk_offline", 410, `该模型已下线`},
	}
	for _, tc := range positive {
		t.Run("pos/"+tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != KindModelDeprecated {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want KindModelDeprecated",
					tc.status, tc.body, got)
			}
			// A permanently-removed model must NOT be a client bug (else the
			// executor skips failover) and must NOT be retryable.
			if IsClientBug(got) {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = IsClientBug true, want false (must failover)",
					tc.status, tc.body)
			}
			if IsRetryable(got) {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = IsRetryable true, want false (model is gone)",
					tc.status, tc.body)
			}
		})
	}

	negative := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		// 410 with a non-EOL body must NOT be model_deprecated — falls through
		// to KindTransient now that 410 is out of the protocol-4xx switch.
		{"410_no_eol_body_transient", 410, `gone`, KindTransient},
		// 200 OK advisory is not a model_deprecated (status out of gate);
		// ClassifyErrorWithBody falls through to KindTransient for <400.
		{"200_deprecation_advice_transient", 200, `model glm-4 has been deprecated`, KindTransient},
		// 502 with EOL-ish body is connectivity, not deprecation (status out of gate).
		{"502_with_retired_body_upstream_down", 502, `model has been retired`, KindUpstreamDown},
	}
	for _, tc := range negative {
		t.Run("neg/"+tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyResponseBody_ModelDeprecated mirrors the above for the
// SSE-body classifier path (used by executor_chat.go / executor_anthropic.go
// to decide whether to construct a modelNotFoundError).
func TestClassifyResponseBody_ModelDeprecated(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"nvidia_410", 410, `reached its end of life`, KindModelDeprecated},
		{"openai_404", 404, `has been deprecated`, KindModelDeprecated},
		{"422_no_longer", 422, `no longer available`, KindModelDeprecated},
		{"400_retired", 400, `has been retired`, KindModelDeprecated},
		// 200 advisory → empty (not classified).
		{"200_not_classified", 200, `has been deprecated`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyResponseBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("ClassifyResponseBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyError_ModelDeprecated_ErrPath verifies the err.Error()-based
// classifier (used when a typed *upstream.Error is re-wrapped via fmt.Errorf
// and the body text survives in the message).
func TestClassifyError_ModelDeprecated_ErrPath(t *testing.T) {
	cases := []string{
		`upstream 410: The model 'minimaxai/minimax-m2.7' has reached its end of life and is no longer available`,
		`upstream 404: model gpt-4 has been deprecated`,
	}
	for _, msg := range cases {
		kind := ClassifyError(errors.New(msg), nil)
		if kind != KindModelDeprecated {
			t.Errorf("ClassifyError(%q) = %q, want KindModelDeprecated", msg, kind)
		}
	}
}

// TestQuotaResetClassification tests the quotaResetsRe pattern matching
// for periodic quota exhaustion with recovery timestamps.
//
// 2026-08-07 audit fix: 补充正则表达式边界测试，覆盖中文"重置"模式
// 和 window_type 场景（P2-3 测试覆盖缺口）。
func TestQuotaResetClassification(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		expected ErrorKind
	}{
		{
			name:     "zhipu periodic quota with reset timestamp",
			status:   429,
			body:     `{"error":"usage limit exceeded，您的限额将在 2026-08-10 12:00:00 重置。"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "zhima window_type total",
			status:   429,
			body:     `{"error":"usage limit exceeded","window_type":"total"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "zhima window_type daily",
			status:   429,
			body:     `{"error":"usage limit exceeded","window_type":"daily"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "permanent balance insufficient without reset",
			status:   429,
			body:     `{"error":"balance insufficient"}`,
			expected: KindQuotaPermanent,
		},
		{
			name:     "periodic with English reset and quota exceeded",
			status:   429,
			body:     `{"error":"usage limit exceeded. Will reset at 2026-08-10 00:00:00"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "periodic with retry_after",
			status:   429,
			body:     `{"error":"Quota exceeded. Retry after 2026-08-10T12:00:00Z"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "permanent budget_exceeded without reset hint",
			status:   429,
			body:     `{"error":"budget exceeded"}`,
			expected: KindQuotaPermanent,
		},
		{
			name:     "chinese quota exhausted with reset",
			status:   429,
			body:     `配额用尽，将在 2026-08-15 重置`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "ISO timestamp format with quota exceeded",
			status:   429,
			body:     `{"error":"usage limit exceeded","reset":"2026-08-10 15:30:45"}`,
			expected: KindQuotaPeriodic,
		},
		{
			name:     "anthropic budget_exceeded with reset",
			status:   429,
			body:     `{"error":{"message":"Organization balance insufficient, will reset at 2026-08-10","type":"rate_limit_error","code":"budget_exceeded"}}`,
			expected: KindQuotaPeriodic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind := ClassifyErrorWithBody(tt.status, []byte(tt.body))
			if kind != tt.expected {
				t.Errorf("ClassifyErrorWithBody() = %v, want %v\nBody: %s",
					kind, tt.expected, tt.body)
			}
		})
	}
}

// 2026-08-08 P0 fix (defense-in-depth): when an upstream.Error is
// re-wrapped via fmt.Errorf("upstream %d: %s", status, body), the body
// text is collapsed into err.Error() and ClassifyError (the no-body
// variant) is called. budgetExceededRe should now fire on that text so
// balance-exhaustion is not classified as KindTransient and silently
// retried. This regression test pins the wrapped-err path; previously
// this case returned KindTransient, surfacing the wrong circuit breaker
// policy and confusing operators with "transient" logs for what was
// actually a balance issue.
func TestClassifyError_WrappedBudgetExceeded(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{
			name: "apiclaude.cc INS-403 wrapped",
			err:  fmt.Errorf(`[auth] upstream 403: {"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}: <nil>`),
			want: KindQuotaPermanent,
		},
		{
			name: "anthropic budget_exceeded wrapped",
			err:  fmt.Errorf(`[quota] upstream 429: {"error":{"message":"Organization balance insufficient","type":"rate_limit_error","code":"budget_exceeded"}}: <nil>`),
			want: KindQuotaPermanent,
		},
		{
			name: "chinese quota exhausted wrapped",
			err:  fmt.Errorf(`[quota] upstream 429: {"error":"您的余额已用尽，请充值。"}`),
			want: KindQuotaPermanent,
		},
		{
			name: "balance insufficient wrapped",
			err:  fmt.Errorf(`[auth] upstream 403: balance insufficient for group`),
			want: KindQuotaPermanent,
		},
		{
			// Without a body that mentions balance/quota/credit, a
			// generic auth error falls through to KindTransient. The
			// upstream.Error path (where the typed Kind field is set)
			// still routes this correctly; this defense-in-depth
			// test only pins the budget patterns.
			name: "non-budget auth error still transient (no body signal)",
			err:  fmt.Errorf(`[auth] upstream 401: invalid api key`),
			want: KindTransient,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err, nil)
			if got != tc.want {
				t.Errorf("ClassifyError(%q) = %q, want %q",
					tc.err.Error(), got, tc.want)
			}
		})
	}
}
