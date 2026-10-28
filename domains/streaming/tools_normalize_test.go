package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// RISK-2（2026-10-29）：streaming 侧两个工具重建站点的顶层非标准键保真
// （IR 层在 internal/ir/tool_extra_test.go）。

// NormalizeOpenAIToolDefinitions：包裹形与平铺形的顶层 vendor 键都保真；
// canonical 键不重复；无 extra 的工具不塞多余键。
func TestNormalizeOpenAIToolDefinitions_ExtrasPreserved(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":true},"vendor_tool_flag":true},` +
		`{"name":"g","description":"d","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},` +
		`{"type":"function","function":{"name":"h","parameters":{"type":"object"}}}` +
		`]}`)
	out := NormalizeToolsInChatBody(body)

	var obj struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(obj.Tools) != 3 {
		t.Fatalf("tools = %d, want 3\n%s", len(obj.Tools), out)
	}
	if obj.Tools[0]["vendor_tool_flag"] != true {
		t.Fatalf("wrapper-form tool: vendor_tool_flag dropped:\n%s", out)
	}
	// strict 在 function 内部 —— streaming normalizer 对 fn 整体复用，天然保真。
	fn0, _ := obj.Tools[0]["function"].(map[string]any)
	if fn0["strict"] != true {
		t.Fatalf("wrapper-form tool: function-inner strict dropped (fn must be reused whole):\n%s", out)
	}
	if obj.Tools[1]["cache_control"] == nil {
		t.Fatalf("flat-form tool: cache_control dropped:\n%s", out)
	}
	if fn1, ok := obj.Tools[1]["function"].(map[string]any); !ok || fn1["name"] != "g" {
		t.Fatalf("flat-form tool not rebuilt to canonical shape:\n%s", out)
	}
	if _, leak := obj.Tools[2]["vendor_tool_flag"]; leak {
		t.Fatalf("clean tool must not gain extra keys\n%s", out)
	}
	// 字节保真（UseNumber）：大整数不漂移。
	big := []byte(`{"model":"m","messages":[],"tools":[{"name":"f","parameters":{"type":"object"},"vendor_meta":{"k":9007199254740993}}]}`)
	bigOut := NormalizeToolsInChatBody(big)
	if !strings.Contains(string(bigOut), "9007199254740993") {
		t.Fatalf("big integer drifted (float64 round trip):\n%s", bigOut)
	}
}

// SanitizeAnthropicToolDefinitions：两个分支的顶层 vendor 键都保真。
func TestSanitizeAnthropicToolDefinitions_ExtrasPreserved(t *testing.T) {
	wrapper := []any{map[string]any{
		"type":             "function",
		"function":         map[string]any{"name": "f", "parameters": map[string]any{"type": "object"}},
		"vendor_tool_flag": true,
	}}
	out := SanitizeAnthropicToolDefinitions(wrapper)
	tool0, _ := out[0].(map[string]any)
	if tool0["vendor_tool_flag"] != true {
		t.Fatalf("wrapper branch: vendor_tool_flag dropped: %#v", out[0])
	}
	if tool0["input_schema"] == nil {
		t.Fatalf("wrapper branch: canonical translation broken: %#v", out[0])
	}

	flat := []any{map[string]any{
		"name": "g", "description": "d",
		"input_schema":  map[string]any{"type": "object"},
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
	out2 := SanitizeAnthropicToolDefinitions(flat)
	tool1, _ := out2[0].(map[string]any)
	if tool1["cache_control"] == nil {
		t.Fatalf("flat branch: cache_control dropped: %#v", out2[0])
	}
	if tool1["name"] != "g" {
		t.Fatalf("flat branch: canonical shape broken: %#v", out2[0])
	}
}
