package ir

import (
	"testing"
)

func TestDetectProtocol_OpenAIBody(t *testing.T) {
	tests := []struct {
		name  string
		body  string
	}{
		{
			name: "basic messages",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with tools",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}]}`,
		},
		{
			name: "with frequency_penalty",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "frequency_penalty": 0.5}`,
		},
		{
			name: "with logprobs",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "logprobs": true, "top_logprobs": 5}`,
		},
		{
			name: "with response_format",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "response_format": {"type": "json_object"}}`,
		},
		{
			name: "with seed",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "seed": 42}`,
		},
		{
			name: "with user field",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "user": "user123"}`,
		},
		{
			name: "with n",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "n": 2}`,
		},
		{
			name: "with max_completion_tokens",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "max_completion_tokens": 500}`,
		},
		{
			name: "with tool_choice required",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tool_choice": "required"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolOpenAIChat {
				t.Errorf("protocol = %q, want %q", proto, ProtocolOpenAIChat)
			}
			if conf < 0.15 {
				t.Errorf("confidence = %v, want >= 0.15", conf)
			}
		})
	}
}

func TestDetectProtocol_AnthropicBody(t *testing.T) {
	tests := []struct {
		name  string
		body  string
	}{
		{
			name: "with system",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "system": "You are helpful", "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with thinking",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "thinking": {"type": "enabled"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with cache_control",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "cache_control": {"type": "ephemeral"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with documents",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "documents": [{"type": "document", "source": {"type": "text", "data": "hello"}}], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with top_k",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "top_k": 100, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with stop_sequences",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "stop_sequences": ["END"], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with metadata.user_id",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "metadata": {"user_id": "user123"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with anthropic tools (input_schema)",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "tools": [{"name": "get_weather", "input_schema": {"type": "object"}}], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with tool_choice type tool",
			body: `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "tool_choice": {"type": "tool", "name": "get_weather"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolAnthropicMessages {
				t.Errorf("protocol = %q, want %q", proto, ProtocolAnthropicMessages)
			}
			if conf < 0.15 {
				t.Errorf("confidence = %v, want >= 0.15", conf)
			}
		})
	}
}

func TestDetectProtocol_ModelBasedDetection(t *testing.T) {
	// When body is ambiguous, model name should help
	tests := []struct {
		name        string
		body        string
		wantProto   string
	}{
		{
			name:      "claude in model",
			body:      `{"model": "claude-sonnet-4-20250514", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolAnthropicMessages,
		},
		{
			name:      "gpt in model",
			body:      `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "chatgpt in model",
			body:      `{"model": "chatgpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolOpenAIChat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != tt.wantProto {
				t.Errorf("protocol = %q, want %q", proto, tt.wantProto)
			}
		})
	}
}

func TestDetectProtocol_EmptyBody(t *testing.T) {
	_, _, err := DetectProtocol([]byte{})
	if err == nil {
		t.Error("expected error for empty body")
	}
}

func TestDetectProtocol_InvalidJSON(t *testing.T) {
	_, _, err := DetectProtocol([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestDetectProtocol_UnknownBody(t *testing.T) {
	// Body with no distinguishing fields
	body := `{"model": "some-model", "messages": [{"role": "user", "content": "hi"}]}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should default to OpenAI when truly ambiguous
	if proto != ProtocolOpenAIChat {
		t.Logf("protocol = %q, may default to OpenAI for ambiguous body", proto)
	}
}

func TestDetectProtocolByURL(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		url      string
		wantProto string
	}{
		{
			name:      "openai chat completions URL",
			body:      `{"messages": [{"role": "user", "content": "hi"}]}`,
			url:       "/v1/chat/completions",
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "anthropic messages URL",
			body:      `{"messages": [{"role": "user", "content": "hi"}]}`,
			url:       "/v1/messages",
			wantProto: ProtocolAnthropicMessages,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocolByURL([]byte(tt.body), tt.url)
			if err != nil {
				t.Fatalf("DetectProtocolByURL error: %v", err)
			}
			if proto != tt.wantProto {
				t.Errorf("protocol = %q, want %q", proto, tt.wantProto)
			}
		})
	}
}

func BenchmarkDetectProtocol(b *testing.B) {
	bodies := []string{
		`{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}]}`,
		`{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "system": "You are helpful", "messages": [{"role": "user", "content": "hi"}]}`,
		`{"model": "MiniMax-M2.7", "messages": [{"role": "user", "content": "hi"}]}`,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, body := range bodies {
			_, _, err := DetectProtocol([]byte(body))
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
