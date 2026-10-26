package emptyoutcome

import (
	"encoding/json"
	"testing"
)

// TestInspectEmptyBody pins the R150-round attribution of an EMPTY upstream
// body (why it was empty), which is a **separate** question from "is it empty".
//
// ── 为什么要有这个测试 ───────────────────────────────────────────────────────
//
// R150 轮（2026-10-27）在本地 8782 网关上实测到一种此前没有被区分过的形态：
// 上游对一条长提示词返回 **HTTP 200**，`message.content` 为空，而
// `usage.completion_tokens == usage.completion_tokens_details.reasoning_tokens
// == 请求的 max_tokens`、`finish_reason == "length"`
// ⇒ **整个输出预算被推理吃掉，assistant 正文从未产出**。
//
// ⇒ 改动前的服务端日志只记 `status=200` ⇒ 运维会把这条读成「上游挂了」，
//
//	而真相是「上游答了，只是预算不够」⇒ 两种故障的处置完全不同。
//
// ⇒ 本测试把「归因」这件事钉死：
//
//	① 归因**不得**改变空/非空判定（那是 2026-10-17 属主裁决过的口径）；
//	② 「预算耗尽」与「上游空」必须能被区分开；
//	③ 不可解析的 body ⇒ **不臆测归因**（EmptyReasonUnknown）。
//
// ★ 阳性对照：构造「预算耗尽」与「上游空」两种 body ⇒ 必须落到不同 reason；
// ★ 阴性对照：不可解析 body ⇒ Unknown；正常 body（有 content）⇒ 不判空。
func TestInspectEmptyBody(t *testing.T) {
	// bodyWithUsage 造一个 content 为空、usage 可控的 chat body。
	bodyWithUsage := func(content string, finishReason string, completion, reasoning int) []byte {
		b, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":              "assistant",
					"content":           content,
					"reasoning_content": "thinking...",
				},
				"finish_reason": finishReason,
			}},
			"usage": map[string]any{
				"prompt_tokens":     44,
				"completion_tokens": completion,
				"total_tokens":      44 + completion,
				"completion_tokens_details": map[string]any{
					"reasoning_tokens": reasoning,
				},
			},
		})
		if err != nil {
			t.Fatalf("构造 body 失败: %v", err)
		}
		return b
	}

	t.Run("预算被推理吃光 ⇒ budget_exhausted_by_reasoning", func(t *testing.T) {
		reason, usage := InspectEmptyBody(bodyWithUsage("", "length", 1024, 1024))
		if reason != EmptyReasonBudgetExhausted {
			t.Fatalf("reason = %q, want %q", reason, EmptyReasonBudgetExhausted)
		}
		if usage.CompletionTokens != 1024 || usage.ReasoningTokens != 1024 {
			t.Fatalf("usage = %+v, want completion=1024 reasoning=1024", usage)
		}
		if usage.FinishReason != "length" {
			t.Fatalf("finish_reason = %q, want %q", usage.FinishReason, "length")
		}
	})

	t.Run("finish_reason=length 且有产出 ⇒ budget_exhausted（不依赖 reasoning 细节）", func(t *testing.T) {
		reason, usage := InspectEmptyBody(bodyWithUsage("", "length", 512, 0))
		if reason != EmptyReasonBudgetExhausted {
			t.Fatalf("reason = %q, want %q", reason, EmptyReasonBudgetExhausted)
		}
		if usage.CompletionTokens != 512 {
			t.Fatalf("usage = %+v", usage)
		}
	})

	t.Run("上游什么都没有 ⇒ upstream_empty", func(t *testing.T) {
		reason, usage := InspectEmptyBody(bodyWithUsage("", "stop", 0, 0))
		if reason != EmptyReasonUpstreamEmpty {
			t.Fatalf("reason = %q, want %q", reason, EmptyReasonUpstreamEmpty)
		}
		if usage.CompletionTokens != 0 {
			t.Fatalf("usage = %+v", usage)
		}
	})

	t.Run("不可解析 ⇒ Unknown（不臆测归因）", func(t *testing.T) {
		reason, _ := InspectEmptyBody([]byte("not json"))
		if reason != EmptyReasonUnknown {
			t.Fatalf("reason = %q, want %q", reason, EmptyReasonUnknown)
		}
		reason, _ = InspectEmptyBody(nil)
		if reason != EmptyReasonUnknown {
			t.Fatalf("reason = %q, want %q", reason, EmptyReasonUnknown)
		}
	})

	t.Run("★ 归因不得改变空/非空判定（2026-10-17 属主裁决口径）", func(t *testing.T) {
		// 预算耗尽的 body：仍然是「空」（必须 failover）。
		budget := bodyWithUsage("", "length", 1024, 1024)
		if !ChatBodyIsEmpty(budget) {
			t.Fatalf("预算耗尽的 body 必须仍然被判为空")
		}
		// 有正文的 body：不是空 ⇒ InspectEmptyBody 不参与空判定。
		ok := bodyWithUsage("hello", "stop", 3, 0)
		if ChatBodyIsEmpty(ok) {
			t.Fatalf("有正文的 body 不得被判为空")
		}
	})
}
