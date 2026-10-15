package sanitize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestManagedInputFieldsReachRealProtocolLeaves(t *testing.T) {
	const value = "VALUE-1234"
	for _, tc := range []struct {
		name, path, field, tool, body string
	}{
		{"chat-content", "/v1/chat/completions", "content", "", `{"messages":[{"role":"user","content":"selected VALUE-1234","reasoning_content":"other VALUE-1234"}]}`},
		{"chat-reasoning", "/v1/chat/completions", "reasoning_content", "", `{"messages":[{"role":"assistant","content":"other VALUE-1234","reasoning_content":"selected VALUE-1234"}]}`},
		{"chat-text-block", "/v1/chat/completions", "text", "", `{"messages":[{"role":"user","content":[{"type":"text","text":"selected VALUE-1234"}],"reasoning_content":"other VALUE-1234"}]}`},
		{"chat-content-sibling-context", "/v1/chat/completions", "content", "", `{"messages":[{"role":"user","content":[{"type":"text","text":"other VALUE-1234"},"selected VALUE-1234"]}]}`},
		{"chat-text-sibling-context", "/v1/chat/completions", "text", "", `{"messages":[{"role":"user","content":[{"type":"text","text":"selected VALUE-1234"},"other VALUE-1234"]}]}`},
		{"messages-system", "/v1/messages", "system", "", `{"system":"selected VALUE-1234","messages":[{"role":"user","content":"other VALUE-1234"}]}`},
		{"messages-content", "/v1/messages", "content", "", `{"system":"other VALUE-1234","messages":[{"role":"user","content":"selected VALUE-1234"}]}`},
		{"messages-system-text", "/v1/messages", "text", "", `{"system":[{"type":"text","text":"selected VALUE-1234"}],"messages":[{"role":"user","content":"other VALUE-1234"}]}`},
		{"messages-thinking", "/v1/messages", "thinking", "", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"selected VALUE-1234"},{"type":"text","text":"other VALUE-1234"}]}]}`},
		{"messages-refusal", "/v1/messages", "refusal", "", `{"messages":[{"role":"assistant","content":[{"type":"refusal","refusal":"selected VALUE-1234"},{"type":"text","text":"other VALUE-1234"}]}]}`},
		{"messages-tool-result", "/v1/messages", "content", "", `{"system":"other VALUE-1234","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"selected VALUE-1234"}]}]}`},
		{"responses-instructions", "/v1/responses", "instructions", "", `{"instructions":"selected VALUE-1234","input":"other VALUE-1234"}`},
		{"responses-input", "/v1/responses", "input", "", `{"instructions":"other VALUE-1234","input":"selected VALUE-1234"}`},
		{"responses-message-content", "/v1/responses", "content", "", `{"instructions":"other VALUE-1234","input":[{"type":"message","role":"user","content":"selected VALUE-1234"}]}`},
		{"responses-text-item", "/v1/responses", "text", "", `{"instructions":"other VALUE-1234","input":[{"type":"input_text","text":"selected VALUE-1234"}]}`},
		{"responses-reasoning-summary", "/v1/responses", "text", "", `{"instructions":"other VALUE-1234","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"selected VALUE-1234"}]}]}`},
		{"responses-reasoning-content", "/v1/responses", "content", "", `{"instructions":"other VALUE-1234","input":[{"type":"reasoning","content":"selected VALUE-1234"}]}`},
		{"responses-output", "/v1/responses", "output", "", `{"instructions":"other VALUE-1234","input":[{"type":"function_call_output","output":"selected VALUE-1234","call_id":"x"}]}`},
		{"responses-mcp-output", "/v1/responses", "output", "", `{"instructions":"other VALUE-1234","input":[{"type":"mcp_call","name":"SomeTool","output":"selected VALUE-1234"}]}`},
		{"responses-mcp-tool", "/v1/responses", "command", "SomeTool", `{"input":[{"type":"mcp_call","name":"SomeTool","arguments":{"command":"selected VALUE-1234"}},{"type":"mcp_call","name":"OtherTool","arguments":{"command":"other VALUE-1234"}}]}`},
		{"responses-mcp-tool-sibling", "/v1/responses", "command", "SomeTool", `{"input":[{"type":"mcp_call","name":"SomeTool","arguments":{"command":"selected VALUE-1234"}},{"type":"shell_call","action":{"command":"other VALUE-1234"}}]}`},
		{"responses-custom-plain-input", "/v1/responses", "input", "SomeTool", `{"input":[{"type":"custom_tool_call","name":"SomeTool","input":"selected VALUE-1234"},{"type":"custom_tool_call","name":"OtherTool","input":"other VALUE-1234"}]}`},
		{"responses-function-plain-arguments", "/v1/responses", "arguments", "SomeTool", `{"input":[{"type":"function_call","name":"SomeTool","arguments":"selected VALUE-1234"},{"type":"function_call","name":"OtherTool","arguments":"other VALUE-1234"}]}`},
		{"responses-mcp-plain-arguments", "/v1/responses", "arguments", "SomeTool", `{"input":[{"type":"mcp_call","name":"SomeTool","arguments":"selected VALUE-1234"},{"type":"mcp_call","name":"OtherTool","arguments":"other VALUE-1234"}]}`},
		{"chat-function-plain-arguments", "/v1/chat/completions", "arguments", "SomeTool", `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"SomeTool","arguments":"selected VALUE-1234"}},{"function":{"name":"OtherTool","arguments":"other VALUE-1234"}}]}]}`},
		{"responses-arguments-subkeys", "/v1/responses", "arguments", "SomeTool", `{"input":[{"type":"function_call","name":"SomeTool","arguments":"selected VALUE-1234"},{"type":"function_call","name":"SomeTool","arguments":{"command":"other VALUE-1234"}}]}`},
		{"responses-custom-input-subkeys", "/v1/responses", "input", "SomeTool", `{"input":[{"type":"custom_tool_call","name":"SomeTool","input":"selected VALUE-1234"},{"type":"custom_tool_call","name":"SomeTool","input":{"text":"other VALUE-1234"}}]}`},
		{"responses-arguments-encoded-subkeys", "/v1/responses", "arguments", "SomeTool", `{"input":[{"type":"function_call","name":"SomeTool","arguments":"selected VALUE-1234"},{"type":"function_call","name":"SomeTool","arguments":"{\"command\":\"other VALUE-1234\"}"}]}`},
		{"responses-custom-encoded-subkeys", "/v1/responses", "input", "SomeTool", `{"input":[{"type":"custom_tool_call","name":"SomeTool","input":"selected VALUE-1234"},{"type":"custom_tool_call","name":"SomeTool","input":"{\"text\":\"other VALUE-1234\"}"}]}`},
		{"responses-custom-encoded-array", "/v1/responses", "input", "SomeTool", `{"input":[{"type":"custom_tool_call","name":"SomeTool","input":"[\"selected VALUE-1234\"]"},{"type":"custom_tool_call","name":"OtherTool","input":"[\"other VALUE-1234\"]"}]}`},
		{"responses-arguments-encoded-array", "/v1/responses", "arguments", "SomeTool", `{"input":[{"type":"function_call","name":"SomeTool","arguments":"[\"selected VALUE-1234\"]"},{"type":"function_call","name":"OtherTool","arguments":"[\"other VALUE-1234\"]"}]}`},
		{"responses-web-query", "/v1/responses", "query", "", `{"instructions":"other VALUE-1234","input":[{"type":"web_search_call","action":{"query":"selected VALUE-1234"}}]}`},
		{"responses-web-results", "/v1/responses", "results", "", `{"instructions":"other VALUE-1234","input":[{"type":"web_search_call","action":{"results":["selected VALUE-1234"]}}]}`},
		{"responses-web-result-text", "/v1/responses", "text", "", `{"instructions":"other VALUE-1234","input":[{"type":"web_search_call","action":{"results":[{"text":"selected VALUE-1234"}]}}]}`},
		{"responses-file-queries", "/v1/responses", "queries", "", `{"instructions":"other VALUE-1234","input":[{"type":"file_search_call","queries":["selected VALUE-1234"]}]}`},
		{"responses-file-result", "/v1/responses", "text", "", `{"instructions":"other VALUE-1234","input":[{"type":"file_search_call","results":[{"text":"selected VALUE-1234"}]}]}`},
		{"responses-shell-result", "/v1/responses", "text", "", `{"instructions":"other VALUE-1234","input":[{"type":"shell_call_output","outputs":[{"text":"selected VALUE-1234"}]}]}`},
		{"responses-code", "/v1/responses", "code", "", `{"instructions":"other VALUE-1234","input":[{"type":"code_interpreter_call","code":"selected VALUE-1234"}]}`},
		{"responses-logs", "/v1/responses", "logs", "", `{"instructions":"other VALUE-1234","input":[{"type":"code_interpreter_call","outputs":[{"logs":"selected VALUE-1234"}]}]}`},
		{"responses-patch-content", "/v1/responses", "content", "", `{"instructions":"other VALUE-1234","input":[{"type":"apply_patch_call","action":{"type":"create","content":"selected VALUE-1234"}}]}`},
		{"completions-prompt", "/v1/completions", "prompt", "", `{"prompt":"selected VALUE-1234","extension":"other VALUE-1234"}`},
		{"completions-prompt-array", "/v1/completions", "prompt", "", `{"prompt":["selected VALUE-1234"],"extension":"other VALUE-1234"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewPatternDetector()
			rule := sanitizerules.Rule{ID: "input-field", Name: "Scoped input", Type: "secret", Kind: "pattern", Pattern: `VALUE-[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true, TenantID: "tenant-a"}
			if tc.tool != "" {
				rule.Tools = []string{tc.tool}
			}
			m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), sanitizerules.Config{Rules: []sanitizerules.Rule{rule}}, sanitizerules.Options{})
			if err != nil {
				t.Fatal(err)
			}
			d.SetRuleManager(m)
			if err := m.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx := WithAuthenticatedTenant(context.Background(), "tenant-a")
			// Prove the input detector sees this exact value before applying fields.
			if hits, err := d.DetectScope(WithRuleField(ctx, tc.tool, ""), value, ScopeInputDeclarations); err != nil || len(hits) != 1 {
				t.Fatal("detector precondition absent", hits, err)
			}
			rule.Fields = []string{tc.field}
			if _, err := m.Publish(ctx, 1, sanitizerules.Config{Rules: []sanitizerules.Rule{rule}}, "tester", "field-scoped"); err != nil {
				t.Fatal(err)
			}
			s, _ := NewSanitizer(d)
			rdb := setupSaniGuardRedis(t)
			middleware, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			forwarded := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				forwarded <- string(body)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			handler := middleware.Wrap(httputil.NewSingleHostReverseProxy(target))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Gw-Session-Id", "field-scope-session")
			req = req.WithContext(m.Pin(ctx))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatal("request failed before upstream", rec.Code, rec.Body.String())
			}
			out := <-forwarded
			if strings.Count(out, value) != 1 || strings.Count(out, "{SENSITIVE:secret:") != 1 ||
				!strings.Contains(out, "selected {SENSITIVE:secret:") || !strings.Contains(out, "other VALUE-1234") {
				t.Errorf("field %q must mask only its actual leaf, preserving same value in the other field: %s", tc.field, out)
			}
			mapping, err := rdb.HGetAll(ctx, sanitizeMapKey("tenant-a", "field-scope-session")).Result()
			found := false
			for _, original := range mapping {
				found = found || original == value
			}
			if err != nil || !found {
				t.Error("selected field lost its complete restorable value", mapping, err)
			}
		})
	}
}

func TestManagedCredentialJSONStringRemainsCompleteInputLeaf(t *testing.T) {
	testManagedJSONStringCompleteLeaf(t, "password", false)
}

func TestManagedCustomFieldJSONStringRemainsCompleteInputLeaf(t *testing.T) {
	testManagedJSONStringCompleteLeaf(t, "auth_blob", true)
}

func testManagedJSONStringCompleteLeaf(t *testing.T, field string, custom bool) {
	t.Helper()
	const original = `{"foo":"R7#kQ92!"}`
	args := map[string]any{field: original}
	encodedArgs, _ := json.Marshal(args)
	for _, tc := range []struct {
		name, path string
		body       map[string]any
	}{
		{"chat", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "Write", "arguments": string(encodedArgs)}}}}}}},
		{"messages", "/v1/messages", map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Write", "input": args}}}}}},
		{"responses", "/v1/responses", map[string]any{"input": []any{map[string]any{"type": "function_call", "name": "Write", "arguments": string(encodedArgs)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, m := managedFixture(t)
			ctx := WithAuthenticatedTenant(context.Background(), "tenant-a")
			if custom {
				cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "custom-json-field", Name: "Custom JSON field", Type: "secret", Kind: "pattern", Pattern: `(?s).+`, Scopes: []string{"input"}, Fields: []string{field}, Action: "mask", Enabled: true}}}
				if _, err := m.Publish(ctx, 1, cfg, "tester", "custom field"); err != nil {
					t.Fatal(err)
				}
			}
			if hits, err := d.DetectScope(WithRuleField(ctx, "Write", field), original, ScopeInputDeclarations); err != nil || len(hits) == 0 {
				t.Fatal("configured field rule cannot see the complete string", hits, err)
			}
			s, _ := NewSanitizer(d)
			rdb := setupSaniGuardRedis(t)
			middleware, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			forwarded := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				forwarded <- string(body)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(body)))
			req.Header.Set("X-Gw-Session-Id", "credential-field-session")
			req = req.WithContext(m.Pin(ctx))
			rec := httptest.NewRecorder()
			middleware.Wrap(httputil.NewSingleHostReverseProxy(target)).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatal("credential request failed before upstream", rec.Code, rec.Body.String())
			}
			out := <-forwarded
			if strings.Contains(out, "R7#kQ92!") || !strings.Contains(out, "{SENSITIVE:secret:") {
				t.Errorf("JSON-looking %s bypassed the actual field rule: %s", field, out)
			}
			mapping, err := rdb.HGetAll(ctx, sanitizeMapKey("tenant-a", "credential-field-session")).Result()
			found := false
			for _, value := range mapping {
				found = found || value == original
			}
			if err != nil || !found {
				t.Errorf("%s mapping must retain the full original JSON string: %v %v", field, mapping, err)
			}
		})
	}
}
