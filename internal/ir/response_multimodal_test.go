package ir

import (
	"encoding/json"
	"testing"
)

// TestParseAnthropicResponse_WithCacheTokens verifies that Anthropic cache tokens
// are correctly extracted from non-streaming responses.
// audit-ir-multimodal (2026-07-13): Previously these fields were ignored,
// causing billing inaccuracy for Anthropic prompt caching requests.
func TestParseAnthropicResponse_WithCacheTokens(t *testing.T) {
	body := []byte(`{
		"id": "msg_01abc",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello"}],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 80,
			"cache_read_input_tokens": 20
		}
	}`)

	ir, err := ParseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}

	if ir.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", ir.Usage.PromptTokens)
	}
	if ir.Usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", ir.Usage.CompletionTokens)
	}

	// Verify cache_creation_input_tokens extracted to CacheWriteTokens
	if ir.Usage.CacheWriteTokens == nil {
		t.Fatal("CacheWriteTokens is nil, want 80")
	}
	if *ir.Usage.CacheWriteTokens != 80 {
		t.Errorf("CacheWriteTokens = %d, want 80", *ir.Usage.CacheWriteTokens)
	}

	// Verify cache_read_input_tokens extracted to CacheReadTokens
	if ir.Usage.CacheReadTokens == nil {
		t.Fatal("CacheReadTokens is nil, want 20")
	}
	if *ir.Usage.CacheReadTokens != 20 {
		t.Errorf("CacheReadTokens = %d, want 20", *ir.Usage.CacheReadTokens)
	}
}

// TestParseOpenAIResponse_WithDetailedUsage verifies that OpenAI detailed usage fields
// (image_tokens, audio_tokens, video_tokens, reasoning_tokens, cached_tokens)
// are correctly extracted from responses.
func TestParseOpenAIResponse_WithDetailedUsage(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "gpt-4o",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "I can see the image."
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 1500,
			"completion_tokens": 50,
			"total_tokens": 1550,
			"prompt_tokens_details": {
				"cached_tokens": 100,
				"image_tokens": 300,
				"audio_tokens": 50
			},
			"completion_tokens_details": {
				"reasoning_tokens": 20
			}
		}
	}`)

	ir, err := ParseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("ParseOpenAIResponse: %v", err)
	}

	if ir.Usage.PromptTokens != 1500 {
		t.Errorf("PromptTokens = %d, want 1500", ir.Usage.PromptTokens)
	}

	// Verify cached_tokens
	if ir.Usage.CacheReadTokens == nil || *ir.Usage.CacheReadTokens != 100 {
		t.Errorf("CacheReadTokens = %v, want 100", ir.Usage.CacheReadTokens)
	}

	// Verify image_tokens
	if ir.Usage.ImageTokens == nil || *ir.Usage.ImageTokens != 300 {
		t.Errorf("ImageTokens = %v, want 300", ir.Usage.ImageTokens)
	}

	// Verify audio_tokens
	if ir.Usage.AudioTokens == nil || *ir.Usage.AudioTokens != 50 {
		t.Errorf("AudioTokens = %v, want 50", ir.Usage.AudioTokens)
	}

	// Verify reasoning_tokens
	if ir.Usage.ReasoningTokens == nil || *ir.Usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %v, want 20", ir.Usage.ReasoningTokens)
	}
}

// TestSerializeOpenAIResponse_WithMultimodalUsage verifies that multimodal usage fields
// are correctly serialized to OpenAI response format with prompt_tokens_details and
// completion_tokens_details nested objects.
func TestSerializeOpenAIResponse_WithMultimodalUsage(t *testing.T) {
	cacheRead := 100
	imageTokens := 300
	reasoningTokens := 20

	ir := &InternalResponse{
		ID:             "resp_123",
		Model:          "gpt-4o",
		Role:           "assistant",
		SourceProtocol: ProtocolOpenAIChat,
		Content:        []ResponseContentBlock{{Type: "text", Text: "Hello"}},
		FinishReason:   "stop",
		Usage: ResponseUsage{
			PromptTokens:     1000,
			CompletionTokens: 50,
			TotalTokens:      1050,
			CacheReadTokens:  &cacheRead,
			ImageTokens:      &imageTokens,
			ReasoningTokens:  &reasoningTokens,
		},
	}

	body, err := SerializeOpenAIResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	usage := resp["usage"].(map[string]any)

	// Verify basic tokens
	if int(usage["prompt_tokens"].(float64)) != 1000 {
		t.Errorf("prompt_tokens = %v, want 1000", usage["prompt_tokens"])
	}

	// Verify prompt_tokens_details exists
	promptDetails, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatal("prompt_tokens_details not found or wrong type")
	}

	if int(promptDetails["cached_tokens"].(float64)) != 100 {
		t.Errorf("cached_tokens = %v, want 100", promptDetails["cached_tokens"])
	}
	if int(promptDetails["image_tokens"].(float64)) != 300 {
		t.Errorf("image_tokens = %v, want 300", promptDetails["image_tokens"])
	}

	// Verify completion_tokens_details exists
	completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok {
		t.Fatal("completion_tokens_details not found or wrong type")
	}

	if int(completionDetails["reasoning_tokens"].(float64)) != 20 {
		t.Errorf("reasoning_tokens = %v, want 20", completionDetails["reasoning_tokens"])
	}
}

// TestSerializeAnthropicResponse_WithCacheTokens verifies that cache tokens
// are correctly serialized to Anthropic response format.
func TestSerializeAnthropicResponse_WithCacheTokens(t *testing.T) {
	cacheWrite := 80
	cacheRead := 20

	ir := &InternalResponse{
		ID:             "msg_123",
		Model:          "claude-3-5-sonnet-20241022",
		Role:           "assistant",
		SourceProtocol: ProtocolAnthropicMessages,
		Content:        []ResponseContentBlock{{Type: "text", Text: "Hello"}},
		FinishReason:   "stop",
		Usage: ResponseUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CacheWriteTokens: &cacheWrite,
			CacheReadTokens:  &cacheRead,
		},
	}

	body, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	usage := resp["usage"].(map[string]any)

	// Verify basic tokens
	if int(usage["input_tokens"].(float64)) != 100 {
		t.Errorf("input_tokens = %v, want 100", usage["input_tokens"])
	}
	if int(usage["output_tokens"].(float64)) != 50 {
		t.Errorf("output_tokens = %v, want 50", usage["output_tokens"])
	}

	// Verify cache tokens
	if int(usage["cache_creation_input_tokens"].(float64)) != 80 {
		t.Errorf("cache_creation_input_tokens = %v, want 80", usage["cache_creation_input_tokens"])
	}
	if int(usage["cache_read_input_tokens"].(float64)) != 20 {
		t.Errorf("cache_read_input_tokens = %v, want 20", usage["cache_read_input_tokens"])
	}
}

// TestRoundTrip_AnthropicWithCache verifies lossless round-trip conversion
// of Anthropic responses with cache tokens through IR.
func TestRoundTrip_AnthropicWithCache(t *testing.T) {
	original := `{
		"id": "msg_01xyz",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Cached response"}],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 500,
			"output_tokens": 100,
			"cache_creation_input_tokens": 400,
			"cache_read_input_tokens": 100
		}
	}`

	// Parse
	ir, err := ParseAnthropicResponse([]byte(original))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Serialize
	serialized, err := SerializeAnthropicResponse(ir, "")
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	// Verify cache tokens preserved
	var result map[string]any
	if err := json.Unmarshal(serialized, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}

	usage := result["usage"].(map[string]any)
	if int(usage["cache_creation_input_tokens"].(float64)) != 400 {
		t.Error("cache_creation_input_tokens not preserved in round-trip")
	}
	if int(usage["cache_read_input_tokens"].(float64)) != 100 {
		t.Error("cache_read_input_tokens not preserved in round-trip")
	}
}
