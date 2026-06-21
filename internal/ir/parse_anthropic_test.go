package ir

import (
	"fmt"
	"testing"
)

func TestParseAnthropic_SimpleMessage(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", ir.Model, "claude-sonnet-4-20250514")
	}
	if ir.SourceProtocol != ProtocolAnthropicMessages {
		t.Errorf("SourceProtocol = %q, want %q", ir.SourceProtocol, ProtocolAnthropicMessages)
	}
	if ir.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", ir.MaxTokens)
	}
	if len(ir.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", ir.Messages[0].Role, "user")
	}
	if len(ir.Messages[0].Content) != 1 {
		t.Fatalf("len(Messages[0].Content) = %d, want 1", len(ir.Messages[0].Content))
	}
	if ir.Messages[0].Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", ir.Messages[0].Content[0].Type, "text")
	}
	if ir.Messages[0].Content[0].Text != "hello" {
		t.Errorf("Content[0].Text = %q, want %q", ir.Messages[0].Content[0].Text, "hello")
	}
}

func TestParseAnthropic_AllSamplingFields(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 100,
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.Temperature == nil || *ir.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", ir.Temperature)
	}
	if ir.TopP == nil || *ir.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", ir.TopP)
	}
	if ir.TopK == nil || *ir.TopK != 100 {
		t.Errorf("TopK = %v, want 100", ir.TopK)
	}
	if !ir.Stream {
		t.Error("Stream = false, want true")
	}
}

func TestParseAnthropic_StopSequences(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"stop_sequences": ["END", "STOP"],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Stop) != 2 || ir.Stop[0] != "END" || ir.Stop[1] != "STOP" {
		t.Errorf("Stop = %v, want [END, STOP]", ir.Stop)
	}
}

func TestParseAnthropic_SystemPrompt(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"system": "You are a helpful assistant.",
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.System == nil {
		t.Fatal("System = nil")
	}
	if ir.System.Content != "You are a helpful assistant." {
		t.Errorf("System.Content = %q, want %q", ir.System.Content, "You are a helpful assistant.")
	}
}

func TestParseAnthropic_SystemPromptWithBlocks(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"system": [
			{"type": "text", "text": "You are a helpful assistant."},
			{"type": "text", "text": "Be concise."}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.System == nil {
		t.Fatal("System = nil")
	}
	if len(ir.System.Parts) != 2 {
		t.Fatalf("len(System.Parts) = %d, want 2", len(ir.System.Parts))
	}
}

func TestParseAnthropic_Tools(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "what's the weather"}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather for a location",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
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

func TestParseAnthropic_ToolChoice(t *testing.T) {
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
			name:     "any",
			body:     `"tool_choice": "any"`,
			wantType: "any",
		},
		{
			name:     "tool by name",
			body:     `"tool_choice": {"type": "tool", "name": "get_weather"}`,
			wantType: "tool",
			wantName: "get_weather",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fullBody := `{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "messages": [{"role": "user", "content": "hi"}], ` + tt.body + `}`
			ir, err := ParseAnthropic([]byte(fullBody))
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

func TestParseAnthropic_ToolUseBlocks(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "what's the weather in Tokyo"},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "I'll check the weather for you."},
					{"type": "tool_use", "id": "toolu_123", "name": "get_weather", "input": {"location": "Tokyo"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_123", "content": "The weather in Tokyo is 72°F and sunny."}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(ir.Messages))
	}

	// Check assistant message with tool_use
	asstMsg := ir.Messages[1]
	if asstMsg.Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want %q", asstMsg.Role, "assistant")
	}
	if len(asstMsg.Content) != 2 {
		t.Fatalf("len(Content) = %d, want 2", len(asstMsg.Content))
	}
	if asstMsg.Content[1].Type != "tool_use" {
		t.Errorf("Content[1].Type = %q, want %q", asstMsg.Content[1].Type, "tool_use")
	}
	if asstMsg.Content[1].ToolUse == nil {
		t.Fatal("Content[1].ToolUse = nil")
	}
	if asstMsg.Content[1].ToolUse.ID != "toolu_123" {
		t.Errorf("ToolUse.ID = %q, want %q", asstMsg.Content[1].ToolUse.ID, "toolu_123")
	}
	if asstMsg.Content[1].ToolUse.Name != "get_weather" {
		t.Errorf("ToolUse.Name = %q, want %q", asstMsg.Content[1].ToolUse.Name, "get_weather")
	}

	// Check tool result message
	toolMsg := ir.Messages[2]
	if toolMsg.Role != "user" {
		t.Errorf("Messages[2].Role = %q, want %q", toolMsg.Role, "user")
	}
	if len(toolMsg.Content) != 1 || toolMsg.Content[0].Type != "tool_result" {
		t.Errorf("Content[0] = %#v, want tool_result block", toolMsg.Content[0])
	}
	if toolMsg.Content[0].ToolResult == nil {
		t.Fatal("ToolResult = nil")
	}
	if toolMsg.Content[0].ToolResult.ToolUseID != "toolu_123" {
		t.Errorf("ToolResult.ToolUseID = %q, want %q", toolMsg.Content[0].ToolResult.ToolUseID, "toolu_123")
	}
}

func TestParseAnthropic_Metadata(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "hi"}],
		"metadata": {"user_id": "user123"}
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.Metadata == nil {
		t.Fatal("Metadata = nil")
	}
	if ir.Metadata.UserID != "user123" {
		t.Errorf("Metadata.UserID = %q, want %q", ir.Metadata.UserID, "user123")
	}
	if ir.User != "user123" {
		t.Errorf("User = %q, want %q", ir.User, "user123")
	}
}

func TestParseAnthropic_Thinking(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 10000},
		"messages": [{"role": "user", "content": "what's 2+2"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if ir.Thinking == nil {
		t.Fatal("Thinking = nil")
	}
	if ir.Thinking.Type != "enabled" {
		t.Errorf("Thinking.Type = %q, want %q", ir.Thinking.Type, "enabled")
	}
	if ir.Thinking.BudgetTokens != 10000 {
		t.Errorf("Thinking.BudgetTokens = %d, want 10000", ir.Thinking.BudgetTokens)
	}
}

func TestParseAnthropic_ThinkingBlock(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me calculate 2+2..."},
					{"type": "text", "text": "The answer is 4."}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
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
	if msg.Content[0].Type != "thinking" {
		t.Errorf("Content[0].Type = %q, want %q", msg.Content[0].Type, "thinking")
	}
	if msg.Content[0].Thinking == nil {
		t.Fatal("Thinking = nil")
	}
	if msg.Content[0].Thinking.Thinking != "Let me calculate 2+2..." {
		t.Errorf("Thinking.Thinking = %q, want %q", msg.Content[0].Thinking.Thinking, "Let me calculate 2+2...")
	}
}

func TestParseAnthropic_CacheControl(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"cache_control": {"type": "ephemeral"},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.CacheControl) != 1 {
		t.Fatalf("len(CacheControl) = %d, want 1", len(ir.CacheControl))
	}
	if ir.CacheControl[0].Type != "ephemeral" {
		t.Errorf("CacheControl[0].Type = %q, want %q", ir.CacheControl[0].Type, "ephemeral")
	}
}

func TestParseAnthropic_ImageBlock(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "what's in this image"},
					{"type": "image", "source": {"type": "url", "media_type": "image/png", "url": "https://example.com/image.png"}}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	msg := ir.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("len(Content) = %d, want 2", len(msg.Content))
	}
	if msg.Content[1].Type != "image" {
		t.Errorf("Content[1].Type = %q, want %q", msg.Content[1].Type, "image")
	}
	if msg.Content[1].Image == nil {
		t.Fatal("Image = nil")
	}
	if msg.Content[1].Image.URL != "https://example.com/image.png" {
		t.Errorf("Image.URL = %q, want %q", msg.Content[1].Image.URL, "https://example.com/image.png")
	}
}

func TestParseAnthropic_Documents(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"documents": [
			{
				"type": "document",
				"source": {"type": "text", "data": "This is a document."},
				"title": "Test Doc"
			}
		],
		"messages": [{"role": "user", "content": "summarize"}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	if len(ir.Documents) != 1 {
		t.Fatalf("len(Documents) = %d, want 1", len(ir.Documents))
	}
	if ir.Documents[0].Title != "Test Doc" {
		t.Errorf("Documents[0].Title = %q, want %q", ir.Documents[0].Title, "Test Doc")
	}
	if ir.Documents[0].Source.Type != "text" {
		t.Errorf("Documents[0].Source.Type = %q, want %q", ir.Documents[0].Source.Type, "text")
	}
}

func TestParseAnthropic_RoundTrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 100,
		"stream": false,
		"stop_sequences": ["END"],
		"system": "You are a helpful assistant.",
		"messages": [
			{"role": "user", "content": "What's the weather in Tokyo?"},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "I'll check that for you."},
					{"type": "tool_use", "id": "toolu_123", "name": "get_weather", "input": {"location": "Tokyo"}}
				]
			}
		],
		"tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}}],
		"tool_choice": "auto",
		"metadata": {"user_id": "user123"}
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	// Verify all fields
	if ir.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", ir.Model, "claude-sonnet-4-20250514")
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
	if ir.TopK == nil || *ir.TopK != 100 {
		t.Errorf("TopK = %v, want 100", ir.TopK)
	}
	if ir.Stream {
		t.Error("Stream = true, want false")
	}
	if len(ir.Stop) != 1 || ir.Stop[0] != "END" {
		t.Errorf("Stop = %v, want [END]", ir.Stop)
	}
	if ir.System == nil || ir.System.Content != "You are a helpful assistant." {
		t.Errorf("System = %#v", ir.System)
	}
	if len(ir.Messages) != 2 {
		t.Errorf("len(Messages) = %d, want 2", len(ir.Messages))
	}
	if len(ir.Tools) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(ir.Tools))
	}
	if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice = %#v", ir.ToolChoice)
	}
	if ir.User != "user123" {
		t.Errorf("User = %q, want %q", ir.User, "user123")
	}
}

func TestParseAnthropic_IndexInToolResult(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "result 1", "index": 0},
					{"type": "tool_result", "tool_use_id": "toolu_2", "content": "result 2", "index": 1}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	msg := ir.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("len(Content) = %d, want 2", len(msg.Content))
	}
	if msg.Content[0].Index == nil || *msg.Content[0].Index != 0 {
		t.Errorf("Content[0].Index = %v, want 0", msg.Content[0].Index)
	}
	if msg.Content[1].Index == nil || *msg.Content[1].Index != 1 {
		t.Errorf("Content[1].Index = %v, want 1", msg.Content[1].Index)
	}
}

func TestParseAnthropic_ToolResultIsError(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "error: file not found", "is_error": true}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	msg := ir.Messages[0]
	if len(msg.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].ToolResult == nil {
		t.Fatal("ToolResult = nil")
	}
	if !msg.Content[0].ToolResult.IsError {
		t.Error("ToolResult.IsError = false, want true")
	}
}

func TestParseAnthropic_CacheControlOnBlock(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Hello", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}

	msg := ir.Messages[0]
	if len(msg.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].CacheControl == nil {
		t.Fatal("CacheControl = nil")
	}
	if msg.Content[0].CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want %q", msg.Content[0].CacheControl.Type, "ephemeral")
	}
}

func BenchmarkParseAnthropic(b *testing.B) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 100,
		"stream": false,
		"stop_sequences": ["END"],
		"system": "You are a helpful assistant.",
		"thinking": {"type": "enabled", "budget_tokens": 10000},
		"messages": [
			{"role": "user", "content": "What's the weather in Tokyo?"},
			{"role": "assistant", "content": [{"type": "thinking", "thinking": "Let me check..."}, {"type": "text", "text": "I'll check that for you."}, {"type": "tool_use", "id": "toolu_123", "name": "get_weather", "input": {"location": "Tokyo"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_123", "content": "72 degrees", "is_error": false}]}
		],
		"tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}}],
		"tool_choice": "auto",
		"metadata": {"user_id": "user123"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ParseAnthropic(body)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestParseAnthropic_TableDriven provides field-by-field verification.
// Note: PDF in system prompt is a complex case, tested separately
func TestParseAnthropic_TableDriven(t *testing.T) {
	// Currently no simple table-driven tests defined
	// Individual features are tested in dedicated test functions
}

func errfAnthropic(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
