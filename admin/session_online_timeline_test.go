// Package admin — session_online_timeline_test.go
//
// v4 T7（2026-08-18, migration 532）会话时间线单元测试：
//
//	UT-FS-03  timeline 读 hot + promoted 月度分区（request_logs_with_current_month
//	          视图）而非仅 request_logs_hot；>7d 行不被截断；联合结果按 ts 排序；
//	UT-FS-04  session 标识语义统一（文本 gw_session_id 主键 + 数值 sessions.id
//	          解析/兜底），tenant 过滤参数形状；
//	G10       is_final_success / outcome（final_success / superseded_success /
//	          failure + error_kind / failure_stage）字段映射。
//
// DB mock 用 pgxmock（现有 admin 测试基建，见 session_turns_tree_test.go）。
package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

var timelineCols = []string{
	"request_id", "request_type", "request_status", "model", "latency_ms",
	"ts", "parent_request_id", "is_final_success", "error_kind", "failure_stage",
}

// UT-FS-03 的**读源**已随会话存储解耦 v3 变更（2026-09-30，见
// TestQuerySessionTimeline_UsesNativeSourceWithPushedPredicate）：
// timeline 从 request_logs_with_current_month 视图迁到 session 族原生源，
// 谓词下推进两条腿。其余断言（>7d 行不被截断、联合结果按 ts 排序、
// outcome 标注、截断保护）语义不变，仍然有效。
func TestQuerySessionTimeline_ReadsHotPlusPromotedView(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	elevenDaysAgo := time.Now().UTC().Add(-11 * 24 * time.Hour).Truncate(time.Second)
	tenDaysAgo := elevenDaysAgo.Add(time.Hour)
	nineDaysAgo := elevenDaysAgo.Add(2 * time.Hour)
	latency := 1500

	rows := pgxmock.NewRows(timelineCols).
		// >7d：重发链第一行失败（穷尽失败细节在 error_kind/failure_stage）
		AddRow("req_older_fail", "main", "failure", "glm-4", nil,
			elevenDaysAgo, "", false, "no_candidate", "exhausted").
		// >7d：promoted 分区里的唯一最终成功
		AddRow("req_final", "main", "success", "glm-4", &latency,
			tenDaysAgo, "", true, "", "").
		// >7d：同会话更晚的成功但被取代（客户端又重发了一次）
		AddRow("req_superseded", "main", "success", "glm-4", &latency,
			nineDaysAgo, "", false, "", "").
		// 扩展请求挂到 req_final 下
		AddRow("req_title_child", "title_gen", "success", "glm-4-air", nil,
			nineDaysAgo.Add(time.Minute), "req_final", false, "", "")

	mock.ExpectQuery(`FROM public\.session_turns_hot t`).
		WithArgs("sess-old-7d").
		WillReturnRows(rows)

	turns, hasMore, err := querySessionTimeline(context.Background(), mock, "sess-old-7d", "")
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("unexpected has_more")
	}
	if len(turns) != 3 { // req_final 带一个 child；older_fail、superseded 顶层
		t.Fatalf("expected 3 top-level turns, got %d: %+v", len(turns), turns)
	}

	byID := map[string]*SessionTurn{}
	var final, superseded *SessionTurn
	for _, turn := range turns {
		byID[turn.RequestID] = turn
		for _, child := range turn.Children {
			byID[child.RequestID] = child
		}
	}
	final = byID["req_final"]
	superseded = byID["req_superseded"]
	older := byID["req_older_fail"]
	child := byID["req_title_child"]
	if final == nil || superseded == nil || older == nil || child == nil {
		t.Fatalf("missing turns: %+v", byID)
	}

	if !final.IsFinalSuccess || final.Outcome != "final_success" || final.OutcomeReason != "" {
		t.Fatalf("final row annotation mismatch: %+v", final)
	}
	if superseded.IsFinalSuccess || superseded.Outcome != "superseded_success" ||
		superseded.OutcomeReason != "superseded_by_final_success" {
		t.Fatalf("superseded row annotation mismatch: %+v", superseded)
	}
	if older.IsFinalSuccess || older.Outcome != "failure" ||
		older.ErrorKind != "no_candidate" || older.FailureStage != "exhausted" {
		t.Fatalf("failure row annotation mismatch: %+v", older)
	}
	if len(final.Children) != 1 || final.Children[0].RequestID != "req_title_child" ||
		final.Children[0].RequestType != "title_gen" {
		t.Fatalf("child attach mismatch: %+v", final.Children)
	}
	// 子行（非最终成功但同会话有成功持有标记）也标注为被取代成功。
	if child.Outcome != "superseded_success" {
		t.Fatalf("child outcome = %q, want superseded_success", child.Outcome)
	}
	// promoted 行的 ts 原样透传（>7d 未截断）。
	if final.StartedAt != tenDaysAgo.Format(time.RFC3339) {
		t.Fatalf("final StartedAt = %q, want %q", final.StartedAt, tenDaysAgo.Format(time.RFC3339))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// 查询形状钉死：投影含 is_final_success / error_kind / failure_stage，
// 读源是 session 族原生源且 session 谓词**下推**进两条腿，
// tenant 过滤是外层第二参数。
//
// 会话存储解耦 v3（2026-09-30）：读源由 request_logs_with_current_month
// 迁到 db.SessionFamilyTurnsForSessionSQL()。断言里最要紧的一条是
// **外层不得出现 gw_session_id** —— 投影名是
// `(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)`，
// 按投影名过滤用不上 idx_session_turns_session，会把 13 倍收益打回全表扫。
func TestQuerySessionTimeline_QueryShape(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT rl\.request_id, COALESCE\(rl\.request_type,'main'\)[\s\S]*COALESCE\(rl\.is_final_success, FALSE\)[\s\S]*COALESCE\(rl\.error_kind, ''\), COALESCE\(rl\.failure_stage, ''\)[\s\S]*FROM \(SELECT[\s\S]*FROM public\.session_turns_hot t[\s\S]*WHERE t\.session_id = \$1[\s\S]*FROM public\.session_turns t[\s\S]*WHERE t\.session_id = \$1[\s\S]*AND rl\.tenant_id = \$2[\s\S]*ORDER BY ts ASC LIMIT 201`).
		WithArgs("sess-t", "tenant-a").
		WillReturnRows(pgxmock.NewRows(timelineCols))

	if _, _, err := querySessionTimeline(context.Background(), mock, "sess-t", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestQuerySessionTimeline_UsesNativeSourceWithPushedPredicate 单独钉住
// 「下推谓词出现两次、外层不加 gw_session_id 过滤」这条性能契约。
//
// 为什么用 QueryMatcherFunc 而不是再写一条正则：变异验证显示正则守不住。
// 把外层 `AND rl.gw_session_id = $1` 加回去（正是这条谓词把 13 倍收益打回
// 全表扫），下推谓词仍然存在于 SQL 里，所以任何「断言它存在」的正则都照过；
// 而 QueryShape 里的 `[\s\S]*AND rl\.tenant_id` 也会被 `... gw_session_id = $1
// AND rl.tenant_id = $2` 顺带满足。**变异后门仍绿 = 守卫没有判别力**，必须
// 换成能看见实际 SQL 并做否定断言的写法。
//
// 判别力已验证：加回外层 gw_session_id 谓词 → 本测试转红；还原 → 转绿。
func TestQuerySessionTimeline_UsesNativeSourceWithPushedPredicate(t *testing.T) {
	var actualSQL string
	matcher := pgxmock.QueryMatcherFunc(func(expectedSQL, realSQL string) error {
		actualSQL = realSQL
		return nil
	})
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("").WithArgs("sess-push", "tenant-a").
		WillReturnRows(pgxmock.NewRows(timelineCols))

	if _, _, err := querySessionTimeline(context.Background(), mock, "sess-push", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if actualSQL == "" {
		t.Fatal("matcher never saw the generated SQL")
	}

	// 否定断言：外层一旦出现 gw_session_id 过滤，下推就被抵消。
	if strings.Contains(actualSQL, "rl.gw_session_id") {
		t.Errorf("outer gw_session_id predicate defeats the pushdown (projected name is a CASE expression, so the session index is unusable):\n%s", actualSQL)
	}
	// 肯定断言：两条腿各一次下推。
	if n := strings.Count(actualSQL, "WHERE t.session_id = $1"); n != 2 {
		t.Errorf("session predicate must be pushed into both legs, found %d", n)
	}
	// 不得回退到视图。
	if strings.Contains(actualSQL, "request_logs_with_current_month") {
		t.Errorf("timeline must read the session family natively, not the frozen v1 view:\n%s", actualSQL)
	}
}

// 截断保护回归：第 201 行触发 has_more（LIMIT 201 / timelineLimit 200）。
func TestQuerySessionTimeline_TruncationFlag(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows(timelineCols)
	base := time.Now().UTC().Add(-10 * 24 * time.Hour)
	for i := 0; i < 201; i++ {
		rows.AddRow(
			"req_"+string(rune('a'+i%26))+string(rune('0'+i/26)),
			"main", "success", "glm-4", nil,
			base.Add(time.Duration(i)*time.Second), "", false, "", "")
	}
	mock.ExpectQuery(`FROM public\.session_turns_hot t`).
		WithArgs("sess-big").
		WillReturnRows(rows)

	turns, hasMore, err := querySessionTimeline(context.Background(), mock, "sess-big", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(turns) != 200 {
		t.Fatalf("hasMore=%v turns=%d, want true/200", hasMore, len(turns))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// UT-FS-04：数值 sessions.id → 文本 session_id 解析（显式 session_pk 与
// 纯数字兜底共用），tenant 过滤形状一致。
func TestResolveSessionIDByPK(t *testing.T) {
	t.Run("hit without tenant filter", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT session_id FROM sessions WHERE id = \$1 ORDER BY partition_date DESC LIMIT 1`).
			WithArgs(int64(42)).
			WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("gw_sess_42"))
		got, err := resolveSessionIDByPK(context.Background(), mock, "", 42)
		if err != nil || got != "gw_sess_42" {
			t.Fatalf("got %q err %v", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tenant scoped miss returns empty", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT session_id FROM sessions WHERE id = \$1 AND tenant_id = \$2 ORDER BY partition_date DESC LIMIT 1`).
			WithArgs(int64(7), "tenant-b").
			WillReturnError(pgx.ErrNoRows)
		got, err := resolveSessionIDByPK(context.Background(), mock, "tenant-b", 7)
		if err != nil || got != "" {
			t.Fatalf("got %q err %v, want empty/nil", got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// UT-FS-04：outcome 推导矩阵（含迁移前历史成功行的降级语义）。
func TestDeriveTurnOutcome(t *testing.T) {
	cases := []struct {
		name                     string
		status                   string
		isFinal, sessionHasFinal bool
		wantOutcome, wantReason  string
	}{
		{"final success", "success", true, true, "final_success", ""},
		{"final success even if flag alone", "success", true, false, "final_success", ""},
		{"superseded by retry", "success", false, true, "superseded_success", "superseded_by_final_success"},
		{"legacy unmarked success", "success", false, false, "success", ""},
		{"failure", "failure", false, true, "failure", ""},
		{"rate limited", "rate_limited", false, false, "rate_limited", ""},
		{"in progress", "in_progress", false, false, "in_progress", ""},
		{"custom disconnect status", "client_disconnect", false, true, "client_disconnect", ""},
		{"empty status", "", false, false, "other", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, reason := deriveTurnOutcome(tc.status, tc.isFinal, tc.sessionHasFinal)
			if outcome != tc.wantOutcome || reason != tc.wantReason {
				t.Fatalf("deriveTurnOutcome(%q,%v,%v) = (%q,%q), want (%q,%q)",
					tc.status, tc.isFinal, tc.sessionHasFinal, outcome, reason, tc.wantOutcome, tc.wantReason)
			}
		})
	}
}

func TestIsAllDigits(t *testing.T) {
	cases := map[string]bool{
		"12345":   true,
		"0":       true,
		"12a":     false,
		"s-123":   false,
		"":        false,
		"gw_sess": false,
	}
	for in, want := range cases {
		if got := isAllDigits(in); got != want {
			t.Fatalf("isAllDigits(%q) = %v, want %v", in, got, want)
		}
	}
}
