package ir

// 命名强制 tool_choice 的全序列化器矩阵（2026-10-13 gpt-5.6-sol 事故）。
//
// 背景：IR ToolChoice.Type 存有方言值——chat 入向的
// {type:"function",function:{name}} 与 responses 入向的
// {type:"function",name} 都存 Type="function"。GAP-2（round 31, 2026-10-02）
// 补齐了 serialize_openai / serialize_anthropic 的 "function" 分支，
// serialize_responses 漏网：裸字符串 "function" 被 Responses API 400 拒绝
// （生产实证：gpt-5.6-sol "Invalid value: 'function'. Supported values
// are: 'none', 'auto', and 'required'."），ba1987a4d 的 proactive
// chat→responses 升级把带强制 tool_choice 的 chat 客户端引流到该转换器
// 后从休眠变成必踩。serialize_gemini 的同类缺口是语义级：落到 default
// 变 AUTO，强制语义静默丢失。
import (
	"encoding/json"
	"testing"
)

func mustParseAndSerializeResponses(t *testing.T, body string) map[string]any {
	t.Helper()
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	return m
}

// chat 客户端带命名强制 tool_choice 经 proactive chat→responses 转换：
// 必须输出对象形态 {"type":"function","name":...}，不得是裸字符串。
func TestMatrix_ForcedToolChoice_ChatToResponses_ObjectForm(t *testing.T) {
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"weather?"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],` +
		`"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`
	m := mustParseAndSerializeResponses(t, body)
	tc, ok := m["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %T(%v), want object form —— 裸字符串会被 Responses API 400", m["tool_choice"], m["tool_choice"])
	}
	if tc["type"] != "function" || tc["name"] != "get_weather" {
		t.Fatalf("tool_choice = %v, want {type:function name:get_weather}", tc)
	}
	// tools 定义同步保留（平铺形态）。
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 entry", m["tools"])
	}
}

// responses 原生客户端 roundtrip：{type:"function",name} 解析回 IR 后
// 不得退化成裸字符串（parseResponsesToolChoice 同样存 Type="function"）。
func TestMatrix_ForcedToolChoice_ResponsesRoundtrip_ObjectForm(t *testing.T) {
	req, err := ParseResponses([]byte(`{"model":"gpt-5.6-sol","input":"weather?",` +
		`"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],` +
		`"tool_choice":{"type":"function","name":"get_weather"}}`))
	if err != nil {
		t.Fatalf("parse responses: %v", err)
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	tc, ok := m["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %T(%v), want object form", m["tool_choice"], m["tool_choice"])
	}
	if tc["name"] != "get_weather" {
		t.Fatalf("tool_choice name = %v, want get_weather", tc["name"])
	}
}

// 宽容值不受影响：auto/none/required 仍是裸字符串。
func TestMatrix_ForcedToolChoice_LooseValuesStayStrings(t *testing.T) {
	for _, v := range []string{"auto", "none", "required"} {
		m := mustParseAndSerializeResponses(t, `{"model":"m","messages":[{"role":"user","content":"x"}],"tool_choice":"`+v+`"}`)
		if s, ok := m["tool_choice"].(string); !ok || s != v {
			t.Fatalf("tool_choice(%s) = %v, want bare string %q", v, m["tool_choice"], v)
		}
	}
}

// anthropic "any" → responses "required"（既有归一化不回归）。
func TestMatrix_ForcedToolChoice_AnthropicAnyMapsToRequired(t *testing.T) {
	req, err := ParseAnthropic([]byte(`{"model":"m","max_tokens":8,` +
		`"tool_choice":{"type":"any"},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`))
	if err != nil {
		t.Fatalf("parse anthropic: %v", err)
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v, want required", m["tool_choice"])
	}
}

// gemini 目标：命名强制形态必须映射 mode=ANY + allowedFunctionNames，
// 不得落到 default 变 AUTO（强制语义丢失）。
func TestMatrix_ForcedToolChoice_GeminiAnyWithAllowedNames(t *testing.T) {
	req, err := ParseOpenAI([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],` +
		`"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := SerializeGemini(req)
	if err != nil {
		t.Fatalf("serialize gemini: %v", err)
	}
	var m struct {
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode                string   `json:"mode"`
				AllowedFunctionNames []string `json:"allowedFunctionNames"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	cfg := m.ToolConfig.FunctionCallingConfig
	if cfg.Mode != "ANY" {
		t.Fatalf("gemini mode = %q, want ANY（强制语义不得丢失）", cfg.Mode)
	}
	if len(cfg.AllowedFunctionNames) != 1 || cfg.AllowedFunctionNames[0] != "get_weather" {
		t.Fatalf("allowedFunctionNames = %v, want [get_weather]", cfg.AllowedFunctionNames)
	}
}

// 事故现场的等比复现（gpt-5.6-sol，chat 客户端 + 34 工具 + 命名强制）：
// 完整会话经 IR 管线后 tools/配对/强制选择全部保真。判据与生产 400 对照：
// 修复前出站 tool_choice 为裸 "function"（上游必 400），修复后为对象形态。
func TestMatrix_ForcedToolChoice_SolIncidentShape(t *testing.T) {
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"audit please"}],` +
		`"tools":[{"type":"function","function":{"name":"Agent","parameters":{"type":"object","$schema":"https://json-schema.org/draft/2020-12/schema","required":["description","prompt"],"properties":{"prompt":{"type":"string"},"description":{"type":"string"}}}}},` +
		`{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}}],` +
		`"tool_choice":"auto","stream":true}`
	req, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fixed := ValidateAndFixRequest(req, "sol-incident")
	fixed.Model = "gpt-5.6-sol"
	fixed.TargetProvider = "unknown-relay"
	out, err := SerializeOpenAI(fixed)
	if err != nil {
		t.Fatalf("serialize openai: %v", err)
	}
	var m struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("outbound not json: %v", err)
	}
	if len(m.Tools) != 2 {
		t.Fatalf("tools = %d, want 2（含 $schema 的 schema 必须原样保留）", len(m.Tools))
	}
	if string(m.ToolChoice) != `"auto"` {
		t.Fatalf("tool_choice = %s, want \"auto\"", m.ToolChoice)
	}
	// $schema 等 schema 内部键不得被剥（出站 tools 的 parameters 原样）。
	var tool0 struct {
		Function struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	}
	_ = json.Unmarshal(m.Tools[0], &tool0)
	if _, ok := tool0.Function.Parameters["$schema"]; !ok {
		t.Fatalf("$schema lost from tool parameters: %v", tool0.Function.Parameters)
	}
}
