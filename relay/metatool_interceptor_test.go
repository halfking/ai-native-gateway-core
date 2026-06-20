package relay

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMetaToolInterceptor_NoMetaTools(t *testing.T) {
	interceptor := NewMetaToolInterceptor(nil)

	body := []byte(`{
		"model": "glm-5.2",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	modified, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if intercepted {
		t.Error("expected no interception")
	}

	if string(modified) != string(body) {
		t.Error("body should not be modified")
	}
}

func TestMetaToolInterceptor_NonMetaTool(t *testing.T) {
	interceptor := NewMetaToolInterceptor(nil)

	body := []byte(`{
		"model": "glm-5.2",
		"messages": [
			{"role": "user", "content": "Search files"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "search_files",
							"arguments": "{\"pattern\":\"*.go\"}"
						}
					}
				]
			}
		]
	}`)

	modified, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if intercepted {
		t.Error("expected no interception for non-meta-tool")
	}

	if string(modified) != string(body) {
		t.Error("body should not be modified")
	}
}

func TestMetaToolInterceptor_InvalidJSON(t *testing.T) {
	interceptor := NewMetaToolInterceptor(nil)

	body := []byte(`invalid json`)

	modified, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if intercepted {
		t.Error("expected no interception for invalid JSON")
	}

	if string(modified) != string(body) {
		t.Error("body should not be modified")
	}
}

func TestMetaToolInterceptor_Structure(t *testing.T) {
	// Test that interceptor can be created
	interceptor := NewMetaToolInterceptor(nil)
	if interceptor == nil {
		t.Fatal("NewMetaToolInterceptor returned nil")
	}

	// Test with nil handler (should not panic)
	body := []byte(`{"messages":[]}`)
	_, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if intercepted {
		t.Error("nil handler should not intercept")
	}
}

func TestMetaToolInterceptor_ToolCallStructure(t *testing.T) {
	// Valid tool call structure
	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_abc",
						"type": "function",
						"function": {
							"name": "list_categories",
							"arguments": "{}"
						}
					}
				]
			}
		]
	}`)

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("failed to parse test body: %v", err)
	}

	messages := req["messages"].([]interface{})
	lastMsg := messages[0].(map[string]interface{})
	toolCalls := lastMsg["tool_calls"].([]interface{})

	if len(toolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	name := fn["name"].(string)

	if name != "list_categories" {
		t.Errorf("expected list_categories, got %s", name)
	}
}
