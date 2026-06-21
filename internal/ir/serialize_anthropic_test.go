package ir

import (
	"encoding/json"
	"testing"
)

func TestSerializeAnthropic_SimpleMessage(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", result["model"], "claude-sonnet-4-20250514")
	}
	if result["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v, want 1024", result["max_tokens"])
	}

	msgs, ok := result["messages"].([]any)
	if !ok {
		t.Fatal("messages is not array")
	}
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %q, want %q", msg["role"], "user")
	}
	if msg["content"] != "hello" {
		t.Errorf("content = %q, want %q", msg["content"], "hello")
	}
}

func TestSerializeAnthropic_AllSamplingFields(t *testing.T) {
	ir := &InternalRequest{
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   1024,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(0.9),
		TopK:        intPtr(100),
		Stream:      true,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7", result["temperature"])
	}
	if result["top_p"] != 0.9 {
		t.Errorf("top_p = %v, want 0.9", result["top_p"])
	}
	if result["top_k"] != float64(100) {
		t.Errorf("top_k = %v, want 100", result["top_k"])
	}
	if result["stream"] != true {
		t.Error("stream != true")
	}
}

func TestSerializeAnthropic_StopSequences(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Stop:      []string{"END", "STOP"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	stops, ok := result["stop_sequences"].([]any)
	if !ok {
		t.Fatal("stop_sequences is not array")
	}
	if len(stops) != 2 {
		t.Fatalf("len(stop_sequences) = %d, want 2", len(stops))
	}
	if stops[0] != "END" || stops[1] != "STOP" {
		t.Errorf("stop_sequences = %v, want [END, STOP]", stops)
	}
}

func TestSerializeAnthropic_SystemPrompt(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		System:    &SystemPrompt{Content: "You are a helpful assistant."},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["system"] != "You are a helpful assistant." {
		t.Errorf("system = %q, want %q", result["system"], "You are a helpful assistant.")
	}
}

func TestSerializeAnthropic_SystemPromptWithBlocks(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		System: &SystemPrompt{
			Parts: []ContentBlock{
				{Type: "text", Text: "You are a helpful assistant."},
				{Type: "text", Text: "Be concise."},
			},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	system, ok := result["system"].([]any)
	if !ok {
		t.Fatal("system is not array")
	}
	if len(system) != 2 {
		t.Fatalf("len(system) = %d, want 2", len(system))
	}
}

func TestSerializeAnthropic_Tools(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather for a location",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
			},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "weather"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("tools is not array")
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Errorf("tool name = %q, want %q", tool["name"], "get_weather")
	}
	if tool["description"] != "Get weather for a location" {
		t.Errorf("description = %q, want %q", tool["description"], "Get weather for a location")
	}
}

func TestSerializeAnthropic_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		tc       *ToolChoice
		want     any
	}{
		{"auto", &ToolChoice{Type: "auto"}, "auto"},
		{"none", &ToolChoice{Type: "none"}, "none"},
		{"any", &ToolChoice{Type: "any"}, "any"},
		{"tool with name", &ToolChoice{Type: "tool", Name: "get_weather"}, map[string]any{"type": "tool", "name": "get_weather"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ir := &InternalRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 1024,
				ToolChoice: tt.tc,
				Messages: []Message{
					{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
				},
			}

			body, err := SerializeAnthropic(ir)
			if err != nil {
				t.Fatal(err)
			}

			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}

			tc := result["tool_choice"]
			switch want := tt.want.(type) {
			case string:
				if tc != want {
					t.Errorf("tool_choice = %v, want %q", tc, want)
				}
			case map[string]any:
				tcMap := tc.(map[string]any)
				if tcMap["type"] != want["type"] {
					t.Errorf("tool_choice.type = %v, want %v", tcMap["type"], want["type"])
				}
				if tcMap["name"] != want["name"] {
					t.Errorf("tool_choice.name = %v, want %v", tcMap["name"], want["name"])
				}
			}
		})
	}
}

func TestSerializeAnthropic_ToolUseBlocks(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "weather"}}},
			{
				Role:    "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: "I'll check that for you."},
					{Type: "tool_use", ToolUse: &ToolUse{ID: "toolu_123", Name: "get_weather", Input: json.RawMessage(`{"location":"Tokyo"}`)}},
				},
			},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(msgs))
	}

	asstMsg := msgs[1].(map[string]any)
	content := asstMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("len(content) = %d, want 2", len(content))
	}

	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" {
		t.Errorf("type = %q, want %q", toolUse["type"], "tool_use")
	}
	if toolUse["id"] != "toolu_123" {
		t.Errorf("id = %q, want %q", toolUse["id"], "toolu_123")
	}
	if toolUse["name"] != "get_weather" {
		t.Errorf("name = %q, want %q", toolUse["name"], "get_weather")
	}
}

func TestSerializeAnthropic_ToolResultBlocks(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolResult: &ToolResult{
						ToolUseID: "toolu_123",
						Content:   []ContentBlock{{Type: "text", Text: "72 degrees and sunny"}},
						IsError:   false,
					}},
				},
			},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	msg := msgs[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(content))
	}

	tr := content[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Errorf("type = %q, want %q", tr["type"], "tool_result")
	}
	if tr["tool_use_id"] != "toolu_123" {
		t.Errorf("tool_use_id = %q, want %q", tr["tool_use_id"], "toolu_123")
	}
}

func TestSerializeAnthropic_Metadata(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		User:      "user123",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	meta, ok := result["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata is not map")
	}
	if meta["user_id"] != "user123" {
		t.Errorf("user_id = %q, want %q", meta["user_id"], "user123")
	}
}

func TestSerializeAnthropic_Thinking(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Thinking:  &ThinkingConfig{Type: "enabled", BudgetTokens: 10000},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "what's 2+2"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %q, want %q", thinking["type"], "enabled")
	}
	if thinking["budget_tokens"] != float64(10000) {
		t.Errorf("budget_tokens = %v, want 10000", thinking["budget_tokens"])
	}
}

func TestSerializeAnthropic_ThinkingBlock(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []Message{
			{
				Role:    "assistant",
				Content: []ContentBlock{
					{Type: "thinking", Thinking: &ThinkingBlock{Thinking: "Let me calculate..."}},
					{Type: "text", Text: "The answer is 4."},
				},
			},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)

	thinkingBlock := content[0].(map[string]any)
	if thinkingBlock["type"] != "thinking" {
		t.Errorf("type = %q, want %q", thinkingBlock["type"], "thinking")
	}
	if thinkingBlock["thinking"] != "Let me calculate..." {
		t.Errorf("thinking = %q, want %q", thinkingBlock["thinking"], "Let me calculate...")
	}
}

func TestSerializeAnthropic_CacheControl(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		CacheControl: []CacheControl{
			{Type: "ephemeral"},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	cc := result["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %q, want %q", cc["type"], "ephemeral")
	}
}

func TestSerializeAnthropic_ImageBlock(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", MediaType: "image/png", URL: "https://example.com/image.png"}},
				},
			},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)

	imgBlock := content[0].(map[string]any)
	if imgBlock["type"] != "image" {
		t.Errorf("type = %q, want %q", imgBlock["type"], "image")
	}
	source := imgBlock["source"].(map[string]any)
	if source["url"] != "https://example.com/image.png" {
		t.Errorf("url = %q, want %q", source["url"], "https://example.com/image.png")
	}
}

func TestSerializeAnthropic_Documents(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Documents: []Document{
			{
				Type: "document",
				Source: DocumentSource{Type: "text", Data: "This is a document."},
				Title: "Test Doc",
			},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "summarize"}}},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	docs := result["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("len(documents) = %d, want 1", len(docs))
	}
	doc := docs[0].(map[string]any)
	if doc["title"] != "Test Doc" {
		t.Errorf("title = %q, want %q", doc["title"], "Test Doc")
	}
}

func TestSerializeAnthropic_NilRequest(t *testing.T) {
	_, err := SerializeAnthropic(nil)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestSerializeAnthropic_RoundTrip(t *testing.T) {
	ir := &InternalRequest{
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   1024,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(0.9),
		TopK:        intPtr(100),
		Stream:      false,
		Stop:        []string{"END"},
		System:      &SystemPrompt{Content: "You are a helpful assistant."},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "What's the weather?"}}},
			{
				Role:    "assistant",
				Content: []ContentBlock{
					{Type: "thinking", Thinking: &ThinkingBlock{Thinking: "Let me check..."}},
					{Type: "text", Text: "I'll check that for you."},
					{Type: "tool_use", ToolUse: &ToolUse{ID: "toolu_123", Name: "get_weather", Input: json.RawMessage(`{"location":"Tokyo"}`)}},
				},
			},
		},
		Tools: []ToolDefinition{
			{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice: &ToolChoice{Type: "auto"},
		User:       "user123",
		Thinking:   &ThinkingConfig{Type: "enabled", BudgetTokens: 10000},
		CacheControl: []CacheControl{
			{Type: "ephemeral"},
		},
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	// Verify key fields
	if result["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q", result["model"])
	}
	if result["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v", result["max_tokens"])
	}
	if result["system"] != "You are a helpful assistant." {
		t.Errorf("system = %q", result["system"])
	}
	if result["thinking"] == nil {
		t.Error("thinking is nil")
	}
	if result["cache_control"] == nil {
		t.Error("cache_control is nil")
	}
	if len(result["messages"].([]any)) != 2 {
		t.Errorf("len(messages) = %d", len(result["messages"].([]any)))
	}
}

func BenchmarkSerializeAnthropic(b *testing.B) {
	ir := &InternalRequest{
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   1024,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(0.9),
		TopK:        intPtr(100),
		Stream:      false,
		Stop:        []string{"END"},
		System:      &SystemPrompt{Content: "You are a helpful assistant."},
		Thinking:    &ThinkingConfig{Type: "enabled", BudgetTokens: 10000},
		CacheControl: []CacheControl{
			{Type: "ephemeral"},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "What's the weather?"}}},
			{
				Role:    "assistant",
				Content: []ContentBlock{
					{Type: "thinking", Thinking: &ThinkingBlock{Thinking: "Let me check..."}},
					{Type: "text", Text: "I'll check that for you."},
					{Type: "tool_use", ToolUse: &ToolUse{ID: "toolu_123", Name: "get_weather", Input: json.RawMessage(`{"location":"Tokyo"}`)}},
				},
			},
			{
				Role:    "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolResult: &ToolResult{
						ToolUseID: "toolu_123",
						Content:   []ContentBlock{{Type: "text", Text: "72 degrees"}},
					}},
				},
			},
		},
		Tools: []ToolDefinition{
			{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice: &ToolChoice{Type: "auto"},
		User:       "user123",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := SerializeAnthropic(ir)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Helper
func intPtr(i int) *int {
	return &i
}
