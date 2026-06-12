package errorsx

import "testing"

func TestClassifyErrorWithBody_ContextLength(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"openai-400", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens."}}`, KindContextLength},
		{"anthropic-400", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`, KindContextLength},
		{"minimax-400", 400, `{"error":{"code":"context_length_exceeded","message":"max_tokens exceed"}}`, KindContextLength},
		{"deepseek-400", 400, `{"error":{"message":"context window exceeded"}}`, KindContextLength},
		{"zhipu-400-cjk", 400, `{"error":{"message":"上下文长度超出限制"}}`, KindContextLength},
		// 413 with a context-length-shaped body
		{"413-with-context-body", 413, `{"error":{"message":"context_length_exceeded"}}`, KindContextLength},
		{"422-status", 422, `{"error":"context_length_exceeded"}`, KindContextLength},
		// Negative cases
		// "rate limit exceeded" matches the concurrentOverloadRe pattern
		// ("limit" + "exceed") before reaching the status-code branch, so
		// it gets KindTransient — this is pre-existing behaviour, not
		// something the context-length addition changed.
		{"400-not-context", 400, `{"error":{"message":"rate limit exceeded"}}`, KindTransient},
		{"401", 401, `{"error":"unauthorized"}`, KindAuth},
		// 5xx status codes always map to KindUpstreamDown, regardless of
		// the body. 413 with a non-context body falls through to the
		// generic 4xx path which is KindTransient.
		{"500", 500, `internal error`, KindUpstreamDown},
		// Status 400 but body does NOT look like context length
		{"400-generic", 400, `{"error":{"message":"bad request"}}`, KindTransient},
		{"413-generic", 413, `{"error":"payload too large"}`, KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("status=%d body=%q: got %s, want %s", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestIsContextLength(t *testing.T) {
	if !IsContextLength(KindContextLength) {
		t.Fatal("IsContextLength(KindContextLength) should be true")
	}
	if IsContextLength(KindTransient) {
		t.Fatal("IsContextLength(KindTransient) should be false")
	}
}

func TestIsRetryable_ContextLengthNotRetryable(t *testing.T) {
	// Context length is intentionally NOT in the default IsRetryable list.
	// The executor handles it via a dedicated trim+retry path. If we add
	// it here, the generic retry loop would re-send the same oversized
	// body to a different candidate and bounce off the same limit.
	if IsRetryable(KindContextLength) {
		t.Fatal("KindContextLength must not be in IsRetryable's default list; the executor has a dedicated path")
	}
}
