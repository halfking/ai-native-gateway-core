package streaming

import "testing"

// TestChunkHasContent_Cases covers the chunkHasContent helper that
// drives the empty-stream content-gate (2026-07-15). Empty / role-only
// / usage-only chunks must return false; content / reasoning / tool
// calls / audio chunks must return true; malformed JSON must return
// false (gate keeps buffering).
func TestChunkHasContent_Cases(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "empty string",
			body: "",
			want: false,
		},
		{
			name: "[DONE] sentinel",
			body: "[DONE]",
			want: false,
		},
		{
			name: "role-only first chunk",
			body: `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			want: false,
		},
		{
			name: "empty choices (NIM 13% failure mode)",
			body: `{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1700000000,"model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":null}]}`,
			want: false,
		},
		{
			name: "content delta",
			body: `{"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000000,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
			want: true,
		},
		{
			name: "reasoning_content delta (deepseek-r1 etc.)",
			body: `{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`,
			want: true,
		},
		{
			name: "tool_calls delta",
			body: `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function"}]}}]}`,
			want: true,
		},
		{
			name: "audio delta (OpenAI Realtime)",
			body: `{"choices":[{"index":0,"delta":{"audio":{"data":"AAAA","transcript":""}}}]}`,
			want: true,
		},
		{
			name: "empty audio delta",
			body: `{"choices":[{"index":0,"delta":{"audio":{"data":"","transcript":""}}}]}`,
			want: false,
		},
		{
			name: "malformed JSON",
			body: `not-json-at-all`,
			want: false,
		},
		{
			name: "missing choices (NIM upstream edge case)",
			body: `{"id":"chatcmpl-4","model":"glm-5.2","choices":[]}`,
			want: false,
		},
		{
			name: "usage-only chunk (no choices)",
			body: `{"id":"chatcmpl-5","model":"glm-5.2","usage":{"prompt_tokens":10,"completion_tokens":0}}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkHasContent(tc.body)
			if got != tc.want {
				t.Errorf("chunkHasContent(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestChunkHasContent_FullStream verifies the gate would recognise the
// canonical NIM empty-stream pattern: one or more empty chunks followed
// by [DONE]. All three empty chunks must return false so the gate
// continues buffering and finally returns Resumable=true.
func TestChunkHasContent_FullStream(t *testing.T) {
	nimEmptyStream := []string{
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"z-ai/glm-5.2","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"z-ai/glm-5.2","choices":[{"index":0,"delta":{}}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"z-ai/glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}
	for i, body := range nimEmptyStream {
		if chunkHasContent(body) {
			t.Fatalf("chunk #%d should be classified as empty: %s", i, body)
		}
	}
}