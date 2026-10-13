package transformation

import (
	"encoding/json"
	"testing"
)

// r1024 判据：chat 嵌套形 tools 在 /v1/responses 出站必须被归一化为平铺形；
// 已平铺的必须原字节不动（前提在场配对：两种输入、两种期望）。
func TestNormalizeResponsesTools(t *testing.T) {
	nested := []byte(`{"model":"m","instructions":"sys","input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_config","description":"d","parameters":{"type":"object"},"strict":true}}],"stream":true}`)
	out := NormalizeResponsesTools(nested)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var tools []map[string]any
	if err := json.Unmarshal(obj["tools"], &tools); err != nil {
		t.Fatalf("tools unmarshal: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools len=%d", len(tools))
	}
	tool := tools[0]
	if tool["name"] != "get_config" {
		t.Errorf("name=%v want get_config", tool["name"])
	}
	if tool["description"] != "d" {
		t.Errorf("description lost: %v", tool["description"])
	}
	if tool["strict"] != true {
		t.Errorf("strict lost: %v", tool["strict"])
	}
	params, _ := tool["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("parameters lost: %v", tool["parameters"])
	}
	if _, has := tool["function"]; has {
		t.Error("nested function wrapper survived")
	}
	// 信封其余字段不受影响。
	if string(obj["model"]) != `"m"` {
		t.Errorf("model damaged: %s", obj["model"])
	}

	// 已平铺：原字节返回。
	flat := []byte(`{"model":"m","tools":[{"type":"function","name":"get_config","parameters":{"type":"object"}}]}`)
	if got := NormalizeResponsesTools(flat); string(got) != string(flat) {
		t.Errorf("flat input must pass through unchanged, got %s", got)
	}
	// 无 tools / 无 name 的畸形嵌套：不动。
	other := []byte(`{"model":"m","input":[]}`)
	if got := NormalizeResponsesTools(other); string(got) != string(other) {
		t.Errorf("no-tools body must pass through unchanged")
	}
	broken := []byte(`{"model":"m","tools":[{"type":"function","function":{"parameters":{}}}]}`)
	if got := NormalizeResponsesTools(broken); string(got) != string(broken) {
		t.Errorf("nameless nested tool must pass through (no invented name)")
	}
}
