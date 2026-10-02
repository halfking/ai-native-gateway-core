package executors

import "testing"

// Wave4-D2 (2026-09-22) pinning tests for the executor-side empty-response
// classification (executor_chat.go failover gate + executor_anthropic.go
// terminal-502 classifier), now delegating to internal/emptyoutcome.
// The usage-only case is the load-bearing pin: token accounting without any
// assistant content MUST classify empty so the candidate loop fails over.

func TestIsNonStreamEmptyResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "usage-only chat body is empty (usage never counts as output)",
			body: `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":0}}`,
			want: true,
		},
		{
			name: "missing choices entirely is empty",
			body: `{"id":"x","object":"chat.completion"}`,
			want: true,
		},
		{
			name: "choices array empty is empty",
			body: `{"choices":[]}`,
			want: true,
		},
		{
			name: "text content is not empty",
			body: `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			want: false,
		},
		{
			name: "reasoning-only content is not empty",
			body: `{"choices":[{"message":{"reasoning_content":"thinking...","content":""}}]}`,
			want: false,
		},
		{
			name: "tool call is not empty",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
			want: false,
		},
		{
			name: "multi-choice: second choice carrying output is not empty",
			body: `{"choices":[{"message":{"content":""}},{"message":{"content":"second"}}]}`,
			want: false,
		},
		{
			name: "malformed JSON is not empty (converter owns attribution)",
			body: `{"choices":[`,
			want: false,
		},
		{
			name: "empty body is not empty (caller decides)",
			body: ``,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonStreamEmptyResponse([]byte(tt.body)); got != tt.want {
				t.Fatalf("isNonStreamEmptyResponse(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestIsEmptyAnthropicMessagesResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "usage-only message envelope is empty",
			body: `{"type":"message","role":"assistant","content":[{"type":"text","text":""}],"usage":{"input_tokens":9,"output_tokens":0}}`,
			want: true,
		},
		{
			name: "message without content key is empty",
			body: `{"type":"message","role":"assistant"}`,
			want: true,
		},
		{
			name: "tool_use block is not empty even with empty input",
			body: `{"type":"message","content":[{"type":"tool_use","id":"toolu_1","name":"f","input":{}}]}`,
			want: false,
		},
		{
			name: "signed thinking block is not empty",
			body: `{"type":"message","content":[{"type":"thinking","thinking":"","signature":"sig"}]}`,
			want: false,
		},
		{
			name: "foreign envelope type is not judged here",
			body: `{"type":"error","content":[]}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEmptyAnthropicMessagesResponse([]byte(tt.body)); got != tt.want {
				t.Fatalf("isEmptyAnthropicMessagesResponse(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
