// Package streaming — summarize_audit_gate_test.go
//
// 2026-10-27「12 小时内修订审计」轮新增的两条回归门。
//
// ★ 两条门锁的是同一批新代码里**两个 P1**：
//
//	① 委托与降级共用一把预算 ⇒ 委托超时时降级不可用（"失败必降级"落空）；
//	② brief 风格不校验输出 ⇒ **HTTP 200 + 空摘要**。
//
// ★ 门的设计原则（与本仓既有门一致）：**必须能被判红** ——
//
//	每条门都配「必须判绿」的对照臂，避免判据恒真。
package streaming

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDelegateBudgetNeverEatsTheFallbackBudget 锁住「委托不得吃空降级预算」。
//
// ★ 判据（一条，不可协商）：**委托预算 ≤ 总预算 / 3** ⇒ 降级恒有 ≥2/3。
//
//	这条判据正是原缺陷的形状：原实现直接用 `s.asm.budget`（8min，默认
//	总预算 10min 的 80%）⇒ 委托超时后父 ctx 只剩 2min，而降级管线的
//	map 阶段自身就要 240s ⇒ 降级在唯一需要它的场景里不可用。
func TestDelegateBudgetNeverEatsTheFallbackBudget(t *testing.T) {
	const total = 10 * time.Minute

	// ① 默配（8min）必须被夹到 1/3。
	got := delegateBudgetFor(total, 8*time.Minute)
	if got > total/asmDelegateBudgetDivisor {
		t.Fatalf("delegate budget = %v, want <= %v (total/%d): "+
			"the delegate would eat the fallback's budget",
			got, total/asmDelegateBudgetDivisor, asmDelegateBudgetDivisor)
	}
	if got != total/asmDelegateBudgetDivisor {
		t.Fatalf("delegate budget = %v, want %v (8min must be clamped to total/%d)",
			got, total/asmDelegateBudgetDivisor, asmDelegateBudgetDivisor)
	}

	// ② ★ 对照臂：**小于**上限的配置必须原样保留（不许被无条件压到 1/3）。
	if got := delegateBudgetFor(total, time.Minute); got != time.Minute {
		t.Fatalf("delegate budget = %v, want 1m (a smaller configured budget must be kept as-is)", got)
	}

	// ③ ★ 对照臂：配置 ≥ 总预算（运维把 LLM_GATEWAY_ASM_TIMEOUT 设成
	//    30min 这类）时**仍必须**被夹住 —— 这正是「降级预算为 0」那条路的源头。
	if got := delegateBudgetFor(total, 30*time.Minute); got > total/asmDelegateBudgetDivisor {
		t.Fatalf("delegate budget = %v, want <= %v even when configured above the total budget",
			got, total/asmDelegateBudgetDivisor)
	}

	// ④ 端到端不变量：降级恒拿得到 ≥2/3 的总预算。
	delegate := delegateBudgetFor(total, 8*time.Minute)
	if remaining := total - delegate; remaining < total*2/3 {
		t.Fatalf("fallback would only have %v of %v total budget (want >= %v)",
			remaining, total, total*2/3)
	}
}

// TestSummarizeBriefRejectsEmptySummaryAfterThinkStripping 锁住
// 「brief 风格不得以 200 返回空摘要」。
//
// ★ 缺陷形状：`callChat` 成功返回时 content 仍可能是**空串** —— thinking
//
//	模型只吐 reasoning 时，chat 面看到非空 content ⇒ 200，但
//	`stripThinkBlock` 把 reasoning 剥掉后就空了。
//	★ minutes 分支与短文本路径都过 `validateRenderedSummary`，
//	  **只有 brief 这条分支漏了** ⇒ 同一个请求换个 style 就出现两种语义。
//
// ★ 对照臂（必须判绿）：同样的 think-only 输出，**minutes** 分支本来就
//
//	判红 —— 证明判据能区分「空输出」与「合法输出」，不是恒真。
func TestSummarizeBriefRejectsEmptySummaryAfterThinkStripping(t *testing.T) {
	// 长文本（≥ summarizeShortPathRunes）⇒ 走 map-reduce 的 brief 分支。
	// ★ 字数必须够：「会议记录内容。」= 7 rune，600 段 ≈ 4200 rune > 3000 阈值。
	//   （不足阈值会走**短文本**单次路径 ⇒ 那条分支本来就有校验 ⇒ 门测不到本缺陷。）
	longText := strings.Repeat("会议记录内容。", 600)

	// chat 桩工厂：★ 每次起服务必须**各自**带一个计数器 ——
	// 计数器若跨服务共享，第二个服务的第一通 map 调用会误判成 reduce 调用。
	newChatStub := func() http.HandlerFunc {
		var calls int
		return func(w http.ResponseWriter, r *http.Request) {
			calls++
			bodyBytes, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(bodyBytes, &body)
			var content string
			if calls == 1 {
				// map 阶段：必须是 JSON 对象形，否则会先走「段降级」分支。
				content = `{"segment_range":"第1段","topics":["预算"]}`
			} else {
				// reduce 阶段：只有思考块 ⇒ 剥离后为空串。
				content = "<think>让我想想……</think>"
			}
			resp := map[string]any{
				"model": "stub-llm",
				"choices": []map[string]any{{
					"message": map[string]any{"content": content},
				}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}

	srv, _ := newSummarizeTestServer(t, newChatStub())

	// ① brief 风格：空输出**不得**以 200 返回。
	resp, body := summarizePost(t, srv.URL, "/v1/summarize", `{"model":"m","style":"brief","text":`+mustJSONString(longText)+`}`)
	if resp.StatusCode == http.StatusOK {
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		if s, _ := out["summary"].(string); strings.TrimSpace(s) == "" {
			t.Fatalf("brief returned HTTP 200 with an EMPTY summary (body=%s): "+
				"the think-stripped empty output must be rejected, not served as success", body)
		}
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d for an empty brief summary (body=%s)",
			resp.StatusCode, http.StatusBadGateway, body)
	}

	// ② ★ 对照臂：同一个 think-only 输出，minutes 风格本来就判红
	//    ⇒ 证明「空输出」这个判据能区分两种风格，不是恒真。
	srv2, _ := newSummarizeTestServer(t, newChatStub())
	resp2, body2 := summarizePost(t, srv2.URL, "/v1/summarize", `{"model":"m","style":"minutes","text":`+mustJSONString(longText)+`}`)
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("minutes style returned 200 for the same empty output (body=%s): "+
			"the brief gate would then be a no-op distinction", body2)
	}
}
