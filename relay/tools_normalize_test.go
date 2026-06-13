package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeOpenAIToolDefinitions_FlatResponsesShape(t *testing.T) {
	tools := []any{map[string]any{
		"type":        "function",
		"name":        "get_weather",
		"description": "weather",
		"parameters":  map[string]any{"type": "object"},
	}}
	out := NormalizeOpenAIToolDefinitions(tools)
	fn := out[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("name = %v", fn["name"])
	}
}

func TestOpenAIToolToAnthropic_StripsTypeField(t *testing.T) {
	tool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "read_file",
			"description": "read",
			"parameters":  map[string]any{"type": "object"},
		},
	}
	anth, ok := OpenAIToolToAnthropic(tool)
	if !ok {
		t.Fatal("expected conversion")
	}
	if _, hasType := anth["type"]; hasType {
		t.Fatalf("anthropic tool must not carry type field: %v", anth)
	}
	if anth["name"] != "read_file" {
		t.Fatalf("name = %v", anth["name"])
	}
	if _, hasSchema := anth["input_schema"]; !hasSchema {
		t.Fatal("expected input_schema")
	}
}

func TestSanitizeAnthropicToolsInBody_RemovesFunctionType(t *testing.T) {
	in := []byte(`{"model":"MiniMax-M3","max_tokens":256,"tools":[{
		"type":"function",
		"function":{"name":"get_weather","description":"w","parameters":{"type":"object"}}
	}],"messages":[{"role":"user","content":"hi"}]}`)
	out := SanitizeAnthropicToolsInBody(in)
	if strings.Contains(string(out), `"type":"function"`) {
		t.Fatalf("upstream body must not contain type:function in tools: %s", out)
	}
	if !strings.Contains(string(out), `"input_schema"`) {
		t.Fatalf("expected input_schema in tools: %s", out)
	}
}

func TestConvertChatRequestToAnthropic_FlatTools(t *testing.T) {
	in := []byte(`{
		"model":"minimax-m3","max_tokens":256,
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"messages":[{"role":"user","content":"北京?"}]
	}`)
	out, err := ConvertChatRequestToAnthropic(in)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	tools := v["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Fatalf("name = %v", tool["name"])
	}
	if _, ok := tool["type"]; ok {
		t.Fatalf("anthropic tool must not have type: %v", tool)
	}
}

func TestSanitizeAnthropicToolDefinitions_CustomType(t *testing.T) {
	tools := []any{map[string]any{
		"type":         "custom",
		"name":         "grep",
		"description":  "search",
		"input_schema": map[string]any{"type": "object"},
	}}
	out := SanitizeAnthropicToolDefinitions(tools)
	tool := out[0].(map[string]any)
	if _, ok := tool["type"]; ok {
		t.Fatalf("type field must be stripped: %v", tool)
	}
	if tool["name"] != "grep" {
		t.Fatalf("name = %v", tool["name"])
	}
}
