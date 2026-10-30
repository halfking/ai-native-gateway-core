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
	// ★★ 租户门（2026-10-30 12h 审计轮）：★★ 读面现在**必须**同时按租户收窄
	//   ⇒ ★★ 期望里带上租户集合 ⇒ ★★ 少一个参数就不是这条 SQL 了 ★★
	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(request_preview, '')")).
		WithArgs([]string{"req-1"}, []string{"t1"}).
		WillReturnRows(rows)

	h := &Handler{}
	got, err := h.loadBlockReviewContextRows(context.Background(), mock, []string{"req-1"}, []sanitizerules.BlockReview{
		{ID: 1, RequestID: "req-1", TenantID: "t1", CreatedAt: time.Now()},
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
			blockReviewContext{}, nil, false)
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
			nil, false)
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
			[]blockReviewCandidateRule{{RuleID: "pii.email.egress", Action: "log"}}, false)
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
			nil, false)
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
		candidates, false)
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

// ── 2026-10-30 12h 审计轮新增三道契约 ──────────────────────────────────
//
// ★ 三道门分别钉住本轮修掉的三个真缺陷：
//   ① A7 分区边界的日期布局（★ "01" 是月、"02" 才是日）★★
//   ② A3 上下文读面**必须**按租户收窄（★ request_id 客户端自选、可跨租户重复）★★
//   ③ A9 候选被读取上限截断时，★ 不许把「候选为空」升格成「不是内容规则命中」★★

// TestBlockReviewPartitionBoundsRendersRealDate 钉住 A7：★ 分区下/上界必须
// 渲染成**真实的年月日** ★。
//
// ★★ 旧写法是 `Format("'2006-01-01'")` ⇒ ★★ Go 的 layout 里 "01" 是**月**
// ⇒ ★★ 2026-11-05 被渲染成 '2026-11-11' ⇒ ★★ 11 月 5~10 日的请求行查不到
// ⇒ ★★ 审核单变成「上下文暂不可用」—— ★★ 正是本功能要消灭的现象 ★★。
func TestBlockReviewPartitionBoundsRendersRealDate(t *testing.T) {
	created := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	from, to := blockReviewPartitionBounds([]sanitizerules.BlockReview{
		{ID: 1, CreatedAt: created},
	})
	// 期望：created 前后各留一个月，且**日**必须是 5（不是 11）。
	if from != "'2026-10-05'" {
		t.Fatalf("分区下界 = %s，期望 '2026-10-05'（★ 日必须是 5）", from)
	}
	if to != "'2026-12-05'" {
		t.Fatalf("分区上界 = %s，期望 '2026-12-05'（★ 日必须是 5）", to)
	}
}

// TestBlockReviewContextRowsAreTenantScoped 钉住 A3：★★ 上下文读面必须
// 按租户收窄 ⇒ ★★ 别的租户用同一个 request_id 不会被一起查出来 ★★。
//
// ★ 两个腿：★★ ① SQL 里必须有租户谓词 + 必须传租户集合 ★★
// ★★ ② 审核单没有租户时 ⇒ ★★ fail-closed（一条都不查）★★。
func TestBlockReviewContextRowsAreTenantScoped(t *testing.T) {
	t.Run("SQL 带租户谓词并传租户集合", func(t *testing.T) {
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
			"req-1", "预览", "", "m", "m", "s", "", "", "", 0, 0, 0, "",
		)
		mock.ExpectQuery(regexp.QuoteMeta("tenant_id = ANY($2)")).
			WithArgs([]string{"req-1"}, []string{"t1", "t2"}).
			WillReturnRows(rows)

		h := &Handler{}
		got, err := h.loadBlockReviewContextRows(context.Background(), mock,
			[]string{"req-1"},
			[]sanitizerules.BlockReview{
				{ID: 1, RequestID: "req-1", TenantID: "t1", CreatedAt: time.Now()},
				{ID: 2, RequestID: "req-1", TenantID: "t2", CreatedAt: time.Now()},
			})
		if err != nil {
			t.Fatalf("loadBlockReviewContextRows: %v", err)
		}
		if _, ok := got["req-1"]; !ok {
			t.Fatal("同租户的 req-1 应当取得到")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("没有租户 ⇒ fail-closed（不查库）", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()
		// ★ 没有 ExpectQuery ⇒ 一旦真的去查库，pgxmock 会报错 ★
		h := &Handler{}
		got, err := h.loadBlockReviewContextRows(context.Background(), mock,
			[]string{"req-1"},
			[]sanitizerules.BlockReview{
				{ID: 1, RequestID: "req-1", CreatedAt: time.Now()},
			})
		if err != nil {
			t.Fatalf("loadBlockReviewContextRows: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("没有租户时必须 fail-closed（返回空），实际返回 %d 行", len(got))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestDeriveBlockReviewAttributionTruncatedIsNotGuard 钉住 A9：★★ 候选被
// 读取上限截断时，★ 归因**不许**判成「守卫侧拦截」★★。
//
// ★★ 为什么：★★ 旧文案写着「邻近时段没有任何内容规则阻断 ⇒ 判定为守卫侧
// 拦截」并直接引导「可直接标误报」⇒ ★★ 标到阈值后自动放行该租户该规则 7 天
// ⇒ ★★ 真实的内容规则被悄悄放过去 ★★。
func TestDeriveBlockReviewAttributionTruncatedIsNotGuard(t *testing.T) {
	kind, text := deriveBlockReviewAttribution(
		sanitizerules.BlockReview{},
		blockReviewContext{ErrorKind: "output_policy_blocked"},
		nil, true)
	if kind == "guard" {
		t.Fatalf("候选被截断时**不许**判成守卫侧拦截（会把真实内容规则放过去）: %q", text)
	}
	if kind != "unknown" {
		t.Fatalf("kind = %q，期望 unknown（结论留给人工）", kind)
	}
	if !strings.Contains(text, "截断") {
		t.Fatalf("归因文本必须说明候选可能被截断: %q", text)
	}
}
