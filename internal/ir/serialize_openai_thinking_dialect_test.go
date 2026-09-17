package ir

import (
	"encoding/json"
	"testing"
)

// TestSerializeOpenAI_ThinkingDialectByTargetProvider 回归 2026-09-18 MiniMax
// thinking 事故的接线层缺陷：
//
// executor 的 OpenAI 入向 legacy 路径（finalizeOpenAIUpstreamBody）此前从未给
// irReq.TargetProvider 赋值，restoreExtensions → resolveTargetDialect 回退到
// openai_chat，paramreg 对 thinking 的 MiniMax 翻译（enabled→adaptive 最小对象）
// 从不生效，上游持续 400。本测试钉死「TargetProvider 决定出向方言」这条链路：
// 同一个 IR，TargetProvider="" 时原样透传，TargetProvider="minimax" 时翻译。
func TestSerializeOpenAI_ThinkingDialectByTargetProvider(t *testing.T) {
	body := []byte(`{
		"model": "minimax-m3",
		"messages": [{"role":"user","content":"hi"}],
		"thinking": {"type":"enabled","budget_tokens":1024},
		"max_tokens": 64
	}`)

	irReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	if _, ok := irReq.Extensions["thinking"]; !ok {
		t.Fatal("thinking 应进入 Extensions（parse_openai 不消费它）")
	}

	// 场景 A：TargetProvider 缺失（修复前所有 legacy 路径的实际状态）
	// → 方言回退 openai_chat → 原样透传 enabled（其它仍接受 enabled 的
	// 上游不受影响；MiniMax 上游必须由 executor 侧设置 TargetProvider）。
	noTarget, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI (no target): %v", err)
	}
	assertThinkingType(t, noTarget, "enabled")

	// 场景 B：TargetProvider=minimax（修复后 executor 的赋值）
	// → thinking 改写为最小对象 {"type":"adaptive"}，budget_tokens 剥离。
	minimaxReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI (minimax): %v", err)
	}
	minimaxReq.TargetProvider = "minimax"
	minimaxBody, err := SerializeOpenAI(minimaxReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI (minimax): %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(minimaxBody, &m); err != nil {
		t.Fatalf("unmarshal minimax body: %v", err)
	}
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("minimax body 缺少 thinking 对象: %s", minimaxBody)
	}
	if th["type"] != "adaptive" {
		t.Errorf("minimax thinking.type=%v, want adaptive", th["type"])
	}
	if len(th) != 1 {
		t.Errorf("minimax thinking 应为最小对象(仅 type), got %v", th)
	}

	// 场景 C：TargetProvider=deepseek（仍接受 enabled 的方言）→ 不改写。
	dsReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI (deepseek): %v", err)
	}
	dsReq.TargetProvider = "deepseek"
	dsBody, err := SerializeOpenAI(dsReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI (deepseek): %v", err)
	}
	assertThinkingType(t, dsBody, "enabled")
}

func assertThinkingType(t *testing.T, body []byte, want string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("body 缺少 thinking 对象: %s", body)
	}
	if th["type"] != want {
		t.Errorf("thinking.type=%v, want %q", th["type"], want)
	}
}