package relay

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMetaToolInterceptor_NoMetaTools verifies the interceptor does
// nothing when the request body has no `tools` field at all.
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
		t.Error("expected no interception (no tools field)")
	}
	if string(modified) != string(body) {
		t.Error("body should not be modified")
	}
}

// TestMetaToolInterceptor_OnlyRegularTools verifies the interceptor
// does nothing when `tools` contains only non-meta tools.
func TestMetaToolInterceptor_OnlyRegularTools(t *testing.T) {
	interceptor := NewMetaToolInterceptor(nil)

	body := []byte(`{
		"model": "glm-5.2",
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "search_files",
					"description": "Search files",
					"parameters": {}
				}
			}
		],
		"messages": [{"role": "user", "content": "Search"}]
	}`)

	modified, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intercepted {
		t.Error("expected no interception when no meta-tool is present")
	}
	if string(modified) != string(body) {
		t.Error("body should not be modified")
	}
}

// TestMetaToolInterceptor_InvalidJSON ensures unparseable bodies are
// returned unchanged and never produce an error.
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

// TestMetaToolInterceptor_NilHandler ensures a nil handler is treated as
// a safe no-op (fail-open: don't break real requests on metatool outage).
func TestMetaToolInterceptor_NilHandler(t *testing.T) {
	interceptor := NewMetaToolInterceptor(nil)

	body := []byte(`{
		"model": "glm-5.2",
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "list_categories",
					"description": "List all tool categories"
				}
			}
		],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)

	_, intercepted, err := interceptor.InterceptRequest(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intercepted {
		t.Error("nil handler must never intercept (safe-default behaviour)")
	}
}

// TestMetaToolInterceptor_MetaToolNames is a sanity check on the
// canonical meta-tool name set. Must be in lock-step with
// metatools.MetaToolDefinitions() in metatools/handler.go.
func TestMetaToolInterceptor_MetaToolNames(t *testing.T) {
	if !MetaToolNames["list_categories"] {
		t.Error("MetaToolNames missing 'list_categories'")
	}
	if !MetaToolNames["load_tools"] {
		t.Error("MetaToolNames missing 'load_tools'")
	}
	if len(MetaToolNames) != 2 {
		t.Errorf("MetaToolNames should have exactly 2 entries, got %d", len(MetaToolNames))
	}
}

// TestMetaToolInterceptor_DetectMetaTool exercises the pure-JSON
// detection logic that runs before any DB access. It does not require
// a real metatools.Handler so it can be tested in isolation.
func TestMetaToolInterceptor_DetectMetaTool(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantFound bool
	}{
		{
			name:      "list_categories present",
			body:      `{"tools":[{"type":"function","function":{"name":"list_categories"}}]}`,
			wantFound: true,
		},
		{
			name:      "load_tools present",
			body:      `{"tools":[{"type":"function","function":{"name":"load_tools"}}]}`,
			wantFound: true,
		},
		{
			name:      "both meta-tools present",
			body:      `{"tools":[{"type":"function","function":{"name":"list_categories"}},{"type":"function","function":{"name":"load_tools"}}]}`,
			wantFound: true,
		},
		{
			name:      "no meta-tool",
			body:      `{"tools":[{"type":"function","function":{"name":"search"}}]}`,
			wantFound: false,
		},
		{
			name:      "no tools field",
			body:      `{"messages":[]}`,
			wantFound: false,
		},
		{
			name:      "empty tools array",
			body:      `{"tools":[]}`,
			wantFound: false,
		},
		{
			name:      "meta-tool mixed with regular tool",
			body:      `{"tools":[{"type":"function","function":{"name":"list_categories"}},{"type":"function","function":{"name":"search_files"}}]}`,
			wantFound: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req map[string]interface{}
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("parse: %v", err)
			}
			toolsRaw, ok := req["tools"].([]interface{})
			if !ok {
				if tc.wantFound {
					t.Error("expected tools array, got none")
				}
				return
			}
			found := false
			for _, raw := range toolsRaw {
				tMap, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				fn, ok := tMap["function"].(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := fn["name"].(string)
				if MetaToolNames[name] {
					found = true
					break
				}
			}
			if found != tc.wantFound {
				t.Errorf("found=%v, want=%v", found, tc.wantFound)
			}
		})
	}
}

// TestIsMetaTool exercises the small helper used during detection.
func TestIsMetaTool(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"list_categories", true},
		{"load_tools", true},
		{"search_files", false},
		{"", false},
		{"List_Categories", false}, // case-sensitive
	}
	for _, tc := range cases {
		if got := isMetaTool(tc.in); got != tc.want {
			t.Errorf("isMetaTool(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
