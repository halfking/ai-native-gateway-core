package streaming

import (
	"encoding/json"
	"testing"
)

// TestConditionalStripByProvider verifies that only the matching vendor
// stripper runs and that unknown/openai providers preserve same-named fields
// (P0-3, 2026-07-11).
func TestConditionalStripByProvider(t *testing.T) {
	tests := []struct {
		name        string
		catalogCode string
		input       string
		stripFunc   func([]byte) []byte
		expectStrip []string
		expectKeep  []string
	}{
		{
			name:        "minimax_strips_nvext",
			catalogCode: "minimax",
			input: `{
				"id": "chat-1",
				"model": "minimax-m3",
				"choices": [{"message": {"content": "test"}}],
				"nvext": {"private": "data"},
				"request_id": "mm-123"
			}`,
			stripFunc:   StripMinimaxFieldsBody,
			expectStrip: []string{"nvext", "request_id"},
			expectKeep:  []string{"id", "model", "choices"},
		},
		{
			name:        "zhipu_strips_zhipu_request_id",
			catalogCode: "zhipu",
			input: `{
				"id": "chat-2",
				"model": "glm-4",
				"choices": [{"message": {"content": "test"}}],
				"zhipu_request_id": "zp-456",
				"web_search_results": ["r1"]
			}`,
			stripFunc:   StripZhipuFieldsBody,
			expectStrip: []string{"zhipu_request_id", "web_search_results"},
			expectKeep:  []string{"id", "model", "choices"},
		},
		{
			name:        "deepseek_strips_reasoning_tokens",
			catalogCode: "deepseek",
			input: `{
				"id": "chat-3",
				"model": "deepseek-r1",
				"choices": [{"message": {"content": "test"}}],
				"reasoning_tokens": 100,
				"deepseek_request_id": "ds-789"
			}`,
			stripFunc:   StripDeepSeekFieldsBody,
			expectStrip: []string{"reasoning_tokens", "deepseek_request_id"},
			expectKeep:  []string{"id", "model", "choices"},
		},
		{
			name:        "doubao_strips_content_safety_score",
			catalogCode: "doubao",
			input: `{
				"id": "chat-4",
				"model": "doubao-pro",
				"choices": [{"message": {"content": "test"}}],
				"content_safety_score": 0.95,
				"doubao_request_id": "db-101"
			}`,
			stripFunc:   StripDoubaoFieldsBody,
			expectStrip: []string{"content_safety_score", "doubao_request_id"},
			expectKeep:  []string{"id", "model", "choices"},
		},
		{
			name:        "openai_through_minimax_stripper_still_strips_request_id",
			catalogCode: "openai",
			input: `{
				"id": "chat-5",
				"model": "gpt-4",
				"choices": [{"message": {"content": "test"}}],
				"request_id": "oai-123"
			}`,
			stripFunc:   StripMinimaxFieldsBody,
			expectStrip: []string{"request_id"},
			expectKeep:  []string{"id", "model", "choices"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.stripFunc([]byte(tt.input))

			var resultMap map[string]interface{}
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			// Verify stripped fields are gone
			for _, field := range tt.expectStrip {
				if _, exists := resultMap[field]; exists {
					t.Errorf("expected %s to be stripped, but it still exists", field)
				}
			}

			// Verify kept fields remain
			for _, field := range tt.expectKeep {
				if _, exists := resultMap[field]; !exists {
					t.Errorf("expected %s to be kept, but it was stripped", field)
				}
			}
		})
	}
}

// TestDispatchStripByProvider tests the conditional dispatcher that should
// route to the correct stripper based on catalogCode (P0-3 implementation).
func TestDispatchStripByProvider(t *testing.T) {
	input := `{
		"id": "chat-1",
		"model": "test",
		"choices": [{"message": {"content": "test"}}],
		"nvext": {"data": "minimax"},
		"zhipu_request_id": "zhipu-123",
		"reasoning_tokens": 50,
		"doubao_request_id": "doubao-456"
	}`

	tests := []struct {
		catalogCode string
		expectGone  []string
		expectKept  []string
	}{
		{
			catalogCode: "minimax",
			expectGone:  []string{"nvext"},
			expectKept:  []string{"zhipu_request_id", "reasoning_tokens", "doubao_request_id"},
		},
		{
			catalogCode: "zhipu",
			expectGone:  []string{"zhipu_request_id"},
			expectKept:  []string{"nvext", "reasoning_tokens", "doubao_request_id"},
		},
		{
			catalogCode: "deepseek",
			expectGone:  []string{"reasoning_tokens"},
			expectKept:  []string{"nvext", "zhipu_request_id", "doubao_request_id"},
		},
		{
			catalogCode: "doubao",
			expectGone:  []string{"doubao_request_id"},
			expectKept:  []string{"nvext", "zhipu_request_id", "reasoning_tokens"},
		},
		{
			catalogCode: "openai",
			expectGone:  []string{},
			expectKept:  []string{"nvext", "zhipu_request_id", "reasoning_tokens", "doubao_request_id"},
		},
		{
			catalogCode: "unknown",
			expectGone:  []string{},
			expectKept:  []string{"nvext", "zhipu_request_id", "reasoning_tokens", "doubao_request_id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.catalogCode, func(t *testing.T) {
			result := DispatchStripVendorFields([]byte(input), tt.catalogCode)

			var resultMap map[string]interface{}
			if err := json.Unmarshal(result, &resultMap); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			for _, field := range tt.expectGone {
				if _, exists := resultMap[field]; exists {
					t.Errorf("catalog=%s: expected %s to be stripped", tt.catalogCode, field)
				}
			}

			for _, field := range tt.expectKept {
				if _, exists := resultMap[field]; !exists {
					t.Errorf("catalog=%s: expected %s to be kept", tt.catalogCode, field)
				}
			}
		})
	}
}
