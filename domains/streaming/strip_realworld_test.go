package streaming

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestVendorStripperRealWorldScenarios 使用真实场景数据验证 stripper 改动正确性
func TestVendorStripperRealWorldScenarios(t *testing.T) {
	scenarios := []struct {
		name             string
		provider         string
		responseBody     string
		stripFunc        func([]byte) []byte
		expectedPreserve []string // 应该保留的字段
		expectedStrip    []string // 应该被清理的字段
	}{
		{
			name:     "DeepSeek reasoning response",
			provider: "deepseek",
			responseBody: `{
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"model": "deepseek-chat",
				"system_fingerprint": "fp_abc123",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "The answer is 42",
						"reasoning_content": "Let me think step by step..."
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30,
					"reasoning_tokens": 15,
					"prompt_cache_hit_tokens": 5,
					"prompt_cache_miss_tokens": 5
				},
				"deepseek_request_id": "internal-req-123"
			}`,
			stripFunc: StripDeepSeekFieldsBody,
			expectedPreserve: []string{
				"object",
				"system_fingerprint",
				"choices.0.message.reasoning_content",
				"usage.reasoning_tokens",
				"usage.prompt_cache_hit_tokens",
				"usage.prompt_cache_miss_tokens",
			},
			expectedStrip: []string{
				"deepseek_request_id",
			},
		},
		{
			name:     "MiniMax with reasoning split",
			provider: "minimax",
			responseBody: `{
				"id": "cmpl-456",
				"object": "chat.completion",
				"model": "abab6.5-chat",
				"system_fingerprint": "fp_xyz789",
				"service_tier": "default",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Result",
						"reasoning": "Internal reasoning"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 15,
					"completion_tokens": 25,
					"total_tokens": 40,
					"total_characters": 120,
					"reasoning_tokens": 10,
					"cache_read_tokens": 8
				},
				"nvext": {"internal": true},
				"base_resp": {"status": 0}
			}`,
			stripFunc: StripMinimaxFieldsBody,
			expectedPreserve: []string{
				"object",
				"system_fingerprint",
				"service_tier",
				"choices.0.message.reasoning",
				"usage.total_characters",
				"usage.reasoning_tokens",
				"usage.cache_read_tokens",
			},
			expectedStrip: []string{
				"nvext",
				"base_resp",
			},
		},
		{
			name:     "GLM with web_search",
			provider: "zhipu",
			responseBody: `{
				"id": "glm-789",
				"object": "chat.completion",
				"model": "glm-4-plus",
				"system_fingerprint": "fp_glm123",
				"request_id": "official-req-456",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Answer",
						"reasoning_content": "Thinking..."
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 12,
					"completion_tokens": 18,
					"total_tokens": 30,
					"prompt_tokens_details": {"cached_tokens": 6},
					"completion_tokens_details": {"reasoning_tokens": 5}
				},
				"zhipu_request_id": "internal-zhipu-123",
				"web_search_results": [{"title": "Result 1"}]
			}`,
			stripFunc: StripZhipuFieldsBody,
			expectedPreserve: []string{
				"object",
				"system_fingerprint",
				"request_id",
				"choices.0.message.reasoning_content",
				"usage.prompt_tokens_details",
				"usage.completion_tokens_details",
			},
			expectedStrip: []string{
				"zhipu_request_id",
				"web_search_results",
			},
		},
		{
			name:     "Doubao response",
			provider: "doubao",
			responseBody: `{
				"id": "doubao-101",
				"object": "chat.completion",
				"model": "doubao-pro",
				"system_fingerprint": "fp_doubao456",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Response"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 20,
					"completion_tokens": 30,
					"total_tokens": 50
				},
				"doubao_request_id": "internal-doubao-456",
				"content_safety_score": 0.95
			}`,
			stripFunc: StripDoubaoFieldsBody,
			expectedPreserve: []string{
				"object",
				"system_fingerprint",
			},
			expectedStrip: []string{
				"doubao_request_id",
				"content_safety_score",
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			stripped := sc.stripFunc([]byte(sc.responseBody))

			// 验证输出是合法 JSON
			var result map[string]interface{}
			if err := json.Unmarshal(stripped, &result); err != nil {
				t.Fatalf("stripped response is not valid JSON: %v\n%s", err, string(stripped))
			}

			// 验证应该保留的字段（使用 assertFieldPresent 支持嵌套路径）
			for _, field := range sc.expectedPreserve {
				if !assertFieldPresentInMap(t, result, field) {
					t.Errorf("field %s should be preserved but was stripped", field)
					t.Logf("stripped body: %s", string(stripped))
				}
			}

			// 验证应该被清理的字段
			for _, field := range sc.expectedStrip {
				if assertFieldPresentInMap(t, result, field) {
					t.Errorf("field %s should be stripped but was preserved", field)
					t.Logf("stripped body: %s", string(stripped))
				}
			}
		})
	}
}

// assertFieldPresentInMap checks if a field exists in a map, supporting dotted paths.
func assertFieldPresentInMap(t *testing.T, root map[string]interface{}, path string) bool {
	t.Helper()
	parts := strings.Split(path, ".")
	var cur interface{} = root

	for _, seg := range parts {
		switch node := cur.(type) {
		case map[string]interface{}:
			child, ok := node[seg]
			if !ok {
				return false
			}
			cur = child
		case []interface{}:
			// For array access like "choices.0.message"
			idx, err := strconv.Atoi(seg)
			if err != nil {
				return false
			}
			if idx < 0 || idx >= len(node) {
				return false
			}
			cur = node[idx]
		default:
			return false
		}
	}
	return true
}
