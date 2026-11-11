// parse_anthropic_openai_tool_default_schema_test.go —— 嵌套 function 工具缺省 input_schema 的钉测
// （★ 2026-11-11 48h 审计 P1）
//
// ★ 缺陷：★ f1a3caab1 引入的嵌套 function 归一化（parse_anthropic.go）在
// ★ `function.parameters` 缺失时把 td.Parameters 置 nil ⇒ ★ Anthropic 序列化器
// ★ 不发 input_schema ⇒ ★ Anthropic 对 custom tool 的 input_schema 必填 ⇒ 400。
// ★ streaming 层同场景历来有缺省（tools_normalize.go），IR 层漏 ⇒ 「同一缺陷只修一半」。
//
// ★ 修法（双层）：★ 解析层 parameters 缺失/非对象 ⇒ 缺省
// ★ {"type":"object","properties":{}}；★ 序列化层 Parameters==nil ⇒ 同款兜底。
//
// ★ 变异臂：★ 去掉两层缺省中的任何一层 ⇒ 用例红。

package ir

import (
	"encoding/json"
	"testing"
)

func TestParseAnthropic_OpenAINestedToolWithoutParametersDefaultsSchema(t *testing.T) {
	// 客户端把 OpenAI 形态的工具发到 Anthropic 端点，且 function 无 parameters。
	body := []byte(`{
		"model": "claude-sonnet-5",
		"max_tokens": 64,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{"type": "function", "function": {"name": "ping", "description": "no-arg tool"}},
			{"type": "function", "function": {"name": "echo", "parameters": {"type": "object", "properties": {"text": {"type": "string"}}}}}
		]
	}`)
	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(req.Tools))
	}

	// ★ 解析层缺省：无 parameters 的工具必须拿到非 nil 的空对象 schema
	if len(req.Tools[0].Parameters) == 0 {
		t.Fatalf("Tools[0].Parameters empty (P1: nil Parameters ⇒ 序列化漏发 input_schema ⇒ Anthropic 400)")
	}
	var p0 map[string]any
	if err := json.Unmarshal(req.Tools[0].Parameters, &p0); err != nil {
		t.Fatalf("Tools[0].Parameters not JSON: %v", err)
	}
	if p0["type"] != "object" {
		t.Errorf("default schema type = %v, want object", p0["type"])
	}
	// 带参数的工具不受影响
	var p1 map[string]any
	if err := json.Unmarshal(req.Tools[1].Parameters, &p1); err != nil {
		t.Fatalf("Tools[1].Parameters not JSON: %v", err)
	}
	if _, ok := p1["properties"]; !ok {
		t.Errorf("Tools[1].Parameters lost properties: %v", p1)
	}

	// ★ 序列化层兜底：即使 Parameters 为 nil（其他来源构造的 IR），
	// ★ Anthropic 输出的每个 function 工具都必须带 input_schema。
	req.Tools[0].Parameters = nil // ★ 直接打掉解析层缺省，单测序列化层兜底
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}
	tools, _ := outMap["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("Anthropic tools len = %d, want 2", len(tools))
	}
	for i, raw := range tools {
		tm, _ := raw.(map[string]any)
		if _, ok := tm["input_schema"]; !ok {
			t.Errorf("tools[%d].input_schema missing (Anthropic 对 custom tool 必填 ⇒ 400): %v", i, tm)
		}
	}
	// 缺省值形状：type=object + 空 properties
	defSchema, _ := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if defSchema == nil || defSchema["type"] != "object" {
		t.Errorf("tools[0].input_schema default shape wrong: %v", defSchema)
	}
}
