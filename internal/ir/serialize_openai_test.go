package ir

import (
	"encoding/json"
	"testing"
)

func TestSerializeOpenAI_SimpleMessage(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		Stream: false,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["model"] != "gpt-4o" {
		t.Errorf("model = %q, want %q", result["model"], "gpt-4o")
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

func TestSerializeOpenAI_AllSamplingFields(t *testing.T) {
	ir := &InternalRequest{
		Model:       "gpt-4o",
		MaxTokens:   1024,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(0.9),
		Stream:      true,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v, want 1024", result["max_tokens"])
	}
	if result["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7", result["temperature"])
	}
	if result["top_p"] != 0.9 {
		t.Errorf("top_p = %v, want 0.9", result["top_p"])
	}
	if result["stream"] != true {
		t.Error("stream != true")
	}
}

func TestSerializeOpenAI_StopSequences(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		Stop:   []string{"END", "STOP"},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	stops, ok := result["stop"].([]any)
	if !ok {
		t.Fatal("stop is not array")
	}
	if len(stops) != 2 {
		t.Fatalf("len(stop) = %d, want 2", len(stops))
	}
	if stops[0] != "END" || stops[1] != "STOP" {
		t.Errorf("stop = %v, want [END, STOP]", stops)
	}
}

func TestSerializeOpenAI_SystemPrompt(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		System: &SystemPrompt{Content: "You are a helpful assistant."},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs, ok := result["messages"].([]any)
	if !ok {
		t.Fatal("messages is not array")
	}
	if len(msgs) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (system + user)", len(msgs))
	}
	sysMsg := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Errorf("first message role = %q, want %q", sysMsg["role"], "system")
	}
	if sysMsg["content"] != "You are a helpful assistant." {
		t.Errorf("system content = %q, want %q", sysMsg["content"], "You are a helpful assistant.")
	}
}

func TestSerializeOpenAI_Tools(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
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

	body, err := SerializeOpenAI(ir)
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
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %q, want %q", fn["name"], "get_weather")
	}
}

func TestSerializeOpenAI_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		tc       *ToolChoice
		wantType string
	}{
		{"auto", &ToolChoice{Type: "auto"}, "auto"},
		{"none", &ToolChoice{Type: "none"}, "none"},
		{"function", &ToolChoice{Type: "tool", Name: "get_weather"}, "function with name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ir := &InternalRequest{
				Model:     "gpt-4o",
				ToolChoice: tt.tc,
				Messages: []Message{
					{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
				},
			}

			body, err := SerializeOpenAI(ir)
			if err != nil {
				t.Fatal(err)
			}

			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}

			tc := result["tool_choice"]
			switch tt.wantType {
			case "auto", "none":
				if tc != tt.wantType {
					t.Errorf("tool_choice = %v, want %q", tc, tt.wantType)
				}
			case "function with name":
				tcMap := tc.(map[string]any)
				if tcMap["type"] != "function" {
					t.Errorf("tool_choice.type = %v, want function", tcMap["type"])
				}
				fn := tcMap["function"].(map[string]any)
				if fn["name"] != "get_weather" {
					t.Errorf("tool_choice.function.name = %v, want get_weather", fn["name"])
				}
			}
		})
	}
}

func TestSerializeOpenAI_AssistantWithToolCalls(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "weather"}}},
			{
				Role:    "assistant",
				Content: []ContentBlock{{Type: "text", Text: "I'll check..."}},
				ToolCalls: []ToolCall{
					{ID: "call_123", Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: `{"location":"Tokyo"}`}},
				},
			},
		},
	}

	body, err := SerializeOpenAI(ir)
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
	if asstMsg["role"] != "assistant" {
		t.Errorf("role = %q, want %q", asstMsg["role"], "assistant")
	}

	toolCalls := asstMsg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(toolCalls))
	}
	tc := toolCalls[0].(map[string]any)
	if tc["id"] != "call_123" {
		t.Errorf("tool_call id = %q, want %q", tc["id"], "call_123")
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function name = %q, want %q", fn["name"], "get_weather")
	}
}

func TestSerializeOpenAI_ToolRoleMessage(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		Messages: []Message{
			{
				Role:       "tool",
				ToolCallID: "call_123",
				Content:    []ContentBlock{{Type: "text", Text: "72 degrees"}},
			},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}

	toolMsg := msgs[0].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Errorf("role = %q, want %q", toolMsg["role"], "tool")
	}
	if toolMsg["tool_call_id"] != "call_123" {
		t.Errorf("tool_call_id = %q, want %q", toolMsg["tool_call_id"], "call_123")
	}
}

func TestSerializeOpenAI_OpenAIOnlyFields(t *testing.T) {
	fp := float64(0.5)
	pp := float64(0.3)
	lp := true
	tlp := 5
	seed := int64(42)
	n := 2

	ir := &InternalRequest{
		Model:             "gpt-4o",
		FrequencyPenalty:  &fp,
		PresencePenalty:   &pp,
		Logprobs:         &lp,
		TopLogprobs:       &tlp,
		Seed:              &seed,
		N:                 n,
		User:              "user123",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	if result["frequency_penalty"] != 0.5 {
		t.Errorf("frequency_penalty = %v, want 0.5", result["frequency_penalty"])
	}
	if result["presence_penalty"] != 0.3 {
		t.Errorf("presence_penalty = %v, want 0.3", result["presence_penalty"])
	}
	if result["logprobs"] != true {
		t.Error("logprobs != true")
	}
	if result["top_logprobs"] != float64(5) {
		t.Errorf("top_logprobs = %v, want 5", result["top_logprobs"])
	}
	if result["seed"] != float64(42) {
		t.Errorf("seed = %v, want 42", result["seed"])
	}
	if result["n"] != float64(2) {
		t.Errorf("n = %v, want 2", result["n"])
	}
	if result["user"] != "user123" {
		t.Errorf("user = %q, want %q", result["user"], "user123")
	}
}

func TestSerializeOpenAI_ResponseFormat(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		ResponseFormat: &ResponseFormat{
			Type:   "json_object",
			Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`),
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	rf := result["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %q, want %q", rf["type"], "json_object")
	}
}

func TestSerializeOpenAI_NilRequest(t *testing.T) {
	_, err := SerializeOpenAI(nil)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestSerializeOpenAI_ImageContent(t *testing.T) {
	ir := &InternalRequest{
		Model:  "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "what's in this image"},
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/image.png"}},
				},
			},
		},
	}

	body, err := SerializeOpenAI(ir)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}

	msgs := result["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("len(content) = %d, want 2", len(content))
	}

	textBlock := content[0].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] != "what's in this image" {
		t.Errorf("text block = %#v", textBlock)
	}

	imgBlock := content[1].(map[string]any)
	if imgBlock["type"] != "image_url" {
		t.Errorf("image type = %q, want image_url", imgBlock["type"])
	}
	imgURL := imgBlock["image_url"].(map[string]any)
	if imgURL["url"] != "https://example.com/image.png" {
		t.Errorf("image url = %q, want %q", imgURL["url"], "https://example.com/image.png")
	}
}

func BenchmarkSerializeOpenAI(b *testing.B) {
	ir := &InternalRequest{
		Model:       "gpt-4o",
		MaxTokens:   1024,
		Temperature: floatPtr(0.7),
		TopP:        floatPtr(0.9),
		Stream:      false,
		Stop:        []string{"END"},
		System:      &SystemPrompt{Content: "You are a helpful assistant."},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "What's the weather?"}}},
			{
				Role:    "assistant",
				Content: []ContentBlock{{Type: "text", Text: "I'll check..."}},
				ToolCalls: []ToolCall{
					{ID: "call_123", Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: `{"location":"Tokyo"}`}},
				},
			},
			{Role: "tool", ToolCallID: "call_123", Content: []ContentBlock{{Type: "text", Text: "72 degrees"}}},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
		ToolChoice: &ToolChoice{Type: "auto"},
		User:       "user123",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := SerializeOpenAI(ir)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Helper
func floatPtr(f float64) *float64 {
	return &f
}
