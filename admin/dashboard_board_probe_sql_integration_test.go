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
// 视图必须真的有谓词引用的三列。
//
// 这三张源表（request_logs_hot / request_logs）在 sql/schema/01-schema.sql 里
// 都声明了这些列，而 request_logs_with_current_month_without_customer_id 是
// 二者按 hot 列序求交集派生（db/request_logs_view_schema.go:103-131）——
// 但那是「按仓内 schema 文件推断」。真库可能因迁移顺序/回滚/自愈重建链而与之
// 不一致，那会让看板整体 42703。
func TestDashboardBoardPredicateColumnsExistOnBoardView(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	from, _ := boardRequestLogsFromClause()
	// boardRequestLogsFromClause 返回 "<view> AS <alias>"，取裸视图名。
	resolved := strings.TrimSpace(strings.SplitN(from, " AS ", 2)[0])
	if resolved == "" {
		t.Fatalf("could not derive a view name from %q", from)
	}

	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
		  AND a.attnum > 0 AND NOT a.attisdropped
	`, resolved)
	if err != nil {
		t.Fatalf("describe %s: %v", resolved, err)
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		present[name] = true
	}
	if len(present) == 0 {
		t.Fatalf("view %s reported zero columns — it is missing on this database", resolved)
	}

	// 谓词的三条臂各引用一列；少一列就是每次看板调用都 42703。
	for _, col := range []string{"quality_flags", "task_type", "origin_actor"} {
		if !present[col] {
			t.Errorf("view %s has no column %q — the board probe exclusion would 42703 on "+
				"every /api/admin/dashboard/board call", resolved, col)
		}
	}
	t.Logf("%s: %d columns; predicate columns quality_flags/task_type/origin_actor all present",
		resolved, len(present))
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
	var keptAsIs, keptDefaulted, nullFlags int64
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		WITH actual AS (
		    SELECT quality_flags, task_type, origin_actor
		    FROM request_logs_with_current_month_without_customer_id
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
	`, pred)).Scan(&keptAsIs, &keptDefaulted, &nullFlags)
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
		summary := h.fallbackBoardSummary(ctx, "", tr)
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
		credits := h.queryBoardCreditsExcludingProbes(ctx, "", tr)
		t.Logf("probe-excluded credits (1d): %d", credits)
	})
}

// The rebuild-side statements (rollupMainStatement / retireClosedMainMinuteSQL)
// are unexported in bg and are covered by
// bg/stats_minute_rollup_rebuild_integration_test.go — exporting them here just
// to reach them from package admin would widen bg's API for a test's benefit.
