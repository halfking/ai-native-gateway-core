package errorsx

import "testing"

func TestParseContextLimitResultEvidence(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		evidence ContextLimitEvidence
		limit    int
	}{
		{
			name:     "authoritative limit with usage",
			body:     `{"error":{"message":"maximum context length is 262144 tokens; your messages resulted in 263247 tokens"}}`,
			evidence: ContextLimitAuthoritative,
			limit:    262144,
		},
		{
			name:     "observed usage only",
			body:     `{"error":{"message":"your messages resulted in 263247 tokens"}}`,
			evidence: ContextLimitObservedUsage,
			limit:    263247,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseContextLimitResult(tt.body)
			if !got.Found || got.Limit != tt.limit || got.Evidence != tt.evidence {
				t.Fatalf("got %+v, want limit=%d evidence=%q", got, tt.limit, tt.evidence)
			}
		})
	}
}

func TestParseContextLimitFromError(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantLimit int
		wantFound bool
	}{
		{
			name:      "OpenAI standard format",
			body:      `{"error":{"message":"This model's maximum context length is 262144 tokens. However, your messages resulted in 263247 tokens. Please reduce the length of the messages."}}`,
			wantLimit: 262144,
			wantFound: true,
		},
		{
			name:      "Simple context window format",
			body:      `{"error":"context window is 128000"}`,
			wantLimit: 128000,
			wantFound: true,
		},
		{
			name:      "Maximum context length variant",
			body:      `{"error":"maximum context length 32768"}`,
			wantLimit: 32768,
			wantFound: true,
		},
		{
			name:      "Chinese format",
			body:      `{"error":"上下文长度限制为 32768 个token"}`,
			wantLimit: 32768,
			wantFound: true,
		},
		{
			name:      "Chinese format without spaces",
			body:      `{"error":"Context限制为8192"}`,
			wantLimit: 8192,
			wantFound: true,
		},
		{
			name:      "Model's maximum context is format",
			body:      `{"error":"This model's maximum context is 200000 tokens"}`,
			wantLimit: 200000,
			wantFound: true,
		},
		{
			name:      "System maximum context format",
			body:      `{"error":"System maximum context length is 100000 tokens"}`,
			wantLimit: 100000,
			wantFound: true,
		},
		{
			name:      "Only 'resulted in' - use as fallback",
			body:      `{"error":"Your messages resulted in 263247 tokens."}`,
			wantLimit: 263247,
			wantFound: true,
		},
		{
			name:      "Both maximum and resulted - prefer maximum",
			body:      `{"error":"This model's maximum context length is 262144 tokens. However, your messages resulted in 263247 tokens."}`,
			wantLimit: 262144,
			wantFound: true,
		},
		{
			name:      "No context limit in error",
			body:      `{"error":"Invalid API key"}`,
			wantLimit: 0,
			wantFound: false,
		},
		{
			name:      "Generic error message",
			body:      `{"error":"Something went wrong"}`,
			wantLimit: 0,
			wantFound: false,
		},
		{
			name:      "Empty body",
			body:      "",
			wantLimit: 0,
			wantFound: false,
		},
		{
			name:      "Case insensitive matching",
			body:      `{"error":"MAXIMUM CONTEXT LENGTH IS 50000 TOKENS"}`,
			wantLimit: 50000,
			wantFound: true,
		},
		{
			name:      "Context window with extra words",
			body:      `{"error":"The context window for this model is 16384 tokens"}`,
			wantLimit: 16384,
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLimit, gotFound := ParseContextLimitFromError(tt.body)
			if gotLimit != tt.wantLimit {
				t.Errorf("ParseContextLimitFromError() limit = %v, want %v", gotLimit, tt.wantLimit)
			}
			if gotFound != tt.wantFound {
				t.Errorf("ParseContextLimitFromError() found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}

// TestParseContextLimitFromError_RealWorldCases tests with actual error messages
// from production logs.
func TestParseContextLimitFromError_RealWorldCases(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantLimit int
		wantFound bool
	}{
		{
			name: "Minimax-m3 2026-09-01 case",
			body: `{
				"error": {
					"message": "This model's maximum context length is 262144 tokens. However, your messages resulted in 263247 tokens. Please reduce the length of the messages."
				}
			}`,
			wantLimit: 262144,
			wantFound: true,
		},
		{
			name: "OpenAI gpt-4 context exceeded",
			body: `{
				"error": {
					"message": "This model's maximum context length is 8192 tokens. However, you requested 9000 tokens (8000 in the messages, 1000 in the completion). Please reduce the length of the messages or completion.",
					"type": "invalid_request_error",
					"param": "messages",
					"code": "context_length_exceeded"
				}
			}`,
			wantLimit: 8192,
			wantFound: true,
		},
		{
			name: "Anthropic Claude context window",
			body: `{
				"error": {
					"type": "invalid_request_error",
					"message": "Your request exceeded the context window of 200000 tokens"
				}
			}`,
			wantLimit: 200000,
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLimit, gotFound := ParseContextLimitFromError(tt.body)
			if gotLimit != tt.wantLimit {
				t.Errorf("ParseContextLimitFromError() limit = %v, want %v", gotLimit, tt.wantLimit)
			}
			if gotFound != tt.wantFound {
				t.Errorf("ParseContextLimitFromError() found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}
