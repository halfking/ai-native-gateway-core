package executors

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErnieSearchInfo_NotStripped verifies that Baidu Ernie's search_info
// field is NOT stripped by the current vendor strip logic, contrary to the
// P2-Ernie-1 assumption in the audit report.
//
// Investigation result: No Ernie-specific strip logic exists in the codebase.
// The stripVendorFields function only handles minimax/zhipu/deepseek/doubao.
func TestErnieSearchInfo_NotStripped(t *testing.T) {
	// Simulated Ernie response with search_info
	body := []byte(`{
		"id": "ernie-response",
		"object": "chat.completion",
		"model": "ernie-4.0",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Based on search results..."
			},
			"finish_reason": "stop"
		}],
		"search_info": {
			"search_results": [
				{"index": 1, "url": "https://example.com/page1", "title": "Result 1"},
				{"index": 2, "url": "https://example.com/page2", "title": "Result 2"}
			]
		},
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30,
			"search_count": 2
		}
	}`)

	// Create executor with no strip functions (simulating Ernie path)
	executor := &Executor{
		StripMinimaxFields: nil,
		StripZhipuFields:   nil,
		StripDeepSeekFields: nil,
		StripDoubaoFields:  nil,
	}

	// stripVendorFields with empty catalogCode (third-party) or "ernie"
	stripped := executor.stripVendorFields(body, "ernie")

	var result map[string]any
	require.NoError(t, json.Unmarshal(stripped, &result))

	// Verify search_info is preserved
	assert.Contains(t, result, "search_info", "search_info should NOT be stripped")

	searchInfo, ok := result["search_info"].(map[string]any)
	require.True(t, ok, "search_info should be a map")

	searchResults, ok := searchInfo["search_results"].([]any)
	require.True(t, ok, "search_results should be an array")
	require.Len(t, searchResults, 2, "should have 2 search results")

	firstResult := searchResults[0].(map[string]any)
	assert.Equal(t, float64(1), firstResult["index"])
	assert.Equal(t, "https://example.com/page1", firstResult["url"])
	assert.Equal(t, "Result 1", firstResult["title"])
}

// TestErnieSearchInfo_EmptyCatalogCode verifies Ernie response with empty
// catalog_code (third-party provider scenario) also preserves search_info.
func TestErnieSearchInfo_EmptyCatalogCode(t *testing.T) {
	body := []byte(`{
		"id": "ernie-response",
		"choices": [{"message": {"role": "assistant", "content": "test"}}],
		"search_info": {
			"search_results": [
				{"index": 1, "url": "https://test.com", "title": "Test"}
			]
		}
	}`)

	executor := &Executor{}
	stripped := executor.stripVendorFields(body, "")

	var result map[string]any
	require.NoError(t, json.Unmarshal(stripped, &result))

	// Should be unchanged (no Ernie-specific detection)
	assert.Contains(t, result, "search_info", "search_info should be preserved with empty catalog_code")
}
