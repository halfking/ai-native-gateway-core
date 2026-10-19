//go:build integration

// migration_877_integration_test.go — migration 877 真库验证（planner 层）。
//
// 为什么这道门不是「索引存在」的断言：
//
// 「索引存在」是**空表上恒真**的判据 —— 一张零行的表上建没建索引都能通过，
// 而缺陷本体恰恰是「6.4GB 的分区上这条谓词走顺序扫描」。所以这道门断言的
// 不是 catalog 里有没有那条索引，而是 **EXPLAIN 出来的计划里 planner 实际
// 有没有用它**。判据与被测对象同层（PostgreSQL 的 planner），都在真库上。
//
// 覆盖：
//
//	P1  级联：父表 + 两个现存分区 + hot 四处都有该索引（逐 relation 查 pg_index，
//	    **不按索引名 grep** —— PG 按列名自动改名，见文件末教训）。
//	P2  可用性（本门的核心）：在**有选择性的**填充数据上，admin/logs.go:540
//	    同形态谓词 `search_text ILIKE '%…%'` 的计划里出现
//	    `Bitmap Index Scan … <某 search_text trigram 索引>` 且谓词落在
//	    Index Cond（不是 Filter）。
//	P3  变异自证：同一夹具上 DROP 索引后复跑，planner 必须**不再**用它 ——
//	    证明 P2 的判别力来自索引本身，而不是「任何计划都长这样」。
//	P4  幂等：up 连续应用两次无错。
//
// 门控（沿用 migration_532 / 715 的 TEST_PG_URL 约定，指向一次性测试库）：
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/dbname?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigration877 -v -count=1
//
// ⚠ TEST_PG_URL 必须指向可丢弃的一次性库：夹具用 public 下的真名
//   （session_turns / session_turns_hot），与生产同名才能验证级联的真实路径；
//   结束时逐对象 DROP IF EXISTS 清理。
package startup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 夹具行数。选它是因为「够大到 planner 会认真比代价」，又不至于让这道门
// 在 CI 上跑成分钟级。命中率由 fixtureNeedleEvery 控制，必须让 needle
// **有选择性**（见文件末「夹具纪律」）。
const (
	fixtureRows         = 120000
	fixtureNeedleEvery  = 500 // → 命中率 ~0.2%
	fixtureSearchNeedle = "needle"
	fixtureTenant       = "tenant_3"
	fixtureParent       = "session_turns"
	fixtureHot          = "session_turns_hot"
	fixtureCleanupSQL877 = `
DROP VIEW IF EXISTS session_turns_with_current_month;
DROP TABLE IF EXISTS session_turns_2026_10;
DROP TABLE IF EXISTS session_turns_2026_09;
DROP TABLE IF EXISTS session_turns CASCADE;
DROP TABLE IF EXISTS session_turns_hot;
DROP TABLE IF EXISTS st;
DROP TABLE IF EXISTS st_2026_09;
DROP TABLE IF EXISTS st_2026_10;
`
)

// fixtureDDL877 建的是 session 家族的**真实形状**：分区父表 + 两个月分区 +
// hot 表，列只保留本迁移与谓词需要的那些。
//
// ⚠ 这是**模板**不是可直接执行的脚本：填充段落里带 %d/%s 占位（SQL 的
// LIKE 通配符要写成 %%），由 fixtureData 在运行时填。
var fixtureDDL877 = `
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE session_turns (
    id BIGINT,
    tenant_id TEXT,
    search_text TEXT,
    ts TIMESTAMPTZ
) PARTITION BY RANGE (ts);

CREATE TABLE session_turns_2026_09 PARTITION OF session_turns
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE session_turns_2026_10 PARTITION OF session_turns
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

CREATE TABLE session_turns_hot (
    id BIGINT,
    tenant_id TEXT,
    search_text TEXT,
    upstream_protocol TEXT,
    ts TIMESTAMPTZ
);

-- 填充形态照抄 admin/logs.go:540 的真实命中面：tenant_id 等值 + search_text
-- 前导通配符子串。needle 只落在 1/%%d 的行上（%%d 由 fixtureData 填）。
INSERT INTO session_turns (id, tenant_id, search_text, ts)
SELECT g,
       'tenant_' || (g %% 20),
       'req_' || g || ' model=deepseek-chat path=/v1/chat/completions user=alice status=ok'
         || CASE WHEN g %% %d = 0 THEN ' %s' ELSE '' END,
       '2026-09-05'::timestamptz + (g || ' seconds')::interval
FROM generate_series(1, %d) g;

INSERT INTO session_turns_hot (id, tenant_id, search_text, ts)
SELECT id, tenant_id, search_text, ts FROM session_turns LIMIT 1000;

-- 复刻 640 建的那条**死索引**：upstream_protocol 恒 NULL ⇒ 谓词永不成立。
-- 必须先把它建出来，否则 P5「删掉了它」会因夹具里本来就没有而恒真。
CREATE INDEX idx_session_turns_hot_upstream_protocol
    ON public.session_turns_hot (tenant_id, upstream_protocol, ts DESC)
    WHERE upstream_protocol IS NOT NULL;
`

// fixtureData 填上夹具的行数与 needle 命中率。命中必须**有选择性**——
// 命中率接近 100% 时 planner 选顺序扫是正确的（见文件末「夹具纪律」）。
func fixtureData() string {
	return fmt.Sprintf(fixtureDDL877, fixtureNeedleEvery, fixtureSearchNeedle, fixtureRows)
}

func TestMigration877SearchTextTrgm(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 877 integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	scriptConn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	defer func() { _ = scriptConn.Close(ctx) }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	execScript := func(script string) {
		t.Helper()
		if _, err := scriptConn.Exec(ctx, script); err != nil {
			t.Fatalf("exec script: %v", err)
		}
	}

	execScript(fixtureCleanupSQL877)
	// 独立连接做清理（migration_532 的 R42 教训：t.Cleanup 晚于 defer 关连接，
	// 写在已关连接上会静默漏夹具）。
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		conn, cerr := pgx.ConnectConfig(cctx, cfg)
		if cerr != nil {
			t.Errorf("cleanup: connect: %v", cerr)
			return
		}
		defer conn.Close(cctx)
		if _, derr := conn.Exec(cctx, fixtureCleanupSQL877); derr != nil {
			t.Errorf("cleanup: %v", derr)
		}
	})
	execScript(fixtureData())
	execScript("ANALYZE public.session_turns; ANALYZE public.session_turns_hot;")

	migration, err := os.ReadFile("877_session_turns_search_text_trgm.sql")
	if err != nil {
		t.Fatal(err)
	}
	execScript(string(migration))

	// P4 幂等。
	execScript(string(migration))

	// ── P5 附带删掉的死索引（Task 4）：upstream_protocol 那条必须不在了。
	//
	// 这条断言只查「对象在不在」，因为它守的是**一次显式的 DROP**，
	// 不是 planner 行为 —— 与 P2 性质不同，不混淆：P2 之所以要做 planner
	// 断言，正因为「存在」在空表上恒真；而 DROP 的语义就是"让它不存在"，
	// 存在性在这里就是被测语义本身。
	var upstreamIdx int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_class WHERE relname = 'idx_session_turns_hot_upstream_protocol'`).Scan(&upstreamIdx); err != nil {
		t.Fatalf("P5 upstream index check: %v", err)
	}
	if upstreamIdx != 0 {
		t.Errorf("P5: idx_session_turns_hot_upstream_protocol still exists; migration 877 "+
			"drops it because the column is permanently NULL (no writer) and has no reader")
	}

	if _, err := pool.Exec(ctx, "ANALYZE public.session_turns; ANALYZE public.session_turns_hot;"); err != nil {
		t.Fatal(err)
	}

	// ── P1 级联：逐 relation 查 pg_index（不按索引名 grep）。
	// PG 按列名自动改分子索引名（父表 idx_session_turns_search_trgm →
	// 分区 session_turns_2026_09_search_text_idx），按名字查会全查空。
	rels := []string{fixtureParent, fixtureHot, "session_turns_2026_09", "session_turns_2026_10"}
	for _, rel := range rels {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM pg_index i
			  JOIN pg_class c ON c.oid = i.indexrelid
			  JOIN pg_class t ON t.oid = i.indrelid
			  JOIN pg_am a ON a.oid = c.relam
			 WHERE t.relname = $1
			   AND a.amname = 'gin'
			   AND pg_get_indexdef(i.indexrelid) LIKE '%search_text gin_trgm_ops%'
			   AND i.indisvalid`, rel).Scan(&n); err != nil {
			t.Fatalf("P1 %s: %v", rel, err)
		}
		if n != 1 {
			t.Errorf("P1 %s: expected 1 valid gin_trgm index on search_text, got %d", rel, n)
		}
	}

	// ── 谓词取 admin/logs.go:540 的真实形态。
	query := `SELECT id FROM public.session_turns
	          WHERE tenant_id = $1 AND search_text ILIKE '%' || $2 || '%'`

	// ── P2 可用性：planner 必须把谓词吃到索引上。
	// 判据不是「索引存在」，是「计划里有 Bitmap Index Scan 打在该索引上，
	// 且 Recheck Cond/Index Cond 里出现 ILIKE 谓词」。
	plan := explainText(ctx, t, pool, query, fixtureTenant, fixtureSearchNeedle)
	if !planUsesTrgmIndex(plan) {
		t.Errorf("P2: planner did not use the search_text trigram index.\nplan:\n%s\n"+
			"This is the whole point of migration 877: without the index this predicate "+
			"seq-scans every month partition.", plan)
	}

	// ── P3 变异自证：把索引摘掉，planner 必须不再用它。
	// 没有这一步，P2 可能恒真（例如谓词退化、夹具空、planner 版本差异）。
	// 这段是**门内自带的反证**，不是靠人手点掉索引再看。
	execScript("DROP INDEX IF EXISTS public.idx_session_turns_search_trgm;")
	if _, err := pool.Exec(ctx, "ANALYZE public.session_turns;"); err != nil {
		t.Fatal(err)
	}
	planNoIdx := explainText(ctx, t, pool, query, fixtureTenant, fixtureSearchNeedle)
	if planUsesTrgmIndex(planNoIdx) {
		t.Errorf("P3: planner STILL used a search_text trigram index after DROP INDEX — "+
			"the fixture is not measuring what it claims to.\nplan:\n%s", planNoIdx)
	}
	// 复原，让 P4 之后的状态与迁移产物一致（不影响判定，纯粹为可读性）。
	execScript(string(migration))
}

// explainText 跑 EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) 并取回文本。
func explainText(ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	explainQuery := "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) " + query
	rows, err := pool.Query(ctx, explainQuery, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	return strings.Join(out, "\n")
}

// planUsesTrgmIndex 判「计划里确实有 Bitmap Index Scan 打在该 trigram 索引上」。
//
// 两个必要条件，都不可省：
//   - 有 `Bitmap Index Scan ... <name>_search_text_idx` 这一行；
//   - 谓词落在 `Index Cond:`（而不是只有 Filter:）——
//     判据纪律：谓词被 planner 忽略、只剩 Filter 时，索引存在也是白存在。
func planUsesTrgmIndex(plan string) bool {
	var (
		bitmapOnTrgm bool
		indexCond    bool
	)
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Bitmap Index Scan") && strings.Contains(line, "search_text_idx") {
			bitmapOnTrgm = true
		}
		if bitmapOnTrgm && strings.Contains(line, "Index Cond:") && strings.Contains(line, "search_text") {
			indexCond = true
		}
	}
	return bitmapOnTrgm && indexCond
}
