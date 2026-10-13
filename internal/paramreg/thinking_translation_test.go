package paramreg

import (
	"encoding/json"
	"testing"
)

// thinking 翻译轮判据（2026-10-13，数据矩阵见
// docs/audit/2026-10-13-Vapeur全量测试轮.md §七）。
//
// 三项策略演进：
//  1. thinking → responses/grok 方言：裁剪（r1021）升级为 effort 翻译；
//  2. KindPortable 的 Dialects 白名单在 Decide 真正生效（R52 对齐）；
//  3. enable_thinking 加 RejectedByUnknown（Vapeur 三模型族台账实证）。

func TestTranslateThinkingToResponsesEffort(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // 期望的 effort；空串 = 应裁剪（disabled）
		drop  bool
	}{
		{"enabled-no-budget", `{"type":"enabled"}`, "medium", false},
		{"enabled-budget-1024", `{"type":"enabled","budget_tokens":1024}`, "low", false},
		{"enabled-budget-2048", `{"type":"enabled","budget_tokens":2048}`, "medium", false},
		{"enabled-budget-4096", `{"type":"enabled","budget_tokens":4096}`, "high", false},
		{"enabled-budget-16384-capped", `{"type":"enabled","budget_tokens":16384}`, "xhigh", false}, // max→xhigh（r1014：gpt 家族无 max）
		{"adaptive", `{"type":"adaptive"}`, "medium", false},
		{"disabled", `{"type":"disabled"}`, "", true},
		{"non-object", `"enabled"`, "medium", false},
	}
	for _, c := range cases {
		key, out, ok := TranslateThinkingForProtocol(json.RawMessage(c.value), "responses")
		if c.drop {
			if ok {
				t.Errorf("%s: disabled 意图不应渲染，got key=%q out=%s", c.name, key, out)
			}
			continue
		}
		if !ok || key != "reasoning" {
			t.Errorf("%s: want reasoning, got ok=%v key=%q", c.name, ok, key)
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil || obj["effort"] != c.want {
			t.Errorf("%s: effort want %s, got %s (err=%v)", c.name, c.want, out, err)
		}
	}
	// chat 协议形态：reasoning_effort 字符串。
	key, out, ok := TranslateThinkingForProtocol(json.RawMessage(`{"type":"enabled","budget_tokens":4096}`), "openai-chat")
	if !ok || key != "reasoning_effort" || string(out) != `"high"` {
		t.Errorf("chat 协议：want reasoning_effort=\"high\", got ok=%v key=%q out=%s", ok, key, out)
	}
	// 未知协议：不翻译（调用方走原裁剪）。
	if _, _, ok := TranslateThinkingForProtocol(json.RawMessage(`{"type":"enabled"}`), "anthropic"); ok {
		t.Error("未知协议不应翻译")
	}
}

func TestPortableDialectsKeepPassthroughSemantics(t *testing.T) {
	// 2026-10-13 裁决：Dialects 对 Portable 维持「来源标注」语义（注册表
	// 既有设计 + decide_test.go 的 keep_alive 判据钉住）；登记方言层面
	// 无跨厂商硬 400 实证不收口（r1021 教义）。钉住该语义防止将来
	// 「顺手加白名单门控」推翻已提交设计。
	for _, dst := range []Dialect{DialectQwen, DialectVLLM, DialectDeepSeek, DialectGLM} {
		if _, _, action, _ := Apply("enable_thinking", json.RawMessage(`true`), DialectOpenAIChat, dst); action != ActionRestore {
			t.Errorf("dst=%s：Portable 维持透传（收口须 RejectedBy 实证登记）：action=%v", dst, action)
		}
	}
}

func TestEnableThinkingRejectedByUnknown(t *testing.T) {
	// Vapeur（unknown-relay 哨兵）responses 腿对 gpt-5.6-sol/gpt-6.1-sol/
	// grok-4.6 实测 400（redis 台账三条）——qwen/vllm 旋钮不适用 fail-open。
	if _, _, action, _ := Apply("enable_thinking", json.RawMessage(`true`), DialectOpenAIChat, DialectUnknown); action != ActionDrop {
		t.Errorf("unknown-relay 应裁剪 enable_thinking：action=%v", action)
	}
}
