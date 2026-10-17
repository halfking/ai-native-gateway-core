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
			// r1029 (2026-10-17): was "reasoning-only content is not empty"
			// (want=false). The 154 evidence — glm-5.3, max_tokens=8 consumed
			// entirely by reasoning_content, content never emitted,
			// finish_reason=length (request
			// 623e36495abf1de578592fa90c78417a) — shows reasoning-only is
			// exactly the shape that reached the client as an empty 200.
			name: "reasoning-only content is empty",
			body: `{"choices":[{"message":{"reasoning_content":"thinking...","content":""}}]}`,
			want: true,
		},
		{
			// Reverse guard: reasoning alongside a real answer must NOT be
			// failed over — otherwise this fix trades one fault for another.
			name: "reasoning alongside content is not empty",
			body: `{"choices":[{"message":{"reasoning_content":"thinking...","content":"answer"}}]}`,
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
			// r1029 (2026-10-17): was "signed thinking block is not empty".
			// A signature terminates the thinking block; it is not the answer.
			// Same 154 failure mode as the chat legs.
			name: "signed thinking block is empty",
			body: `{"type":"message","content":[{"type":"thinking","thinking":"","signature":"sig"}]}`,
			want: true,
		},
		{
			// Reverse guard: a signed thinking block preceding real text still
			// counts as a non-empty answer.
			name: "signed thinking followed by text is not empty",
			body: `{"type":"message","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"pong"}]}`,
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
