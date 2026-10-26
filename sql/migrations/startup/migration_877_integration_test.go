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
//
//	（session_turns / session_turns_hot），与生产同名才能验证级联的真实路径；
//	结束时逐对象 DROP IF EXISTS 清理。
package startup

import (
	"bytes"
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
	fixtureRows          = 120000
	fixtureNeedleEvery   = 500 // → 命中率 ~0.2%
	fixtureSearchNeedle  = "needle"
	fixtureTenant        = "tenant_3"
	fixtureParent        = "session_turns"
	fixtureHot           = "session_turns_hot"
	generatorMarker      = "SELECT format(\n  'CREATE INDEX CONCURRENTLY"
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
		t.Fatalf("TEST_PG_URL IS SET but unreachable (%v) — a misconfigured CI credential silently skips this gate and the package still reports ok; fix the DSN or unset the variable", err)
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

	// applyMigration877 应用**真实文件** 877，处理 \gexec。
	//
	// \gexec 是 psql 元命令，不是 SQL；pgx 会把它当 SQL 发给 PG 然后语法错。
	// 走真 psql 也不可行：部署机上 psql 在 PG 容器里（installer 用的是
	// `docker exec <container> psql`），宿主 PATH 上并没有；本机的
	// ~/kaixuan/pg18/bin/psql 是**转发到线上 llm-gateway-pg 容器的 shim**，
	// 拿它跑这道门等于打生产。所以这里自己实现 \gexec 的语义：
	//   1) \gexec 之前的最后一条语句是 SELECT format(...)，跑它、取回生成的 DDL；
	//   2) 逐条执行那些 DDL；
	//   3) 执行 \gexec 之后的剩余部分。
	// 这样测的仍然是**真实文件的真实 DDL**，且不依赖宿主 psql。
	// 耦合点只有一个：生成器 SELECT 必须是 \gexec 前最后一条语句。上面那道
	// generatorMarker 门在它不再成立时立刻红，而不是静默测了个空。
	applyMigration877 := func(script []byte, what string) {
		t.Helper()
		// 按**整行**切，不用裸子串：877 的文件头注释里就出现了 \gexec 这个词
		// （描述三段式），裸切会把注释从中间劈开。整行形式只匹配真正的元命令。
		const gexecLine = "\n\\gexec\n"
		segments := bytes.Split(script, []byte(gexecLine))
		if len(segments) < 2 {
			t.Fatalf("%s: 877 应当至少有一个独占一行的 \\gexec 元命令", what)
		}
		for i, seg := range segments {
			gen := bytes.LastIndex(seg, []byte(generatorMarker))
			if gen < 0 {
				// 普通段：整段自动提交执行。
				//
				// 这里**刻意不做**文本扫描去禁止 "CREATE INDEX CONCURRENTLY"
				// 出现在普通段：877 的注释与 RAISE 文案里多处出现该词句，
				// 任何子串判定都会被自己的说明文字绊倒（本轮已因此误报两次）。
				// 而且不需要它——若某段真的混入了 CONCURRENTLY，PG 会直接回
				// SQLSTATE 25001，错误信息（"CREATE INDEX CONCURRENTLY cannot
				// run inside a transaction block"）比任何文本守卫都准。
				// 首次改机制时正是靠这条真实错误发现问题的。
				if _, err := scriptConn.Exec(ctx, string(seg)); err != nil {
					t.Fatalf("%s: 第 %d 段执行失败: %v", what, i, err)
				}
				continue
			}
			prefix, generator := seg[:gen], strings.TrimSpace(strings.TrimSuffix(
				strings.TrimSpace(string(seg[gen:])), ";"))
			if _, err := scriptConn.Exec(ctx, string(prefix)); err != nil {
				t.Fatalf("%s: 第 %d 段前置语句失败: %v", what, i, err)
			}
			rows, err := scriptConn.Query(ctx, generator)
			if err != nil {
				t.Fatalf("%s: 第 %d 段生成器 SELECT 失败: %v", what, i, err)
			}
			var generated []string
			for rows.Next() {
				var stmt string
				if err := rows.Scan(&stmt); err != nil {
					rows.Close()
					t.Fatalf("%s: 第 %d 段扫生成语句失败: %v", what, i, err)
				}
				generated = append(generated, stmt)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatalf("%s: 第 %d 段生成器迭代失败: %v", what, i, err)
			}
			for _, stmt := range generated {
				// 每条都是 CREATE INDEX CONCURRENTLY —— 必须**各自**自动提交。
				// 这里逐条 Exec，形态与生产（\\gexec 逐条发）一致。
				if _, err := scriptConn.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s: 第 %d 段执行 %q 失败: %v", what, i, stmt, err)
				}
			}
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

	// ★ 通道门：877 必须携带 dbinit:no-transaction 标记。
	//   applySQL 在没有该标记时会给整个文件加 --single-transaction，而
	//   CREATE INDEX CONCURRENTLY 在事务块内是非法的（718 文件头记的就是
	//   这个失败面："无该标记时全新安装会失败于 CREATE INDEX CONCURRENTLY
	//   cannot run inside a transaction block"）。这条判据守的是**部署通道**
	//   而不是索引行为——今天为绿，丢掉标记即红。
	if !bytes.Contains(migration, []byte("dbinit:no-transaction")) {
		t.Errorf("877 含 CREATE INDEX CONCURRENTLY，必须携带 dbinit:no-transaction 标记；" +
			"否则 installer applySQL 的 --single-transaction 会让它在全新安装上失败")
	}

	applyMigration877(migration, "apply 877")

	// P4 幂等。
	applyMigration877(migration, "re-apply 877 (idempotence)")

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
		t.Errorf("P5: idx_session_turns_hot_upstream_protocol still exists; migration 877 " +
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

	// ── P6 自愈：模拟 applySQL 的 5 分钟 ctx 掐断 CONCURRENTLY 的残留
	// （indisvalid=false 的半成品索引），复跑 877 必须把它 DROP 并重建为 valid。
	//
	// 为什么这条重要：IF NOT EXISTS 只判存在性，对 invalid 对象**不生效**。
	// 所以「重跑一次」在有残留时会看起来跑了、实际什么都没修——索引在、
	// 名字对、indisvalid=false，而 planner 不用它。那正是「索引明明在，
	// EXPLAIN 却还在 Seq Scan」最难查的一种形态。877 的第 0 段就是为它写的。
	//
	// 需要改 pg_catalog（非超级用户会失败），失败则跳过本条而不是判红：
	// 这道门本来就跑在可丢弃的一次性库上，跳过不影响其他判据的有效性。
	if _, err := pool.Exec(ctx, `UPDATE pg_index SET indisvalid = false, indisready = false
		WHERE indexrelid = to_regclass('public.idx_session_turns_hot_search_trgm')`); err != nil {
		t.Logf("P6 skipped: cannot write pg_catalog (%v); needs a superuser test DB", err)
	} else {
		var invalid int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index
			WHERE indexrelid = to_regclass('public.idx_session_turns_hot_search_trgm')
			  AND NOT indisvalid`).Scan(&invalid); err != nil {
			t.Fatalf("P6 seed check: %v", err)
		}
		if invalid != 1 {
			t.Fatalf("P6 setup: expected to mark the hot index invalid, still %d invalid", invalid)
		}
		applyMigration877(migration, "re-apply 877 after simulated ctx kill")
		var stillInvalid int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index
			WHERE indexrelid = to_regclass('public.idx_session_turns_hot_search_trgm')
			  AND NOT indisvalid`).Scan(&stillInvalid); err != nil {
			t.Fatalf("P6 post check: %v", err)
		}
		if stillInvalid != 0 {
			t.Errorf("P6: 877 的第 0 段没有清掉 indisvalid=false 的残留；"+
				"IF NOT EXISTS 对 invalid 对象不生效，重跑会假装修好了（stillInvalid=%d）", stillInvalid)
		}
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
	applyMigration877(migration, "restore 877 after P3")
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
		if strings.Contains(line, "Bitmap Index Scan") && strings.Contains(line, "search_trgm") {
			bitmapOnTrgm = true
		}
		if bitmapOnTrgm && strings.Contains(line, "Index Cond:") && strings.Contains(line, "search_text") {
			indexCond = true
		}
	}
	return bitmapOnTrgm && indexCond
}
