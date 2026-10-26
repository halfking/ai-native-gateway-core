package streaming

import "testing"

func TestIsEmptyUpstreamChatResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "nil body is not empty (handled upstream)",
			body: ``,
			want: false,
		},
		{
			name: "invalid json is not empty",
			body: `not json`,
			want: false,
		},
		{
			name: "no choices field is empty",
			body: `{"id":"chatcmpl-1","model":"gpt-4"}`,
			want: true,
		},
		{
			name: "empty choices array is empty",
			body: `{"choices":[]}`,
			want: true,
		},
		{
			name: "choice without finish_reason and without content is empty",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":""}}]}`,
			want: true,
		},
		{
			name: "choice with finish_reason but no content is empty",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":""}}]}`,
			want: true,
		},
		{
			name: "choice with content but no finish_reason is NOT empty",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`,
			want: false,
		},
		{
			name: "normal response with content and finish_reason is not empty",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hello world"}}]}`,
			want: false,
		},
		{
			// r1029 (2026-10-17): was "…is not empty". Reasoning-only with a
			// finish_reason is the 154 glm-5.3 shape (max_tokens consumed by
			// deliberation, content never emitted) — it must fail over.
			name: "response with reasoning_content and finish_reason is empty",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","reasoning_content":"thinking..."}}]}`,
			want: true,
		},
		{
			// Reverse guard: reasoning beside real content still passes.
			name: "response with reasoning_content alongside content is not empty",
			body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","reasoning_content":"thinking...","content":"answer"}}]}`,
			want: false,
		},
		{
			name: "response with tool_calls and finish_reason is not empty",
			body: `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]}}]}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isEmptyUpstreamChatResponse([]byte(tt.body))
			if got != tt.want {
				t.Errorf("isEmptyUpstreamChatResponse(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
