package admin

// logs_serial_plan_test.go — 2026-11-23 request-logs「有 count 无 items」根修的门。
//
// 生产实证（211，2026-11-23 02:46 owner 报障 + 02:52 三连复现）：列表页查询被
// planner 选成 Gather Merge（2 并行 worker）⇒ acc-pg17 /dev/shm=64MB、水位
// ~35MB ⇒ worker DSM resize 8MB 撞 SQLSTATE 53100 ⇒ pgx 游标在 Next() 中途
// 死亡 ⇒ 旧代码在 rows.Err 分支只打 WARN ⇒ **返回 200 + items=[]**，而 COUNT
// 聚合照常成功 ⇒ 前端拿到 count=66235 + items=[] ⇒ 用户看到「查询到 66235
// 条记录，但无法正常显示」。
//
// 修复两件事，各配一道门：
//  1. listLogs 三条语句（聚合 / by_model / 页查询）全走**同一只读事务**，事务
//     内 set_config('max_parallel_workers_per_gather','0',true)（SET LOCAL）
//     强制串行 —— 真库 tracer 门钉「先关并行、后发语句」的次序与 is_local=true；
//     静态门钉「函数体内不再有池直连查询」（谁改回 h.db.* 谁就重新打开雷区）。
//  2. rows.Err 中途死亡 → **503 如实报错**（事实+性质+处置），不再静默 200；
//     静态门钉住 503 接线与旧的「全行扫描失败→500」闸并存。
//
// ⚠ 门禁纪律：跳过**不构成**证据（没配 TEST_PG_URL 时 t.Skip，文案同
// logs_single_scan_test.go 的先例）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestListLogs_RunsAllQueriesInSerialPlanTx(t *testing.T) {
	pool, tracer := logsRealDBPoolWithTracer(t)
	h := &Handler{db: pool}

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-24 * time.Hour)
	q := fmt.Sprintf("/api/logs?from=%s&to=%s&page=1&page_size=5",
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	req := httptest.NewRequest(http.MethodGet, q, nil)
	rec := httptest.NewRecorder()

	h.listLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("listLogs status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	sqls := tracer.snapshot()

	// ── 判据 ①：set_config 恰好一次，且 is_local=true（SET LOCAL）──────
	// 第三参若是 false（会话级），GUC 会泄漏给下一个租用同一池连接的请求
	// —— 全局关并行的副作用（FIXME-14377 实测对部分大会话查询 2.4× 变慢）。
	gucIdx := -1
	for i, s := range sqls {
		if !strings.Contains(s, "max_parallel_workers_per_gather") {
			continue
		}
		if gucIdx >= 0 {
			t.Fatalf("parallel-off GUC issued more than once; SQL trace:\n%s", strings.Join(sqls, "\n---\n"))
		}
		gucIdx = i
		if !strings.Contains(s, "'0', true)") {
			t.Fatalf("parallel-off GUC must use set_config(..., true) = SET LOCAL so the setting is restored "+
				"when the tx ends; a session-level SET leaks onto pooled connections. Got: %s", s)
		}
	}
	if gucIdx < 0 {
		t.Fatalf("listLogs must disable parallel plans (SET LOCAL max_parallel_workers_per_gather=0) inside its tx — "+
			"the page query's Gather Merge hits SQLSTATE 53100 on /dev/shm-starved hosts (211 生产实证). "+
			"SQL trace:\n%s", strings.Join(sqls, "\n---\n"))
	}

	// ── 判据 ②：每条读日志源的语句都在 GUC 之后 ─────────────────────
	for i, s := range sqls {
		if isLogsSourceMarker(s) && i < gucIdx {
			t.Fatalf("logs-source query ran BEFORE the parallel-off GUC (index %d < %d): %s", i, gucIdx, s)
		}
	}

	// ── 判据 ③：COUNT 与 SUM 仍合并为一条非分组聚合（sibling 判据①同口径）──
	if scans := countNonGroupedAggregateScans(sqls); scans != 1 {
		t.Fatalf("expected exactly 1 non-grouped aggregate scan over the logs source, got %d — "+
			"the serial-plan tx refactor must not re-split COUNT/SUM. SQL trace:\n%s",
			scans, strings.Join(sqls, "\n---\n"))
	}

	// ── 判据 ④：by_model（有数据时）也在 GUC 之后 ────────────────────
	for i, s := range sqls {
		if strings.Contains(s, "GROUP BY") && isLogsSourceMarker(s) && i < gucIdx {
			t.Fatalf("by_model aggregate ran BEFORE the parallel-off GUC (index %d < %d): %s", i, gucIdx, s)
		}
	}
}

// TestListLogs_SerialPlanStaticWiring 是不依赖真库的静态接线门：钉住
// listLogs 的「全部语句走串行事务」与「rows.Err → 503 如实报错」两处接线，
// 防止未来改回池直连/静默吞错（回归即重开 53100 雷区或假空列表）。
func TestListLogs_SerialPlanStaticWiring(t *testing.T) {
	raw, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	src := string(raw)

	// ① 常量字节形态：is_local=true 必须在场（false = 会话级 SET = 池泄漏）。
	const gucShape = "SELECT set_config('max_parallel_workers_per_gather', '0', true)"
	if !strings.Contains(src, gucShape) {
		t.Fatalf("listLogsSerialPlanSQL must stay %q (SET LOCAL semantics) — got a drifted form", gucShape)
	}

	lo := strings.Index(src, "func (h *Handler) listLogs")
	hi := strings.Index(src, "func (h *Handler) getLog")
	if lo < 0 || hi < 0 || hi <= lo {
		t.Fatalf("cannot locate listLogs body in logs.go (lo=%d hi=%d)", lo, hi)
	}
	body := src[lo:hi]

	// ② 三条语句全走 tx：函数体内不允许池直连查询。
	if strings.Contains(body, "h.db.Query") {
		t.Fatalf("listLogs must issue every statement through the serial-plan tx; found a direct h.db.Query — " +
			"pool-direct queries can be planned as Gather Merge and hit SQLSTATE 53100 on /dev/shm-starved hosts")
	}
	if !strings.Contains(body, "h.db.Begin(ctx)") {
		t.Fatalf("listLogs must open its serial-plan tx (h.db.Begin(ctx))")
	}
	if !strings.Contains(body, "tx.Commit(ctx)") {
		t.Fatalf("listLogs must commit its serial-plan tx (tx.Commit(ctx))")
	}

	// ③ rows.Err 中途死亡 → 503 如实报错（不得静默 200 + items=[]）。
	if !strings.Contains(body, "rows iteration failed") || !strings.Contains(body, "StatusServiceUnavailable") {
		t.Fatalf("listLogs rows.Err branch must return 503 with an honest client-facing message " +
			"(facts: rows received / counted; nature: storage-side; disposition: retryable) — " +
			"a silent 200+items=[] shows users the misleading \"data format\" banner")
	}

	// ④ 旧闸仍在：全行扫描失败 → 500（e428167e5 的闸，与本轮 503 并存）。
	if !strings.Contains(body, "data scanning failed") {
		t.Fatalf("the all-rows-scan-failed 500 gate (data scanning failed) must stay in listLogs")
	}

	// ⑤ by_model 游标在事务内显式关闭（defer Close 是池时代形态，
	// 配单连接事务会把游标拖到函数尾 ⇒ 分页页查询没法定义在同一条连接上）。
	if strings.Contains(body, "defer byModelRows.Close()") {
		t.Fatalf("by_model cursor must be closed explicitly before the page query (tx holds one connection); defer is the pool-era shape")
	}
	if !strings.Contains(body, "byModelRows.Close()") {
		t.Fatalf("by_model cursor must be closed explicitly (byModelRows.Close()) before the page query")
	}

	_ = os.Getenv // 保持 os 导入在静态门与真库门共用的文件里可用
}

// 防止未配 TEST_PG_URL 时真库门静默被当证据：本文件首注已写明纪律，
// 这里只钉文案形态（跳过必须自述「跳过不构成证据」）。
func TestListLogs_RealDBGateSkipDiscipline(t *testing.T) {
	raw, err := os.ReadFile("logs_single_scan_test.go")
	if err != nil {
		t.Skip("logs_single_scan_test.go not present in this checkout — nothing to pin")
	}
	if !strings.Contains(string(raw), "skipping does NOT constitute evidence") {
		t.Fatalf("the shared real-DB helper must keep the skip discipline wording")
	}
}
