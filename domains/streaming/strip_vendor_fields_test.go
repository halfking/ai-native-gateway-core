package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// assertFieldPresent walks a dotted path and reports whether the field exists.
// "usage.prompt_tokens_details" finds the `prompt_tokens_details` field under
// the `usage` object. Numeric segments traverse array indices.
func assertFieldPresent(t *testing.T, root map[string]interface{}, path string) bool {
	t.Helper()
	parts := strings.Split(path, ".")
	cur := any(root)
	for _, seg := range parts {
		switch node := cur.(type) {
		case map[string]interface{}:
			child, ok := node[seg]
			if !ok {
				return false
			}
			cur = child
		case []interface{}:
			idx, err := atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return false
			}
			cur = node[idx]
		default:
			return false
		}
	}
	return true
}

// assertFieldAbsent is the inverse of assertFieldPresent.
func assertFieldAbsent(t *testing.T, root map[string]interface{}, path string) bool {
	t.Helper()
	return !assertFieldPresent(t, root, path)
}

func atoi(s string) (int, error) {
	n := 0
	for i, c := range s {
		if c < '0' || c > '9' {
			return 0, &strconvErr{s: s, idx: i}
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

type strconvErr struct {
	s   string
	idx int
}

func (e *strconvErr) Error() string { return "bad index: " + e.s }

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
			stripped: []string{"zhipu_request_id", "web_search_results", "system_fingerprint"},
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
		{
			// Real capture: glm-5.2 response from provider_id=32, ts=2026-07-09
			name: "strips GLM-5 nested usage details and reasoning chain",
			input: `{
				"id": "202607091302388f6a7ff3cf8c400c",
				"model": "glm-5.2",
				"usage": {
					"total_tokens": 18,
					"prompt_tokens": 13,
					"completion_tokens": 5,
					"prompt_tokens_details": {"cached_tokens": 0},
					"completion_tokens_details": {"reasoning_tokens": 2}
				},
				"choices": [{
					"message": {
						"role": "assistant",
						"reasoning_content": "Let me think about it...",
						"content": "answer",
						"tool_calls": [{"id": "call_x", "type": "function"}]
					},
					"finish_reason": "tool_calls"
				}],
				"system_fingerprint": "fp_glm5_2"
			}`,
			stripped: []string{
				"prompt_tokens_details",
				"completion_tokens_details",
				"reasoning_content",
				"system_fingerprint",
			},
			kept: []string{
				"id", "model", "usage",
				"usage.prompt_tokens", "usage.completion_tokens", "usage.total_tokens",
				"choices.0.message.role", "choices.0.message.content",
				"choices.0.message.tool_calls", "choices.0.finish_reason",
			},
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
				if !assertFieldAbsent(t, resultMap, field) {
					t.Errorf("field %s should be stripped but still exists", field)
				}
			}

			// Check kept fields remain
			for _, field := range tt.kept {
				if !assertFieldPresent(t, resultMap, field) {
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

func TestStripMinimaxNestedFields(t *testing.T) {
	// Real capture from 252 (provider_id=14, 2026-07-09 to 2026-07-11):
	// MiniMax M2 thinking-mode response with Anthropic-style usage extras.
	input := `{
		"id": "chatmm-abc",
		"model": "MiniMax-M2.7",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Final answer",
				"reasoning": "step-by-step private reasoning"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"total_tokens": 182,
			"prompt_tokens": 177,
			"completion_tokens": 5,
			"total_characters": 0,
			"cache_read_tokens": 128,
			"prompt_tokens_details": {"cached_tokens": 128},
			"completion_tokens_details": {"reasoning_tokens": 4}
		},
		"system_fingerprint": "fp_mm5"
	}`

	result := StripMinimaxFieldsBody([]byte(input))

	var resultMap map[string]interface{}
	if err := json.Unmarshal(result, &resultMap); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	stripped := []string{
		"total_characters",
		"cache_read_tokens",
		"prompt_tokens_details",
		"completion_tokens_details",
		"reasoning",
		"system_fingerprint",
	}
	for _, field := range stripped {
		if !assertFieldAbsent(t, resultMap, field) {
			t.Errorf("field %s should be stripped but still exists", field)
		}
	}

	kept := []string{
		"id", "model", "usage",
		"usage.total_tokens", "usage.prompt_tokens", "usage.completion_tokens",
		"choices.0.message.role", "choices.0.message.content",
		"choices.0.finish_reason",
	}
	for _, field := range kept {
		if !assertFieldPresent(t, resultMap, field) {
			t.Errorf("field %s should be kept but was removed", field)
		}
	}
}
