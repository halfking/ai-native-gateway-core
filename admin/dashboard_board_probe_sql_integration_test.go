//go:build integration

package admin

// dashboard_board_probe_sql_integration_test.go
//
// 真库门：看板「总请求数 / 成功率」的探测排除 SQL 必须在真实 PostgreSQL 上
// 可执行，且不得因视图分支差异而静默丢行。
//
// 为什么需要这道门（沿用 admin/credential_monitor_heatmap_sql_integration_test.go
// 的理由）：pgxmock 只匹配查询*字符串*，从不让 PostgreSQL 解析它，所以所有
// 活在 SQL 文本里的缺陷对单测完全不可见。该文件记录过一次真实事故：
//
//	ERROR: column rl.origin_stage does not exist          (42703)
//	线上存活 ≥2 周、就在默认路径上
//
// 静态断言抓不到这类形状：视图名和谓词是运行时拼接的两个独立字面量，任何
// 单字面量规则都看不见两者同时出现；**执行**组装后的语句才看得见。
//
// 看板侧的风险与那次事故同源且更重：boardLogsWhere 是所有日志兜底读面的
// 共享 WHERE，一旦列名错，整个 /api/admin/dashboard/board 每次调用都 42703
// ——看板直接挂，不是局部降级。
//
// 跑法（需要能连到真库）：
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestDashboardBoard
//
// 未设 TEST_DATABASE_URL 时 Skip —— 但 Skip **不构成**「SQL 能解析」的证据。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// boardProbePool 用仓内既有的 setupTestDB（TEST_DATABASE_URL 门控，缺省 skip）。
// 刻意不新造一个 TEST_PG_URL：仓库里两种写法并存，但 TEST_DATABASE_URL 才是
// CI 真正注入的那个（.github/workflows/sessionforensics-ci.yml），跟它对齐才
// 能让这道门在有真库的流水线里真的跑起来。
func boardProbePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return setupTestDB(t)
}

// TestDashboardBoardPredicateColumnsExistOnBoardView 反向校验：看板实际读的那张
// 源必须真的有谓词引用的三列。
//
// ★ 2026-10-21（R67 / C8 改判）：**换判据形状**。
//
// 旧判据从 boardRequestLogsFromClause() 里「按 ` AS ` 切开取前半段」当**视图名**，
// 再去 pg_attribute 查它的列。r1033 起那张「视图」已经不存在了 ——
// boardRequestLogsFromClause() 返回的是**一段投影子查询**（会话族 hot ∪ 母表），
// 于是切出来的是一整段 SQL 文本 ⇒ 查 pg_attribute 必然零行 ⇒ 门恒红，
// 而它守的东西（谓词引用的列在不在）其实一点没坏。
//
// ⇒ 改成**同层且更强**的判据：把**生产那条 FROM + 生产那条谓词**直接在真库上跑。
//
//	能跑通 ⇒ 三列都在投影里、谓词能解析、计划能成；
//	跑不动（42703 / 其它）⇒ 门红并把原文打出来。
//	这比「视图有几列」更强：它验的是**看板真的会发出去的那条 SQL**。
func TestDashboardBoardPredicateColumnsExistOnBoardView(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	from, alias := boardRequestLogsFromClause()
	if strings.TrimSpace(from) == "" {
		t.Fatalf("boardRequestLogsFromClause() returned an empty FROM clause")
	}
	// boardRequestLogsFromClause() 返回的 from 已经带了 `AS <alias>`。
	pred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, alias, alias, alias)
	q := "SELECT count(*) FROM " + from +
		" WHERE " + alias + ".ts > now() - interval '7 days' AND " + pred
	if _, err := pool.Exec(ctx, q); err != nil {
		t.Fatalf("看板 fallback 的生产读法在真库上跑不通：%v\n"+
			"⚠ 谓词引用 quality_flags / task_type / origin_actor 三列 —— "+
			"少一列就是每次看板调用都 42703。\n  SQL: %s", err, q)
	}

	// ★ 附加断言：三列必须真的在**投影**里。
	//   ⚠ 不能去查物理表的列：quality_flags 在 session_turns 上**并不存在**
	//     （它由会话分臂的投影补成 NULL）⇒ 查物理列会误判成「缺列」。
	//   ⇒ 直接问 PG：让**投影**自己报。LIMIT 0 ⇒ 只解析不执行。
	for _, col := range []string{"quality_flags", "task_type", "origin_actor"} {
		probe := "SELECT " + col + " FROM " + from + " LIMIT 0"
		if _, err := pool.Exec(ctx, probe); err != nil {
			t.Errorf("投影里取不到列 %q：%v\n"+
				"⚠ 看板的探针排除谓词引用了这三列 ⇒ 少一列就是每次看板调用都 42703。"+
				"该列要么得投影进源，要么谓词得改用别的列。\n  SQL: %s", col, err, probe)
		}
	}
	t.Logf("看板生产读法在真库上可执行；谓词三列在投影里齐备")
}

// TestDashboardBoardProbeExclusionSurvivesNullPaddedFlags 是「能解析」给不了的那一半：
// 查询可以成功执行、返回 200、同时静默丢行。
//
// 已知缺陷形态（见 credential_monitor_heatmap_sql_integration_test.go 的记录）：
// 视图的 session 分支把 quality_flags 补成 NULL，于是 `'probe' = ANY(NULL)` 是
// NULL、`NOT NULL` 不是 TRUE，WHERE 会把**整支 session 分支**全部丢掉——
// 热榜对已迁移的数据集几乎全盲却毫无报错。谓词的 flags 臂必须有
// COALESCE(..., FALSE)。
//
// 真正要断言的不变量不是「保留数等于某个别的谓词」（两个不同谓词比大小什么也
// 证明不了），而是：**排除的判定不得取决于 quality_flags 恰好是不是 NULL。**
// 于是对同一批行跑两遍生产谓词——一遍用真实（可能为 NULL 的）flags，一遍把
// NULL 换成空数组——要求两者计数相等。
func TestDashboardBoardProbeExclusionSurvivesNullPaddedFlags(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "r", "r", "r")
	// ★ 2026-10-21（R67 / C8 同族）：源也必须是**生产那条**。
	//   旧写法硬编码 request_logs_with_current_month_without_customer_id ——
	//   那张视图 r1033 起就没了 ⇒ 42P01 ⇒ 门恒红，而它守的不变式没坏。
	from, _ := boardRequestLogsFromClause()
	var keptAsIs, keptDefaulted, nullFlags int64
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		WITH actual AS (
		    SELECT quality_flags, task_type, origin_actor
		    FROM %[2]s
		    WHERE ts > now() - interval '7 days'
		),
		defaulted AS (
		    SELECT COALESCE(quality_flags, '{}'::text[]) AS quality_flags,
		           task_type, origin_actor
		    FROM actual
		)
		SELECT
		    (SELECT count(*) FROM actual    r WHERE %[1]s) AS kept_as_is,
		    (SELECT count(*) FROM defaulted r WHERE %[1]s) AS kept_defaulted,
		    (SELECT count(*) FROM actual WHERE quality_flags IS NULL) AS null_flags
	`, pred, from)).Scan(&keptAsIs, &keptDefaulted, &nullFlags)
	if err != nil {
		t.Fatalf("counting query failed: %v", err)
	}
	t.Logf("7d window: %d rows have NULL quality_flags; production predicate keeps %d as-is / %d with NULL defaulted",
		nullFlags, keptAsIs, keptDefaulted)

	if keptAsIs != keptDefaulted {
		t.Errorf("the production exclusion keeps %d rows with real flags but %d with NULL "+
			"coalesced to an empty array — its verdict depends on quality_flags being NULL, "+
			"so the NULL-padded session branch (%d such rows) is silently excluded and the "+
			"board undercounts 总请求数. The flags arm needs COALESCE('probe' = ANY(...), FALSE).",
			keptAsIs, keptDefaulted, nullFlags)
	}
}

// TestDashboardBoardSummaryExecutesOnRealDatabase 跑真实生产路径
// （fallbackBoardSummary / queryBoardSummary），而不是 SQL 副本。
func TestDashboardBoardSummaryExecutesOnRealDatabase(t *testing.T) {
	pool := boardProbePool(t)
	h := &Handler{db: pool}
	tr := daysToBoardTimeRange(1)

	t.Run("fallback-summary", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		summary, err := h.fallbackBoardSummary(ctx, "", tr)
		if err != nil {
			t.Fatalf("fallbackBoardSummary: %v", err)
		}
		total, _ := summary["total_requests"].(int64)
		rate, _ := summary["success_rate"].(float64)
		t.Logf("fallback: total_requests=%d success_rate=%.4f", total, rate)
		if total < 0 {
			t.Fatal("negative total_requests")
		}
		if rate < 0 || rate > 1 {
			t.Errorf("success_rate out of range: %v", rate)
		}
	})

	t.Run("minute-summary", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if summary, ok := h.queryBoardSummary(ctx, "", tr); ok {
			total, _ := summary["total_requests"].(int64)
			t.Logf("minute: total_requests=%d", total)
		} else {
			t.Log("minute table has no rows in this window; fallback path is the one under test")
		}
	})

	t.Run("board-credits-excluding-probes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		credits, degraded := h.queryBoardCreditsExcludingProbes(ctx, "", tr)
		if degraded != "" {
			t.Logf("probe-excluded credits degraded view note: %s", degraded)
		}
		t.Logf("probe-excluded credits (1d): %d", credits)
	})
}

// The rebuild-side statements (rollupMainStatement / retireClosedMainMinuteSQL)
// are unexported in bg and are covered by
// bg/stats_minute_rollup_rebuild_integration_test.go — exporting them here just
// to reach them from package admin would widen bg's API for a test's benefit.
