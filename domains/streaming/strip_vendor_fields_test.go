package streaming

import (
	"encoding/json"
	"testing"
)

func TestStripZhipuFieldsBody(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		stripped []string
		kept     []string
	}{
		{
			name: "strips zhipu private fields",
			input: `{
				"id": "chat-123",
				"model": "glm-4",
				"choices": [{"message": {"content": "hello"}}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 5},
				"zhipu_request_id": "private-id-123",
				"cache_read_tokens": 100,
				"web_search_results": ["result1", "result2"],
				"system_fingerprint": "fp-123"
			}`,
			stripped: []string{"zhipu_request_id", "cache_read_tokens", "web_search_results", "system_fingerprint"},
			kept:     []string{"id", "model", "choices", "usage"},
		},
		{
			name:     "handles empty body",
			input:    ``,
			stripped: []string{},
			kept:     []string{},
		},
		{
			name: "handles body with no private fields",
			input: `{
				"id": "chat-123",
				"model": "glm-4",
				"choices": [{"message": {"content": "hello"}}]
			}`,
			stripped: []string{},
			kept:     []string{"id", "model", "choices"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripZhipuFieldsBody([]byte(tt.input))

			if tt.input == "" {
				if len(result) != 0 {
					t.Errorf("expected empty result for empty input, got %s", result)
				}
				return
			}

			var resultMap map[string]interface{}
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			// Check stripped fields are removed
			for _, field := range tt.stripped {
				if _, exists := resultMap[field]; exists {
					t.Errorf("field %s should be stripped but still exists", field)
				}
			}

			// Check kept fields remain
			for _, field := range tt.kept {
				if _, exists := resultMap[field]; !exists {
					t.Errorf("field %s should be kept but was removed", field)
				}
			}
		})
	}
}

func TestStripDeepSeekFieldsBody(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		stripped []string
		kept     []string
	}{
		{
			name: "strips deepseek private fields including reasoning_tokens",
			input: `{
				"id": "chat-456",
				"model": "deepseek-chat",
				"choices": [{"message": {"content": "world"}}],
				"usage": {
					"prompt_tokens": 20,
					"completion_tokens": 10,
					"reasoning_tokens": 50
				},
				"deepseek_request_id": "ds-req-789",
				"prompt_cache_hit_tokens": 15,
				"system_fingerprint": "fp-456"
			}`,
			stripped: []string{"deepseek_request_id", "prompt_cache_hit_tokens", "system_fingerprint"},
			kept:     []string{"id", "model", "choices", "usage"},
		},
		{
			name: "handles R1 reasoning fields",
			input: `{
				"id": "chat-r1",
				"model": "deepseek-reasoner",
				"reasoning_content": "Let me think step by step...",
				"reasoning_tokens": 100,
				"choices": [{"message": {"content": "answer"}}]
			}`,
			stripped: []string{"reasoning_content", "reasoning_tokens"},
			kept:     []string{"id", "model", "choices"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripDeepSeekFieldsBody([]byte(tt.input))

			var resultMap map[string]interface{}
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			for _, field := range tt.stripped {
				if _, exists := resultMap[field]; exists {
					t.Errorf("field %s should be stripped but still exists", field)
				}
			}

			for _, field := range tt.kept {
				if _, exists := resultMap[field]; !exists {
					t.Errorf("field %s should be kept but was removed", field)
				}
			}
		})
	}
}

func TestStripDoubaoFieldsBody(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		stripped []string
		kept     []string
	}{
		{
			name: "strips doubao/volcengine private fields",
			input: `{
				"id": "chat-db",
				"model": "doubao-pro",
				"choices": [{"message": {"content": "response"}}],
				"usage": {"prompt_tokens": 30, "completion_tokens": 15},
				"doubao_request_id": "db-req-999",
				"seeddance_request_id": "sd-req-888",
				"content_safety_score": 0.95,
				"ab_test_group": "experiment-A",
				"system_fingerprint": "fp-db"
			}`,
			stripped: []string{"doubao_request_id", "seeddance_request_id", "content_safety_score", "ab_test_group", "system_fingerprint"},
			kept:     []string{"id", "model", "choices", "usage"},
		},
		{
			name:     "handles invalid json gracefully",
			input:    `{invalid json`,
			stripped: []string{},
			kept:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripDoubaoFieldsBody([]byte(tt.input))

			// For invalid JSON, the function should return the original body
			if tt.name == "handles invalid json gracefully" {
				if string(result) != tt.input {
					t.Errorf("invalid JSON should be returned as-is")
				}
				return
			}

			var resultMap map[string]interface{}
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			for _, field := range tt.stripped {
				if _, exists := resultMap[field]; exists {
					t.Errorf("field %s should be stripped but still exists", field)
				}
			}

			for _, field := range tt.kept {
				if _, exists := resultMap[field]; !exists {
					t.Errorf("field %s should be kept but was removed", field)
				}
			}
		})
	}
}

func TestStripMinimaxFieldsBody(t *testing.T) {
	// Regression test: ensure existing MiniMax stripper still works
	input := `{
		"id": "chat-mm",
		"model": "minimax-m3",
		"choices": [{"message": {"content": "test"}}],
		"nvext": {"private": "data"},
		"base_resp": {"status": 200},
		"request_id": "mm-req-123"
	}`

	result := StripMinimaxFieldsBody([]byte(input))

	var resultMap map[string]interface{}
	if err := json.Unmarshal(result, &resultMap); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	stripped := []string{"nvext", "base_resp", "request_id"}
	for _, field := range stripped {
		if _, exists := resultMap[field]; exists {
			t.Errorf("field %s should be stripped but still exists", field)
		}
	}

	kept := []string{"id", "model", "choices"}
	for _, field := range kept {
		if _, exists := resultMap[field]; !exists {
			t.Errorf("field %s should be kept but was removed", field)
		}
	}
}
