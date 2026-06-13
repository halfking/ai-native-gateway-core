package errorsx

import (
	"context"
	"errors"
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
		kind := ClassifyResponseBody([]byte(tc.body))
		if kind != tc.kind {
			t.Errorf("for %q expected %q, got %q", tc.body, tc.kind, kind)
		}
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
	if !IsClientBug(KindModelNotFound) {
		t.Error("KindModelNotFound must be flagged as a client bug")
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
