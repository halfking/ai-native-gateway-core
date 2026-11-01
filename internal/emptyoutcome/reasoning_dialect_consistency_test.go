package emptyoutcome

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// 本文件是 2026-11-02 12h 审计轮 R3 的落地门：**两种 reasoning 方言对
// 「空 delta」的判定必须一致**。
//
// ── 背景 ──────────────────────────────────────────────────────────────
//
// 2026-11-02 的生产事故修完之后，仓库新增了 `internal/ir/reasoning_alias.go`：
// Ollama 的 `/v1` 兼容层把思考轨迹放在 `delta.reasoning`，而 OpenAI 方言
// 放在 `delta.reasoning_content`。alias 让 IR 把两者统一成
// `ReasoningContent`。
//
// 而 `ChatSemanticDelta` 的契约是「**排除正在进行的 reasoning**」——它判
// `chunk.Delta.ReasoningContent == ""`。alias 落地之前：
//
//   · 标准 OpenAI 方言：ReasoningContent 有值 ⇒ 判 false（不算空 delta）
//   · Ollama 方言：IR 看不见 `reasoning` ⇒ ReasoningContent 恒空 ⇒
//     判 **true**（被当成空 delta）⇒ reasoning-only 的 burst 会累计
//     `consecutiveEmptyDeltas` ⇒ 提前判空并 failover。
//
// ⇒ 两个方言对同一件事给出**相反**的判定。这不是「alias 改变了空/非空
// 判定」——**终判一直相同**（推理不是 assistant 答案 ⇒ 仍然 failover），
// 变的是**判空的时点**。alias 落地后两边一致 ⇒ 方言被对齐。
//
// ── 这道门钉住什么 ────────────────────────────────────────────────────
//
// 钉住「alias 把两个方言对齐」这件事本身：如果将来有人给 alias 加了
// 第三个字段名、或改了 `ChatSemanticDelta` 的排除条件，两个方言会重新
// 分叉 ⇒ 门会红。
//
// ★ 门**不**断言「哪个判定是对的」——那是属主裁决（2026-10-17）的领域：
//   本门只断言「两个方言必须一样」+「reasoning 仍然不算 assistant 答案」。

// reasoningOnlyDeltas：两种方言的 reasoning-only delta。
//
// 形态取自生产实采：
//   · reasoning_content —— OpenAI 标准方言
//   · reasoning         —— Ollama /v1 兼容层（2026-11-02 实采）
var reasoningOnlyDeltas = []struct {
	name    string
	payload string
}{
	{
		name:    "openai-reasoning_content",
		payload: `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"Thinking Process: 1. analyze"},"finish_reason":null}]}`,
	},
	{
		name:    "ollama-reasoning-alias",
		payload: `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":"Thinking"},"finish_reason":null}]}`,
	},
}

// TestReasoningDialectsAgreeOnSemanticDelta 钉两个方言一致：
//
// ChatSemanticDelta 判「这是一个**空** delta」⇒ 对**正在进行的 reasoning**
// 必须一律返回 false（排除条件）。
//
// ★ 这条断言写的是「两个方言结果相同」而不是「结果等于某个常量」——
//   属主裁决若哪天改口径，门不该红，它要红的是「两个方言分叉」。
func TestReasoningDialectsAgreeOnSemanticDelta(t *testing.T) {
	var first bool
	var firstName string
	for i, tc := range reasoningOnlyDeltas {
		got := ChatSemanticDelta(tc.payload)
		if i == 0 {
			first, firstName = got, tc.name
			continue
		}
		if got != first {
			t.Errorf("reasoning 方言判定分叉：%s ⇒ %v，而 %s ⇒ %v ⇒ "+
				"同一件事（正在进行的 reasoning）在两个方言上被判成不同 ⇒ "+
				"空/非空的**时点**会因供应商而异",
				tc.name, got, firstName, first)
		}
	}
}

// TestReasoningDeltaIsNeverAssistantAnswer 钉 2026-10-17 属主裁决：
// 「推理不是 assistant 答案」⇒ reasoning-only 的 delta
// **不**算 assistant 输出（ChatChunkHasOutput 必须为 false）。
//
// ★ 这是终判口径：无论 reasoning 方言怎么变、alias 怎么改，
//   「只有推理没有答案」永远不能被当成「有答案」。
func TestReasoningDeltaIsNeverAssistantAnswer(t *testing.T) {
	for _, tc := range reasoningOnlyDeltas {
		if ChatChunkHasOutput(tc.payload) {
			t.Errorf("方言 %s：ChatChunkHasOutput = true ⇒ "+
				"reasoning 被当成了 assistant 答案 ⇒ 违反 2026-10-17 裁决"+
				"（推理不是 assistant 答案）", tc.name)
		}
	}
}

// TestReasoningAliasPopulatesIRReasoningContent 钉 alias 真的生效：
// 两种方言的 reasoning-only delta 经 IR 解析后，`ReasoningContent` 都必须
// 非空 ⇒ alias 与标准字段在 IR 层是**同一个东西**。
//
// ★ 这条同时是「两个方言一致」的**上游**证据：若 alias 某天失效
//   （Ollama 方言的 ReasoningContent 变空），上面的两条门会先红。
func TestReasoningAliasPopulatesIRReasoningContent(t *testing.T) {
	for _, tc := range reasoningOnlyDeltas {
		chunk, err := ir.ParseOpenAIStreamChunk("data: " + tc.payload + "\n\n")
		if err != nil {
			t.Fatalf("方言 %s：解析失败：%v", tc.name, err)
		}
		if chunk == nil || chunk.Delta == nil {
			t.Fatalf("方言 %s：解析出空 chunk", tc.name)
		}
		if chunk.Delta.ReasoningContent == "" {
			t.Errorf("方言 %s：IR 的 ReasoningContent 为空 ⇒ "+
				"alias 没有生效（Ollama 方言的思考轨迹被静默丢弃）", tc.name)
		}
	}
}
