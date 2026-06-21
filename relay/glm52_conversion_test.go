package relay

import (
	"encoding/json"
	"testing"
)

// TestConvertChatToAnthropicGLM52 specifically tests glm-5.2 conversion
// to ensure proper handling of all OpenAI features
func TestConvertChatToAnthropicGLM52(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantErr       bool
		checkSystem   bool
		checkMessages bool
		checkMaxToken bool
		validate      func(t *testing.T, output []byte)
	}{
		{
			name: "glm-5.2_simple_request",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "user", "content": "Hello"}
				],
				"max_tokens": 50
			}`,
			wantErr:       false,
			checkMessages: true,
			checkMaxToken: true,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				// Must have model
				if model, ok := result["model"].(string); !ok || model != "glm-5.2" {
					t.Errorf("model = %v, want glm-5.2", result["model"])
				}
				
				// Must have max_tokens
				if maxTokens, ok := result["max_tokens"]; !ok {
					t.Error("output missing max_tokens")
				} else if mt, ok := maxTokens.(float64); !ok || mt != 50 {
					t.Errorf("max_tokens = %v, want 50", maxTokens)
				}
				
				// Must have messages array
				messages, ok := result["messages"].([]interface{})
				if !ok {
					t.Fatal("messages not an array")
				}
				if len(messages) != 1 {
					t.Errorf("messages length = %d, want 1", len(messages))
				}
			},
		},
		{
			name: "glm-5.2_with_system_message",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "system", "content": "You are helpful"},
					{"role": "user", "content": "Hello"}
				],
				"max_tokens": 100
			}`,
			wantErr:     false,
			checkSystem: true,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				// System message should be extracted to top-level
				if system, ok := result["system"].(string); !ok || system != "You are helpful" {
					t.Errorf("system = %v, want 'You are helpful'", result["system"])
				}
				
				// Messages should only contain user message
				messages, ok := result["messages"].([]interface{})
				if !ok {
					t.Fatal("messages not an array")
				}
				if len(messages) != 1 {
					t.Errorf("messages length = %d, want 1 (system should be extracted)", len(messages))
				}
				
				// Verify first message is user
				msg := messages[0].(map[string]interface{})
				if role := msg["role"].(string); role != "user" {
					t.Errorf("first message role = %s, want user", role)
				}
			},
		},
		{
			name: "glm-5.2_multi_turn_conversation",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "user", "content": "Hi"},
					{"role": "assistant", "content": "Hello"},
					{"role": "user", "content": "How are you?"}
				],
				"max_tokens": 50,
				"temperature": 0.7
			}`,
			wantErr: false,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				messages := result["messages"].([]interface{})
				if len(messages) != 3 {
					t.Errorf("messages length = %d, want 3", len(messages))
				}
				
				// Verify temperature preserved
				if temp, ok := result["temperature"].(float64); !ok || temp != 0.7 {
					t.Errorf("temperature = %v, want 0.7", result["temperature"])
				}
			},
		},
		{
			name: "glm-5.2_with_tools",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "user", "content": "What's the weather?"}
				],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "get_weather",
							"description": "Get weather",
							"parameters": {
								"type": "object",
								"properties": {
									"location": {"type": "string"}
								}
							}
						}
					}
				],
				"max_tokens": 50
			}`,
			wantErr: false,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				// Tools should be converted to Anthropic format
				tools, ok := result["tools"].([]interface{})
				if !ok {
					t.Fatal("tools not an array")
				}
				if len(tools) != 1 {
					t.Errorf("tools length = %d, want 1", len(tools))
				}
				
				tool := tools[0].(map[string]interface{})
				if name, ok := tool["name"].(string); !ok || name != "get_weather" {
					t.Errorf("tool name = %v, want get_weather", tool["name"])
				}
				
				// Should have input_schema instead of parameters
				if _, ok := tool["input_schema"]; !ok {
					t.Error("tool missing input_schema (should be converted from parameters)")
				}
			},
		},
		{
			name: "glm-5.2_default_max_tokens",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "user", "content": "Hello"}
				]
			}`,
			wantErr: false,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				// Should have default max_tokens of 4096
				if maxTokens, ok := result["max_tokens"].(float64); !ok || maxTokens != 4096 {
					t.Errorf("max_tokens = %v, want 4096 (default)", result["max_tokens"])
				}
			},
		},
		{
			name: "glm-5.2_empty_messages",
			input: `{
				"model": "glm-5.2",
				"messages": [],
				"max_tokens": 50
			}`,
			wantErr: false, // Should not error, but messages will be empty
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				messages, ok := result["messages"]
				if !ok {
					t.Fatal("messages field missing")
				}
				
				if messages == nil {
					t.Log("messages is nil (acceptable)")
					return
				}
				
				msgArray, ok := messages.([]interface{})
				if !ok {
					t.Fatalf("messages not an array, got %T", messages)
				}
				
				if len(msgArray) != 0 {
					t.Errorf("messages length = %d, want 0", len(msgArray))
				}
			},
		},
		{
			name: "glm-5.2_invalid_json",
			input: `{
				"model": "glm-5.2",
				"messages": [
					{"role": "user", "content": "Hello"
				]
			}`,
			wantErr: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := ConvertChatRequestToAnthropic([]byte(tt.input))
			
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertChatRequestToAnthropic() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			
			if tt.wantErr {
				return // Expected error, nothing more to check
			}
			
			if output == nil {
				t.Fatal("output is nil")
			}
			
			// Run custom validation
			if tt.validate != nil {
				tt.validate(t, output)
			}
			
			// Log output for manual inspection in verbose mode
			if testing.Verbose() {
				t.Logf("Input:  %s", tt.input)
				t.Logf("Output: %s", string(output))
			}
		})
	}
}

// TestAnthropicToOpenAIResponseGLM52 tests the reverse conversion
// for glm-5.2 responses
func TestAnthropicToOpenAIResponseGLM52(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		model    string
		wantErr  bool
		validate func(t *testing.T, output []byte)
	}{
		{
			name: "glm-5.2_simple_response",
			input: `{
				"id": "msg-123",
				"type": "message",
				"role": "assistant",
				"model": "glm-5.2",
				"content": [
					{"type": "text", "text": "Hello back"}
				],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 5
				},
				"stop_reason": "end_turn"
			}`,
			model:   "glm-5.2",
			wantErr: false,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				// Should have choices array
				choices, ok := result["choices"].([]interface{})
				if !ok {
					t.Fatal("choices not an array")
				}
				if len(choices) == 0 {
					t.Fatal("choices array is empty")
				}
				
				// Check first choice
				choice := choices[0].(map[string]interface{})
				message := choice["message"].(map[string]interface{})
				
				// Content should be flattened to string
				content, ok := message["content"].(string)
				if !ok {
					t.Errorf("content not a string, got %T", message["content"])
				}
				if content != "Hello back" {
					t.Errorf("content = %q, want 'Hello back'", content)
				}
				
				// Should have usage
				usage, ok := result["usage"].(map[string]interface{})
				if !ok {
					t.Fatal("usage not present")
				}
				if pt, ok := usage["prompt_tokens"].(float64); !ok || pt != 10 {
					t.Errorf("prompt_tokens = %v, want 10", usage["prompt_tokens"])
				}
			},
		},
		{
			name: "glm-5.2_with_thinking_blocks",
			input: `{
				"id": "msg-456",
				"type": "message",
				"role": "assistant",
				"model": "glm-5.2",
				"content": [
					{"type": "text", "text": "Answer"},
					{"type": "thinking", "thinking": "Internal reasoning"}
				],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20
				},
				"stop_reason": "end_turn"
			}`,
			model:   "glm-5.2",
			wantErr: false,
			validate: func(t *testing.T, output []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatalf("output not valid JSON: %v", err)
				}
				
				choices := result["choices"].([]interface{})
				choice := choices[0].(map[string]interface{})
				message := choice["message"].(map[string]interface{})
				
				// Content should only contain text, thinking dropped
				content := message["content"].(string)
				if content != "Answer" {
					t.Errorf("content = %q, want 'Answer' (thinking should be dropped)", content)
				}
				
				// Should have _kxg_meta indicating thinking blocks
				meta, ok := result["_kxg_meta"].(map[string]interface{})
				if !ok {
					t.Fatal("_kxg_meta not present")
				}
				
				if hasThinking, ok := meta["has_thinking"].(bool); !ok || !hasThinking {
					t.Error("_kxg_meta.has_thinking should be true")
				}
				
				// Note: thinking blocks are now kept in reasoning_content field
				// Check for thinking_blocks_count instead of dropped count
				if count, ok := meta["thinking_blocks_count"].(float64); ok && count != 1 {
					t.Errorf("thinking_blocks_count = %v, want 1", count)
				}
			},
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := ConvertAnthropicResponseToChat([]byte(tt.input), tt.model)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertAnthropicResponseToChat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			
			if tt.wantErr {
				return
			}
			
			if output == nil {
				t.Fatal("output is nil")
			}
			
			if tt.validate != nil {
				tt.validate(t, output)
			}
			
			if testing.Verbose() {
				t.Logf("Input:  %s", tt.input)
				t.Logf("Output: %s", string(output))
			}
		})
	}
}

// TestGLM52ConversionRoundTrip tests that data survives the round trip
func TestGLM52ConversionRoundTrip(t *testing.T) {
	originalRequest := `{
		"model": "glm-5.2",
		"messages": [
			{"role": "system", "content": "Be helpful"},
			{"role": "user", "content": "Hello"}
		],
		"max_tokens": 100,
		"temperature": 0.7
	}`
	
	// Convert to Anthropic
	anthropicReq, err := ConvertChatRequestToAnthropic([]byte(originalRequest))
	if err != nil {
		t.Fatalf("failed to convert request: %v", err)
	}
	
	t.Logf("Anthropic request: %s", string(anthropicReq))
	
	// Verify Anthropic format
	var anthReq map[string]interface{}
	if err := json.Unmarshal(anthropicReq, &anthReq); err != nil {
		t.Fatalf("Anthropic request not valid JSON: %v", err)
	}
	
	// Check key transformations
	if system, ok := anthReq["system"].(string); !ok || system == "" {
		t.Error("system message not extracted")
	}
	
	if maxTokens, ok := anthReq["max_tokens"].(float64); !ok || maxTokens != 100 {
		t.Errorf("max_tokens = %v, want 100", anthReq["max_tokens"])
	}
	
	messages := anthReq["messages"].([]interface{})
	if len(messages) != 1 {
		t.Errorf("messages length = %d, want 1 (only user message)", len(messages))
	}
	
	// Simulate Anthropic response
	anthropicResp := `{
		"id": "msg-789",
		"type": "message",
		"role": "assistant",
		"model": "glm-5.2",
		"content": [
			{"type": "text", "text": "Hi there!"}
		],
		"usage": {
			"input_tokens": 15,
			"output_tokens": 8
		},
		"stop_reason": "end_turn"
	}`
	
	// Convert back to OpenAI
	openaiResp, err := ConvertAnthropicResponseToChat([]byte(anthropicResp), "glm-5.2")
	if err != nil {
		t.Fatalf("failed to convert response: %v", err)
	}
	
	t.Logf("OpenAI response: %s", string(openaiResp))
	
	// Verify OpenAI format
	var oaiResp map[string]interface{}
	if err := json.Unmarshal(openaiResp, &oaiResp); err != nil {
		t.Fatalf("OpenAI response not valid JSON: %v", err)
	}
	
	choices := oaiResp["choices"].([]interface{})
	if len(choices) == 0 {
		t.Fatal("choices array is empty")
	}
	
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	content := message["content"].(string)
	
	if content != "Hi there!" {
		t.Errorf("content = %q, want 'Hi there!'", content)
	}
	
	// Verify usage mapping
	usage := oaiResp["usage"].(map[string]interface{})
	if pt := usage["prompt_tokens"].(float64); pt != 15 {
		t.Errorf("prompt_tokens = %v, want 15", pt)
	}
	if ct := usage["completion_tokens"].(float64); ct != 8 {
		t.Errorf("completion_tokens = %v, want 8", ct)
	}
}

// TestGLM52StreamEventDetection tests detection of problematic stream events
func TestGLM52StreamEventDetection(t *testing.T) {
	tests := []struct {
		name           string
		eventData      string
		isAnthropic    bool
		isOpenAI       bool
		hasEmptyChoice bool
		shouldDrop     bool
	}{
		{
			name:        "valid_anthropic_message_start",
			eventData:   `{"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`,
			isAnthropic: true,
			isOpenAI:    false,
			shouldDrop:  false,
		},
		{
			name:        "valid_anthropic_content_delta",
			eventData:   `{"type":"content_block_delta","index":0,"delta":{"type":"text","text":"Hello"}}`,
			isAnthropic: true,
			isOpenAI:    false,
			shouldDrop:  false,
		},
		{
			name:           "invalid_openai_chunk_in_anthropic_stream",
			eventData:      `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			isAnthropic:    false,
			isOpenAI:       true,
			hasEmptyChoice: false,
			shouldDrop:     true,
		},
		{
			name:           "empty_choices_array",
			eventData:      `{"id":"chatcmpl-2","object":"chat.completion.chunk","choices":[]}`,
			isAnthropic:    false,
			isOpenAI:       true,
			hasEmptyChoice: true,
			shouldDrop:     true,
		},
		{
			name:           "mixed_format_empty_type_with_choices",
			eventData:      `{"type":"","choices":[],"model":"glm-5.2"}`,
			isAnthropic:    false,
			isOpenAI:       true,
			hasEmptyChoice: true, // Empty choices array
			shouldDrop:     true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(tt.eventData), &data); err != nil {
				t.Fatalf("failed to parse event data: %v", err)
			}
			
			// Check event type
			eventType, hasType := data["type"].(string)
			choices, hasChoices := data["choices"].([]interface{})
			
			// Determine if this is Anthropic format
			isAnthropic := hasType && eventType != "" && !hasChoices
			if isAnthropic != tt.isAnthropic {
				t.Errorf("isAnthropic = %v, want %v", isAnthropic, tt.isAnthropic)
			}
			
			// Determine if this is OpenAI format
			isOpenAI := hasChoices || (data["id"] != nil && data["object"] != nil)
			if isOpenAI != tt.isOpenAI {
				t.Errorf("isOpenAI = %v, want %v", isOpenAI, tt.isOpenAI)
			}
			
			// Check for empty choices
			hasEmptyChoice := hasChoices && len(choices) == 0
			if hasEmptyChoice != tt.hasEmptyChoice {
				t.Errorf("hasEmptyChoice = %v, want %v", hasEmptyChoice, tt.hasEmptyChoice)
			}
			
			// Determine if should drop
			shouldDrop := (isOpenAI && !isAnthropic) || hasEmptyChoice
			if shouldDrop != tt.shouldDrop {
				t.Errorf("shouldDrop = %v, want %v", shouldDrop, tt.shouldDrop)
			}
		})
	}
}
