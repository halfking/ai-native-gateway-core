// auto_cool_hot_realdb_test.go — 2026-10-13 252 SQL 日志审计轮：
// checkAutoCool 从 canonical 视图改读 hot 双写表后的真库回归门
// （admin/sliding_window_realdb_test.go 同款模板，R32 定版）。
//
// 三层断言：
//  1. 生产同一文本经 SimpleProtocol 池在真库解析执行（SimpleProtocol 是
//     生产全量协议，参数内联语义只有该模式可证）；
//  2. 计划只读 hot 三表、不触碰 canonical 视图（回归点：查询文本漂移回
//     视图形态——视图三臂对 5 分钟窗是纯税，252 实测饱和期 avg 3.5s/次）；
//  3. 同窗口 win 形状对拍：hot 形态 vs 冻结的视图参考形态 EXCEPT 双向
//     差集必须为空（窗口 ≤ hot promote retention 时的语义等价性证据；
//     252 生产库 2026-10-13 实测 0/0）。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。只读查询零足迹。
package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func autoCoolRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	// 生产全量 SimpleProtocol（db/db.go）——参数内联语义只有该模式可证。
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func autoCoolHotTablesPresent(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.session_turns_hot') IS NOT NULL
		     AND to_regclass('public._request_logs_hot') IS NOT NULL
		     AND to_regclass('public.candidate_failure_logs_hot') IS NOT NULL`,
	).Scan(&ok); err != nil {
		t.Fatalf("probe hot tables: %v", err)
	}
	return ok
}

// autoCoolWinHotSQL 构造与 checkAutoCool 生产查询完全同文本的 win CTE
// （改动查询时必须同步这里；漂移由计划断言兜底不由本注释兜底）。
func autoCoolWinHotSQL(window string) string {
	return `
		WITH win AS (
		    SELECT credential_id,
		           COUNT(*) FILTER (
		               WHERE lower(COALESCE(request_status, '')) = 'failure'
		                 AND COALESCE(error_kind, '') NOT LIKE 'probe_direct_%'
		           ) AS fails,
		           COUNT(*) AS attempts
		    FROM (
		        SELECT (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END) AS credential_id,
		               (CASE WHEN t.success IS NULL THEN NULL
		                     WHEN t.success THEN 'success'
		                     WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'
		                     WHEN t.status_code = 429 THEN 'rate_limited'
		                     ELSE 'failure' END) AS request_status,
		               t.error_kind
		        FROM session_turns_hot t
		        WHERE t.ts >= now() - interval '` + window + `'
		        UNION ALL
		        SELECT r.credential_id, r.request_status, r.error_kind
		        FROM _request_logs_hot r
		        WHERE r.ts >= now() - interval '` + window + `'
		          AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = r.request_id)
		    ) cand
		    WHERE credential_id IS NOT NULL
		    GROUP BY credential_id
		)
		SELECT credential_id, attempts, fails FROM win
	`
}

// autoCoolWinViewSQL 是改写前生产查询的 win 形状（canonical 视图形态），
// 冻结为对拍参考。视图投影/反连接演进时这里**不跟随**——它是历史语义
// 锚；对拍失败说明 hot 形态与视图语义出现分叉，必须人工裁决哪边对。
func autoCoolWinViewSQL(window string) string {
	return `
		WITH win AS (
		    SELECT credential_id,
		           COUNT(*) FILTER (
		               WHERE lower(COALESCE(request_status, '')) = 'failure'
		                 AND COALESCE(error_kind, '') NOT LIKE 'probe_direct_%'
		           ) AS fails,
		           COUNT(*) AS attempts
		    FROM request_logs_with_current_month
		    WHERE ts >= now() - interval '` + window + `'
		      AND credential_id IS NOT NULL
		    GROUP BY credential_id
		)
		SELECT credential_id, attempts, fails FROM win
	`
}

func autoCoolScanWinRows(t *testing.T, rows pgx.Rows) map[[3]string]struct{} {
	t.Helper()
	defer rows.Close()
	set := map[[3]string]struct{}{}
	for rows.Next() {
		var credID, attempts, fails string
		if err := rows.Scan(&credID, &attempts, &fails); err != nil {
			t.Fatalf("scan win row: %v", err)
		}
		set[[3]string{credID, attempts, fails}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return set
}

// TestAutoCoolHotWindow_RealDB 生产同一文本执行 + 计划断言：auto-cool 的
// win 臂必须只读 hot 双写表，不得触碰 canonical 视图（回归点：查询文本
// 漂移回视图形态）。
func TestAutoCoolHotWindow_RealDB(t *testing.T) {
	pool := autoCoolRealDBPool(t)
	ctx := context.Background()
	if !autoCoolHotTablesPresent(t, pool) {
		t.Skip("hot 双写表不在库（迁移未应用），跳过")
	}

	rows, err := pool.Query(ctx, autoCoolWinHotSQL("5"))
	if err != nil {
		t.Fatalf("auto-cool hot win query failed under SimpleProtocol: %v", err)
	}
	autoCoolScanWinRows(t, rows)

	expl, err := pool.Query(ctx, "EXPLAIN "+autoCoolWinHotSQL("5"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer expl.Close()
	var sb strings.Builder
	for expl.Next() {
		var line string
		if err := expl.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		sb.WriteString(line)
	}
	if err := expl.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	plan := sb.String()
	if strings.Contains(plan, "with_current_month") {
		t.Fatalf("auto-cool win 计划触碰 canonical 视图（应只读 hot 双写表）:\n%s", plan)
	}
	for _, tbl := range []string{"session_turns_hot", "_request_logs_hot"} {
		if !strings.Contains(plan, tbl) {
			t.Fatalf("auto-cool win 计划缺 %s 臂:\n%s", tbl, plan)
		}
	}
}

// TestAutoCoolHotParity_RealDB 同窗口双形态对拍：窗口 ≤ hot promote
// retention 时 hot 形态与冻结的视图参考形态必须逐行一致（attempts/fails
// 聚合行级 EXCEPT 双向差集为空）。5 分钟与 60 分钟双窗跑（60min 仍在
// 保留期内且更容易有流量，避免空对空假绿）。两侧都空也是合法形态
// （静默窗口），结构性保证来自本文件计划断言 + 252 生产库 2026-10-13
// 轮报告的 0/0 实测。
func TestAutoCoolHotParity_RealDB(t *testing.T) {
	pool := autoCoolRealDBPool(t)
	if !autoCoolHotTablesPresent(t, pool) {
		t.Skip("hot 双写表不在库（迁移未应用），跳过")
	}
	for _, win := range []string{"5", "60"} {
		hotRows, err := pool.Query(context.Background(), autoCoolWinHotSQL(win))
		if err != nil {
			t.Fatalf("hot win(%sm) failed: %v", win, err)
		}
		hot := autoCoolScanWinRows(t, hotRows)
		viewRows, err := pool.Query(context.Background(), autoCoolWinViewSQL(win))
		if err != nil {
			t.Fatalf("view win(%sm) failed: %v", win, err)
		}
		view := autoCoolScanWinRows(t, viewRows)
		if len(hot) != len(view) {
			t.Fatalf("窗口 %smin 双形态行数不一致: hot=%d view=%d", win, len(hot), len(view))
		}
		for k := range hot {
			if _, ok := view[k]; !ok {
				t.Fatalf("窗口 %smin: hot 独有 win 行 %v 不在视图结果中", win, k)
			}
		}
	}
}
