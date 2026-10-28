package ir

import (
	"encoding/json"
	"testing"
)

// 原厂直通轮（2026-10-28）：NativePassthrough 置位时 Extensions 全量
// 原样还原，不再做方言裁剪 / thinking 翻译。闸门对照臂证明门有牙
// （关闭时网关仍在改写原厂自己的参数）——否则「全量还原」可能只是恒真断言。

func TestRestoreExtensions_NativePassthrough_KeepsDialectPrivateFields(t *testing.T) {
	// thinking 是 MiniMax 方言的私有形态：非原生路径上 paramreg 会把它
	// 翻译成 MiniMax 认的形状（enabled → adaptive，budget_tokens 丢弃）。
	// 原厂直通（minimax-code↔minimax）下必须原样保留。
	newReq := func(native bool) *InternalRequest {
		req := &InternalRequest{
			Model:          "minimax-m2",
			TargetProvider: "minimax",
			SourceProtocol: ProtocolOpenAIChat,
		}
		if native {
			req.NativePassthrough = true
		}
		req.Extensions = map[string]json.RawMessage{
			"thinking": json.RawMessage(`{"type":"enabled","budget_tokens":1024}`),
		}
		return req
	}

	// 对照臂：未置位时，thinking 被网关改写为 {"type":"adaptive"}。
	off, err := SerializeOpenAI(newReq(false))
	if err != nil {
		t.Fatalf("SerializeOpenAI(off): %v", err)
	}
	var offObj map[string]any
	if err := json.Unmarshal(off, &offObj); err != nil {
		t.Fatalf("unmarshal off: %v", err)
	}
	offThinking, ok := offObj["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("non-native: thinking missing or not an object: %s", off)
	}
	if offThinking["type"] != "adaptive" {
		t.Fatalf("non-native: thinking.type = %v, want \"adaptive\" (paramreg translation must keep its teeth)", offThinking["type"])
	}
	if _, present := offThinking["budget_tokens"]; present {
		t.Fatalf("non-native: thinking.budget_tokens survived the translation: %s", off)
	}

	// 正臂：置位时原样保留（thinking 对象一字不改）。
	on, err := SerializeOpenAI(newReq(true))
	if err != nil {
		t.Fatalf("SerializeOpenAI(on): %v", err)
	}
	var onObj map[string]any
	if err := json.Unmarshal(on, &onObj); err != nil {
		t.Fatalf("unmarshal on: %v", err)
	}
	thinking, ok := onObj["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("native: thinking missing or not an object: %s", on)
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("native: thinking.type = %v, want \"enabled\" (verbatim)", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("native: thinking.budget_tokens = %v, want 1024 (verbatim)", thinking["budget_tokens"])
	}
}

// 未登记字段在两条路径上都要透传（paramreg 的 fail-open 承诺不得被
// NativePassthrough 破坏 —— 它只是把「已知但方言不认」的字段也放行）。
func TestRestoreExtensions_NativePassthrough_KeepsUnregisteredFields(t *testing.T) {
	req := &InternalRequest{
		Model:             "glm-5.3",
		TargetProvider:    "zhipu",
		SourceProtocol:    ProtocolOpenAIChat,
		NativePassthrough: true,
		Extensions: map[string]json.RawMessage{
			"zcode_brand_new_param": json.RawMessage(`{"x":1}`),
		},
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := obj["zcode_brand_new_param"]; !present {
		t.Fatalf("native: unregistered field dropped: %s", out)
	}
}
