package ir

import (
	"fmt"
	"testing"
)

func TestParseOpenAI_SimpleMessage(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", ir.Model, "gpt-4o")
	}
	if ir.SourceProtocol != ProtocolOpenAIChat {
		t.Errorf("SourceProtocol = %q, want %q", ir.SourceProtocol, ProtocolOpenAIChat)
	}
	if len(ir.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", ir.Messages[0].Role, "user")
	}
	if len(ir.Messages[0].Content) != 1 || ir.Messages[0].Content[0].Type != "text" {
		t.Errorf("Messages[0].Content[0] = %#v, want text block", ir.Messages[0].Content)
	}
	if ir.Messages[0].Content[0].Text != "hello" {
		t.Errorf("Messages[0].Content[0].Text = %q, want %q", ir.Messages[0].Content[0].Text, "hello")
	}
}

func TestParseOpenAI_AllSamplingFields(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", ir.MaxTokens)
	}
	if ir.Temperature == nil || *ir.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", ir.Temperature)
	}
	if ir.TopP == nil || *ir.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", ir.TopP)
	}
	if !ir.Stream {
		t.Error("Stream = false, want true")
	}
}

func TestParseOpenAI_StopSequences(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"stop": ["END", "STOP"],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Stop) != 2 || ir.Stop[0] != "END" || ir.Stop[1] != "STOP" {
		t.Errorf("Stop = %v, want [END, STOP]", ir.Stop)
	}
}

func TestParseOpenAI_SystemPrompt(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "hi"}
		]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.System == nil {
		t.Fatal("System = nil, want non-nil")
	}
	if ir.System.Content != "You are a helpful assistant." {
		t.Errorf("System.Content = %q, want %q", ir.System.Content, "You are a helpful assistant.")
	}
	// System message should be removed from Messages
	if len(ir.Messages) != 1 || ir.Messages[0].Role != "user" {
		t.Errorf("Messages = %#v, want only user message", ir.Messages)
	}
}

func TestParseOpenAI_Tools(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "what's the weather"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get weather for a location",
					"parameters": {"type": "object", "properties": {"location": {"type": "string"}}}
				}
			}
		]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(ir.Tools))
	}
	if ir.Tools[0].Name != "get_weather" {
		t.Errorf("Tools[0].Name = %q, want %q", ir.Tools[0].Name, "get_weather")
	}
	if ir.Tools[0].Description != "Get weather for a location" {
		t.Errorf("Tools[0].Description = %q, want %q", ir.Tools[0].Description, "Get weather for a location")
	}
}

func TestParseOpenAI_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantType string
		wantName string
	}{
		{
			name:     "auto",
			body:     `"tool_choice": "auto"`,
			wantType: "auto",
		},
		{
			name:     "none",
			body:     `"tool_choice": "none"`,
			wantType: "none",
		},
		{
			name:     "function by name",
			body:     `"tool_choice": {"type": "function", "function": {"name": "get_weather"}}`,
			wantType: "function",
			wantName: "get_weather",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fullBody := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], ` + tt.body + `}`
			ir, err := ParseOpenAI([]byte(fullBody))
			if err != nil {
				t.Fatal(err)
			}
			if ir.ToolChoice == nil {
				t.Fatal("ToolChoice = nil")
			}
			if ir.ToolChoice.Type != tt.wantType {
				t.Errorf("ToolChoice.Type = %q, want %q", ir.ToolChoice.Type, tt.wantType)
			}
			if tt.wantName != "" && ir.ToolChoice.Name != tt.wantName {
				t.Errorf("ToolChoice.Name = %q, want %q", ir.ToolChoice.Name, tt.wantName)
			}
		})
	}
}

func TestParseOpenAI_AssistantWithToolCalls(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "what's the weather in Tokyo"},
			{
				"role": "assistant",
				"content": "I'll check the weather for you.",
				"tool_calls": [
					{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\": \"Tokyo\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_123",
				"content": "The weather in Tokyo is 72°F and sunny."
			}
		]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(ir.Messages))
	}

	// Check assistant message with tool_calls
	asstMsg := ir.Messages[1]
	if asstMsg.Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want %q", asstMsg.Role, "assistant")
	}
	if len(asstMsg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(asstMsg.ToolCalls))
	}
	if asstMsg.ToolCalls[0].ID != "call_123" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", asstMsg.ToolCalls[0].ID, "call_123")
	}
	if asstMsg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("ToolCalls[0].Function.Name = %q, want %q", asstMsg.ToolCalls[0].Function.Name, "get_weather")
	}

	// Check tool message
	toolMsg := ir.Messages[2]
	if toolMsg.Role != "tool" {
		t.Errorf("Messages[2].Role = %q, want %q", toolMsg.Role, "tool")
	}
	if toolMsg.ToolCallID != "call_123" {
		t.Errorf("ToolCallID = %q, want %q", toolMsg.ToolCallID, "call_123")
	}
}

func TestParseOpenAI_OpenAIOnlyFields(t *testing.T) {
	logProbs := true
	topLogProbs := 5
	seed := int64(42)
	n := 2
	body := []byte(`{
		"model": "gpt-4o",
		"frequency_penalty": 0.5,
		"presence_penalty": 0.3,
		"logprobs": true,
		"top_logprobs": 5,
		"seed": 42,
		"n": 2,
		"user": "user123",
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.FrequencyPenalty == nil || *ir.FrequencyPenalty != 0.5 {
		t.Errorf("FrequencyPenalty = %v, want 0.5", ir.FrequencyPenalty)
	}
	if ir.PresencePenalty == nil || *ir.PresencePenalty != 0.3 {
		t.Errorf("PresencePenalty = %v, want 0.3", ir.PresencePenalty)
	}
	if ir.Logprobs == nil || *ir.Logprobs != logProbs {
		t.Errorf("Logprobs = %v, want %v", ir.Logprobs, logProbs)
	}
	if ir.TopLogprobs == nil || *ir.TopLogprobs != topLogProbs {
		t.Errorf("TopLogprobs = %v, want %v", ir.TopLogprobs, topLogProbs)
	}
	if ir.Seed == nil || *ir.Seed != seed {
		t.Errorf("Seed = %v, want %v", ir.Seed, seed)
	}
	if ir.N != n {
		t.Errorf("N = %d, want %d", ir.N, n)
	}
	if ir.User != "user123" {
		t.Errorf("User = %q, want %q", ir.User, "user123")
	}
}

func TestParseOpenAI_ResponseFormat(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"response_format": {"type": "json_object", "json_schema": {"type": "object", "properties": {"answer": {"type": "string"}}}},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.ResponseFormat == nil {
		t.Fatal("ResponseFormat = nil")
	}
	if ir.ResponseFormat.Type != "json_object" {
		t.Errorf("ResponseFormat.Type = %q, want %q", ir.ResponseFormat.Type, "json_object")
	}
}

func TestParseOpenAI_ImageContent(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "what's in this image"},
					{"type": "image_url", "image_url": {"url": "https://example.com/image.png"}}
				]
			}
		]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(ir.Messages))
	}
	msg := ir.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("len(Content) = %d, want 2", len(msg.Content))
	}
	if msg.Content[0].Type != "text" || msg.Content[0].Text != "what's in this image" {
		t.Errorf("Content[0] = %#v, want text block", msg.Content[0])
	}
	if msg.Content[1].Type != "image" || msg.Content[1].Image == nil {
		t.Errorf("Content[1] = %#v, want image block", msg.Content[1])
	}
	if msg.Content[1].Image.URL != "https://example.com/image.png" {
		t.Errorf("Image.URL = %q, want %q", msg.Content[1].Image.URL, "https://example.com/image.png")
	}
}

func TestParseOpenAI_RoundTrip(t *testing.T) {
	// This test verifies that parsing a complex OpenAI request produces the expected IR
	body := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"stream": false,
		"stop": ["END"],
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "What's the weather in Tokyo?"}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get weather for a location",
					"parameters": {"type": "object", "properties": {"location": {"type": "string"}}}
				}
			}
		],
		"tool_choice": "auto",
		"user": "user123"
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	// Verify all fields are parsed correctly
	if ir.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", ir.Model, "gpt-4o")
	}
	if ir.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", ir.MaxTokens)
	}
	if ir.Temperature == nil || *ir.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", ir.Temperature)
	}
	if ir.TopP == nil || *ir.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", ir.TopP)
	}
	if ir.Stream {
		t.Error("Stream = true, want false")
	}
	if len(ir.Stop) != 1 || ir.Stop[0] != "END" {
		t.Errorf("Stop = %v, want [END]", ir.Stop)
	}
	if ir.System == nil || ir.System.Content != "You are a helpful assistant." {
		t.Errorf("System = %#v, want content", ir.System)
	}
	if len(ir.Messages) != 1 {
		t.Errorf("len(Messages) = %d, want 1 (system extracted)", len(ir.Messages))
	}
	if len(ir.Tools) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(ir.Tools))
	}
	if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice = %#v, want type=auto", ir.ToolChoice)
	}
	if ir.User != "user123" {
		t.Errorf("User = %q, want %q", ir.User, "user123")
	}
}

// TestParseOpenAI_FlatToolFormat tests that flat tool format (Anthropic-style) is also accepted.
func TestParseOpenAI_FlatToolFormat(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		]
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(ir.Tools))
	}
	if ir.Tools[0].Name != "get_weather" {
		t.Errorf("Tools[0].Name = %q, want %q", ir.Tools[0].Name, "get_weather")
	}
}

func TestParseOpenAI_EmptyMessages(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": []
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Messages) != 0 {
		t.Errorf("len(Messages) = %d, want 0", len(ir.Messages))
	}
}

func TestParseOpenAI_NullOptionalFields(t *testing.T) {
	// Ensure null values for optional fields don't cause errors
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": null,
		"tool_choice": null,
		"stop": null,
		"temperature": null,
		"top_p": null
	}`)

	ir, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Tools) != 0 {
		t.Errorf("len(Tools) = %d, want 0", len(ir.Tools))
	}
	if ir.ToolChoice != nil {
		t.Errorf("ToolChoice = %#v, want nil", ir.ToolChoice)
	}
	if len(ir.Stop) != 0 {
		t.Errorf("Stop = %v, want []", ir.Stop)
	}
}

// BenchmarkParseOpenAI benchmarks the OpenAI parser.
func BenchmarkParseOpenAI(b *testing.B) {
	body := []byte(`{
		"model": "gpt-4o",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"stream": false,
		"stop": ["END", "STOP"],
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "What's the weather in Tokyo?"},
			{"role": "assistant", "content": "I'll check that for you.", "tool_calls": [{"id": "call_123", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_123", "content": "72 degrees and sunny"}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}}}}] ,
		"tool_choice": "auto",
		"user": "user123"
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ParseOpenAI(body)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestParseOpenAI_TableDriven provides field-by-field verification.
func TestParseOpenAI_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		check   func(*InternalRequest) error
	}{
		{
			name: "max_completion_tokens",
			body: `{"model": "gpt-4o", "max_completion_tokens": 500, "messages": [{"role": "user", "content": "hi"}]}`,
			check: func(ir *InternalRequest) error {
				if ir.MaxTokens != 500 {
					return errf("MaxTokens = %d, want 500", ir.MaxTokens)
				}
				return nil
			},
		},
		{
			name: "single stop string",
			body: `{"model": "gpt-4o", "stop": "END", "messages": [{"role": "user", "content": "hi"}]}`,
			check: func(ir *InternalRequest) error {
				if len(ir.Stop) != 1 || ir.Stop[0] != "END" {
					return errf("Stop = %v, want [END]", ir.Stop)
				}
				return nil
			},
		},
		{
			name: "response_format text",
			body: `{"model": "gpt-4o", "response_format": {"type": "text"}, "messages": [{"role": "user", "content": "hi"}]}`,
			check: func(ir *InternalRequest) error {
				if ir.ResponseFormat == nil || ir.ResponseFormat.Type != "text" {
					return errf("ResponseFormat.Type = %v, want text", ir.ResponseFormat)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ir, err := ParseOpenAI([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.check(ir); err != nil {
				t.Error(err)
			}
		})
	}
}

func errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
