package admin

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ★ 审核单的「原始上下文」读面（2026-10-29 输出合规误报轮）。
//
// ★ owner 的原话：「审批列表里看不到原始的上下文，搞不明白是什么原因被拦截，
//   无法进行审批。」★★ 这两条测试分别钉住：
//   ① 读面能从**请求日志**联表取回这次请求的原始上下文（★ 不落库）★★；
//   ② 归因结论必须与**证据强度**匹配（★ 有规则 /守卫侧 / 待人工，三档）★★。

// TestLoadBlockReviewContextRowsReadsRequestLog 钉住上下文读面：★ 按
// request_id 从会话族读面取回请求预览 / 模型 / 会话 ID / 失败口径 ★★。
func TestLoadBlockReviewContextRowsReadsRequestLog(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	rows := mock.NewRows([]string{
		"request_id", "request_preview", "response_preview", "client_model",
		"outbound_model", "gw_session_id", "error_kind", "failure_stage",
		"failure_detail_code", "prompt_tokens", "completion_tokens",
		"latency_ms", "client_profile",
	}).AddRow(
		"req-1", "请帮我把这段配置改一下", "", "minimax-m3.1",
		"minimax-m3.1", "gw_session_abc", "output_policy_blocked", "upstream",
		"output_policy_blocked", 221552, 0, 110887, "default",
	)
	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(request_preview, '')")).
		WithArgs([]string{"req-1"}).
		WillReturnRows(rows)

	h := &Handler{}
	got, err := h.loadBlockReviewContextRows(context.Background(), mock, []string{"req-1"}, []sanitizerules.BlockReview{
		{ID: 1, RequestID: "req-1", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("loadBlockReviewContextRows: %v", err)
	}
	row, ok := got["req-1"]
	if !ok {
		t.Fatalf("没有取到 req-1 的上下文（审核员就看不到原始上下文）")
	}
	if !strings.Contains(row.requestPreview, "把这段配置改一下") {
		t.Fatalf("请求预览不对: %q", row.requestPreview)
	}
	if row.sessionID != "gw_session_abc" {
		t.Fatalf("会话 ID 不对: %q", row.sessionID)
	}
	if row.errorKind != "output_policy_blocked" {
		t.Fatalf("失败口径不对: %q", row.errorKind)
	}
	if row.promptTokens != 221552 || row.latencyMS != 110887 {
		t.Fatalf("token/时延读数不对: %+v", row)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDeriveBlockReviewAttribution 钉住归因的**三档**与措辞强度。
//
// ★ 关键不变量：★★ 候选规则只能出现在「待人工判断」那一档 ⇒ ★★ 不许把推断
//
//	说成事实 ★★。
func TestDeriveBlockReviewAttribution(t *testing.T) {
	t.Run("有规则 ⇒ 直接说规则", func(t *testing.T) {
		kind, text := deriveBlockReviewAttribution(
			sanitizerules.BlockReview{Rule: "pii.email", RuleName: "电子邮箱地址"},
			blockReviewContext{}, nil)
		if kind != "rule" {
			t.Fatalf("kind = %q，期望 rule", kind)
		}
		if !strings.Contains(text, "pii.email") {
			t.Fatalf("归因文本没有规则名: %q", text)
		}
	})

	t.Run("无规则 + 出站拦截 + 邻近无规则命中 ⇒ 守卫侧", func(t *testing.T) {
		kind, text := deriveBlockReviewAttribution(
			sanitizerules.BlockReview{},
			blockReviewContext{ErrorKind: "output_policy_blocked", PromptTokens: 221552, LatencyMS: 110887},
			nil)
		if kind != "guard" {
			t.Fatalf("kind = %q，期望 guard", kind)
		}
		if !strings.Contains(text, "守卫") {
			t.Fatalf("归因文本没有点明是守卫侧拦截: %q", text)
		}
		if !strings.Contains(text, "221552") {
			t.Fatalf("归因文本没有带上响应侧证据（token 数）: %q", text)
		}
	})

	t.Run("无规则 + 邻近有规则命中 ⇒ 待人工判断 + 候选", func(t *testing.T) {
		kind, text := deriveBlockReviewAttribution(
			sanitizerules.BlockReview{},
			blockReviewContext{ErrorKind: "output_policy_blocked"},
			[]blockReviewCandidateRule{{RuleID: "pii.email.egress", Action: "log"}})
		if kind != "unknown" {
			t.Fatalf("kind = %q，期望 unknown", kind)
		}
		if !strings.Contains(text, "pii.email.egress") {
			t.Fatalf("归因文本没有列出候选规则: %q", text)
		}
		if !strings.Contains(text, "候选") {
			t.Fatalf("候选必须标明是候选（不许说成事实）: %q", text)
		}
	})

	t.Run("失败口径不是出站拦截 ⇒ 不判守卫侧", func(t *testing.T) {
		kind, _ := deriveBlockReviewAttribution(
			sanitizerules.BlockReview{},
			blockReviewContext{ErrorKind: "upstream_timeout"},
			nil)
		if kind == "guard" {
			t.Fatal("非出站策略拦截不许判成守卫侧拦截")
		}
	})
}

// TestBlockReviewCandidateWindowUsesLastSeen 钉住候选规则的窗口判据。
//
// ★ 判据是「**最近一次命中（last_seen）落在拦截前后 ±窗口内**」★
// ⇒ ★★ **不是**「[first_seen, last_seen] 与窗口重叠」★★：★ 事件是**按指纹
// 聚合**的（★ 同一规则反复命中聚成一行）⇒ ★★ 它的区间可以**横跨数周**
// （★ 211 生产实测：★ pii.server_ip 的 first_seen 在 10-15、last_seen 在
// 10-29）★★ ⇒ ★★ 「区间重叠」几乎**恒真** ⇒ ★★ 候选清单被全量历史淹没
// （★ 实测 26 条）★★ ⇒ ★★ 那个判据必须换掉 ★★。
func TestBlockReviewCandidateWindowUsesLastSeen(t *testing.T) {
	base := time.Date(2026, 10, 29, 18, 10, 45, 0, time.UTC)
	// ★命中在窗口内（但 first_seen 远在两周前）⇒ ⇒ 仍算候选 ★
	recentButOldStart := blockReviewCandidateRule{
		RuleID:    "pii.email.egress",
		FirstSeen: base.AddDate(0, 0, -14),
		LastSeen:  base.Add(-1 * time.Minute),
	}
	// ★ 最近一次命中在窗口外（哪怕区间与窗口有重叠）⇒ ⇒ 不算候选 ★
	oldRecency := blockReviewCandidateRule{
		RuleID:    "pii.bank_card.egress",
		FirstSeen: base.Add(-2 * time.Hour),
		LastSeen:  base.Add(-90 * time.Minute),
	}
	inWindow := func(ev blockReviewCandidateRule) bool {
		return !ev.LastSeen.Before(base.Add(-blockReviewCandidateWindow)) &&
			!ev.LastSeen.After(base.Add(blockReviewCandidateWindow))
	}
	if !inWindow(recentButOldStart) {
		t.Fatal("最近一次命中在窗口内 ⇒ 必须算候选")
	}
	if inWindow(oldRecency) {
		t.Fatal("最近一次命中在窗口外 ⇒ 不该算候选（★ 区间重叠不算）")
	}
}

// TestDeriveBlockReviewAttributionCapsCandidates 钉住候选清单**封顶**。
//
// ★ 211 实测：★ 一次请求里 server_ip / server_ipv6 / address /
// network_destination / bearer / password.assignment 都会命中 ⇒ ★★ 全列出来
// = 审核员看到一堵墙 ⇒ ★★ 反而读不出重点 ★★ ⇒ ★★ 封顶 + 其余只给计数 ★★
func TestDeriveBlockReviewAttributionCapsCandidates(t *testing.T) {
	candidates := make([]blockReviewCandidateRule, 0, 20)
	for i := 0; i < 20; i++ {
		candidates = append(candidates, blockReviewCandidateRule{
			RuleID: "rule-" + strconv.Itoa(i),
			Action: "log",
		})
	}
	kind, text := deriveBlockReviewAttribution(
		sanitizerules.BlockReview{},
		blockReviewContext{ErrorKind: "output_policy_blocked"},
		candidates)
	if kind != "unknown" {
		t.Fatalf("kind = %q，期望 unknown", kind)
	}
	if strings.Count(text, "rule-") != blockReviewCandidateMax {
		t.Fatalf("列出的候选数 = %d，期望封顶 %d", strings.Count(text, "rule-"), blockReviewCandidateMax)
	}
	if !strings.Contains(text, "另有 12 条未列") {
		t.Fatalf("其余候选必须给计数（不许隐藏）: %q", text)
	}
}
