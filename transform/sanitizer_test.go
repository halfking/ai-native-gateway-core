package transform

import (
	"encoding/json"
	"testing"
)

func TestApplyRequestWhitelist_NoOp(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"stream":true}`)
	result := ApplyRequestWhitelist(body, nil, nil)
	if string(result) != string(body) {
		t.Errorf("empty lists should be no-op")
	}
}

func TestApplyRequestWhitelist_Passthrough(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"stream":true,"extra_field":"remove","max_tokens":100}`)
	result := ApplyRequestWhitelist(body, []string{"model", "messages", "stream"}, nil)

	var obj map[string]any
	json.Unmarshal(result, &obj)

	if _, ok := obj["extra_field"]; ok {
		t.Error("extra_field should be removed")
	}
	if _, ok := obj["max_tokens"]; !ok {
		t.Error("max_tokens should be kept (always_keep)")
	}
	if _, ok := obj["model"]; !ok {
		t.Error("model should be kept")
	}
}

func TestApplyRequestWhitelist_StripOnly(t *testing.T) {
	body := []byte(`{"model":"gpt-4","parallel_tool_calls":true,"reasoning_effort":"high"}`)
	result := ApplyRequestWhitelist(body, nil, []string{"parallel_tool_calls", "reasoning_effort"})

	var obj map[string]any
	json.Unmarshal(result, &obj)

	if _, ok := obj["parallel_tool_calls"]; ok {
		t.Error("parallel_tool_calls should be stripped")
	}
	if _, ok := obj["reasoning_effort"]; ok {
		t.Error("reasoning_effort should be stripped")
	}
	if _, ok := obj["model"]; !ok {
		t.Error("model should remain")
	}
}

func TestApplyRequestWhitelist_BothLists(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"stream":true,"extra":"x","remove_me":"y","max_tokens":50}`)
	result := ApplyRequestWhitelist(body, []string{"model", "messages", "stream"}, []string{"extra"})

	var obj map[string]any
	json.Unmarshal(result, &obj)

	if _, ok := obj["extra"]; ok {
		t.Error("extra should be stripped")
	}
	if _, ok := obj["remove_me"]; ok {
		t.Error("remove_me not in passthrough, should be removed")
	}
	if _, ok := obj["max_tokens"]; !ok {
		t.Error("max_tokens in always_keep should survive")
	}
}

func TestNeedsToolCollapse_NoTool(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`)
	if NeedsToolCollapse(body) {
		t.Error("no tool messages should not need collapse")
	}
}

func TestNeedsToolCollapse_ToolRole(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"go"},{"role":"tool","content":"result","tool_call_id":"123"}]}`)
	if !NeedsToolCollapse(body) {
		t.Error("tool role message should need collapse")
	}
}

func TestNeedsToolCollapse_ToolCalls(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"test"}}]}]}`)
	if !NeedsToolCollapse(body) {
		t.Error("assistant with tool_calls should need collapse")
	}
}

func TestCollapseToolHistory_Basic(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"What's the weather?"},
			{"role":"assistant","tool_calls":[{"id":"tc1","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},
			{"role":"tool","content":"72F sunny","tool_call_id":"tc1"},
			{"role":"assistant","content":"It's 72F in NYC."}
		],
		"tools":[{"type":"function","function":{"name":"get_weather"}}],
		"tool_choice":"auto"
	}`)

	result := CollapseToolHistory(body)

	var obj map[string]any
	json.Unmarshal(result, &obj)

	if _, ok := obj["tools"]; ok {
		t.Error("tools should be removed after collapse")
	}
	if _, ok := obj["tool_choice"]; ok {
		t.Error("tool_choice should be removed after collapse")
	}

	msgs, _ := obj["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}

	firstMsg, _ := msgs[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Errorf("first message should be system, got %v", firstMsg["role"])
	}
}

func TestCollapseToolHistory_AttemptCompletion(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"do it"},
			{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"attempt_completion","arguments":"done!"}}]}
		]
	}`)

	result := CollapseToolHistory(body)
	var obj map[string]any
	json.Unmarshal(result, &obj)

	msgs, _ := obj["messages"].([]any)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := lastMsg["content"].(string)
	if content != "[完成] done!" {
		t.Errorf("expected attempt_completion collapse, got: %s", content)
	}
}

func TestIsToolUseCapable(t *testing.T) {
	if !IsToolUseCapable("openai") {
		t.Error("openai should be tool-use capable")
	}
	if !IsToolUseCapable("anthropic") {
		t.Error("anthropic should be tool-use capable")
	}
	if IsToolUseCapable("volcengine") {
		t.Error("volcengine should NOT be tool-use capable")
	}
}

// TestIsToolUseCapable_ProtocolFallback guards the protocol heuristic
// introduced to match the Python gateway's is_tool_use_capable():
// openai-completions and anthropic-messages carry the standard tool
// definition block, so any provider speaking one of those wire formats
// is treated as tool-capable regardless of catalog code.  This is what
// makes Xiaomi MiMo (catalog_code="xiaomi", protocol="openai-completions")
// work — without it the Go gateway would fold MiMo's prior tool history
// into "[调用工具 ...]" text, breaking the 50+ turn audit tasks the
// agent runs on this model (see request_logs id 27998 from 2026-06-10).
func TestIsToolUseCapable_ProtocolFallback(t *testing.T) {
	cases := []struct {
		catalog string
		proto   string
		want    bool
	}{
		// Protocol fallback turns on for OpenAI/Anthropic wire formats.
		{"xiaomi", "openai-completions", true},
		{"volcengine", "openai-completions", true},
		{"deepseek", "openai-completions", true},
		{"custom-anything", "anthropic-messages", true},
		// Other protocols keep the old behaviour.
		{"volcengine", "ollama-native", false},
		{"volcengine", "gemini-generate", false},
		{"volcengine", "", false},
		// No protocol arg → backwards-compatible single-arg call.
		{"xiaomi", "", false},
	}
	for _, tc := range cases {
		got := IsToolUseCapable(tc.catalog, tc.proto)
		if got != tc.want {
			t.Errorf("IsToolUseCapable(%q, %q) = %v, want %v", tc.catalog, tc.proto, got, tc.want)
		}
	}
}

func TestSimplifyTools_CanonicalShape(t *testing.T) {
	// Already canonical — should pass through unchanged.
	in := []byte(`{
		"model":"minimax-m3",
		"tools":[{
			"type":"function",
			"function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}
		}]
	}`)
	out := SimplifyTools(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Error("type should be function")
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Error("name lost")
	}
}

func TestSimplifyTools_MissingType(t *testing.T) {
	// Missing type field — should add type=function.
	in := []byte(`{
		"model":"minimax-m3",
		"tools":[{
			"name":"get_weather",
			"description":"weather",
			"parameters":{"type":"object","properties":{"city":{"type":"string"}}}
		}]
	}`)
	out := SimplifyTools(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("type should be function, got %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name lost, got %v", fn["name"])
	}
	if fn["description"] != "weather" {
		t.Errorf("description lost, got %v", fn["description"])
	}
}

func TestSimplifyTools_TopLevelFunctionFields(t *testing.T) {
	// Function fields at top level (no wrapper) — should wrap them.
	in := []byte(`{
		"model":"minimax-m3",
		"tools":[{
			"type":"function",
			"name":"get_weather",
			"description":"weather",
			"parameters":{"type":"object","properties":{"city":{"type":"string"}}}
		}]
	}`)
	out := SimplifyTools(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("type should be function, got %v", tool["type"])
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatal("function should be a map")
	}
	if fn["name"] != "get_weather" {
		t.Errorf("name lost, got %v", fn["name"])
	}
}

func TestSimplifyTools_MissingOptionalFields(t *testing.T) {
	// Missing description and parameters — should add defaults.
	in := []byte(`{
		"model":"minimax-m3",
		"tools":[{
			"type":"function",
			"function":{"name":"get_weather"}
		}]
	}`)
	out := SimplifyTools(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if fn["description"] != "" {
		t.Errorf("description should be empty string, got %v", fn["description"])
	}
	if fn["parameters"] == nil {
		t.Error("parameters should be set to default")
	}
}

func TestSimplifyTools_NoToolsField(t *testing.T) {
	// No tools field — should pass through unchanged.
	in := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}]}`)
	out := SimplifyTools(in)
	if string(out) != string(in) {
		t.Error("should be no-op when no tools field")
	}
}

func TestSimplifyTools_EmptyToolsArray(t *testing.T) {
	// Empty tools array — should pass through unchanged.
	in := []byte(`{"model":"minimax-m3","tools":[]}`)
	out := SimplifyTools(in)
	if string(out) != string(in) {
		t.Error("should be no-op when tools is empty")
	}
}

func TestSimplifyTools_MixedFormats(t *testing.T) {
	// Mix of canonical and non-canonical tools.
	in := []byte(`{
		"model":"minimax-m3",
		"tools":[
			{"type":"function","function":{"name":"tool1","description":"desc1","parameters":{"type":"object","properties":{}}}},
			{"name":"tool2","description":"desc2","parameters":{"type":"object","properties":{}}}
		]
	}`)
	out := SimplifyTools(in)
	var v map[string]any
	json.Unmarshal(out, &v)
	tools := v["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	// First tool should be unchanged (canonical).
	t1 := tools[0].(map[string]any)
	if t1["type"] != "function" {
		t.Error("tool1 type should be function")
	}
	// Second tool should be wrapped.
	t2 := tools[1].(map[string]any)
	if t2["type"] != "function" {
		t.Errorf("tool2 type should be function, got %v", t2["type"])
	}
	fn2, ok := t2["function"].(map[string]any)
	if !ok {
		t.Fatal("tool2 function should be a map")
	}
	if fn2["name"] != "tool2" {
		t.Errorf("tool2 name lost, got %v", fn2["name"])
	}
}
