// Package stress - D07 压力测试：request_logs 归档链路的真实执行与真实成本。
//
// S-01 首轮（2026-09-29，本机 PostgreSQL 17.10）在这个包里钉死了两类
// 「静态形状门禁结构上抓不到」的缺陷：
//
//	S-01a TestStress_ArchiveRequestLogs_ExecutesOnBaselineShape
//	      列名不存在（SQLSTATE 42703）。在一次性 scratch 库里，用**仓库自己的
//	      基线 DDL** 建 request_logs、用**仓库自己的 754 迁移文件**建函数，
//	      再调用 archive_request_logs_default()。投影里再出现一个不存在的列，
//	      这里就拿到 42703。零手写列名，因此不会随真实 schema 漂移成假绿。
//
//	S-01b TestStress_ArchiveRequestLogs_BatchCursorIsIndexed
//	      计划形状。754 的批游标是 `WHERE id > :last ORDER BY id LIMIT 1000`，
//	      而 request_logs **没有任何以 id 为首列的索引**——首轮 EXPLAIN 实测，
//	      这让每一批都退化成全分区并行顺序扫描（取 1000 行读了 531,262 个
//	      缓冲块）。本门 EXPLAIN 真实的批次查询并断言走索引。
//
//	S-01c TestStress_ArchiveRequestLogs_RerunIsIdempotent
//	      ON CONFLICT 幂等与重跑成本（754 没有「已归档」标记）。
//
//	S-01d TestStress_ArchiveRequestLogs_RetentionGuard
//	      7..365 留存联锁。
//
// 首轮生产规模实测结论（真数据形状，见 reports/latest.md）：
//
//	2,125,857 行 / 6070 MB 月分区冷归档 —— 无 id 索引时在 30:00.028 被
//	statement_timeout 击杀并整体 ROLLBACK；建索引后 25.96s 跑完，热重跑 8.18s。
//
// 跑测（无库自动 skip，有库即跑；需要带 CREATEDB 的角色）：
//
//	D07_S01_PG_URL=postgres://user:pass@127.0.0.1:5432/postgres?sslmode=disable \
//	  go test -timeout 300s ./tests/48h-audit/D07-hot-columnar/stress/...
package stress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	baselineSchemaRel = "installer/cmd/llm-gw-installer/embeddata/01-schema.sql"
	// canonical lives in sql/migrations/startup/ — that is where
	// migration_754_test.go pins the contract; the installer ships a byte copy
	// under embeddata/startup/, asserted identical by ../data.
	migration754Rel = "sql/migrations/startup/754_archive_request_logs_default.sql"
	migration756Rel = "sql/migrations/startup/756_request_logs_id_index.sql"

	// scratchRequestLogsRows is deliberately small: these gates prove the SQL
	// *executes* and *plans correctly*, not that it is fast at production
	// volume (that is what reports/latest.md records). 5k rows over a 1000-row
	// cursor is 5 batches plus the terminating short batch.
	scratchRequestLogsRows = 5000

	partAug = "CREATE TABLE request_logs_2026_08 PARTITION OF request_logs FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08')"
	partSep = "CREATE TABLE request_logs_2026_09 PARTITION OF request_logs FOR VALUES FROM ('2026-09-01 00:00:00+08') TO ('2026-10-01 00:00:00+08')"
)

func repoRoot(t testing.TB) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}

func readRepoFile(t testing.TB, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// executableSQL strips psql meta-commands and `--` comments from a migration
// file. Stripping the comments matters: a column name that only appears in
// prose must never be able to satisfy a gate.
func executableSQL(t testing.TB, rel string) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(readRepoFile(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, `\`) || trimmed == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// liveDSN resolves a maintenance DSN. The gates create and drop their own
// scratch database through it, so it must point at any database on a server
// where the role may CREATE DATABASE.
func liveDSN(t testing.TB) string {
	t.Helper()
	for _, k := range []string{"D07_S01_PG_URL", "TEST_PG_URL", "LLM_GATEWAY_PG_URL", "TEST_DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	t.Skip("no live PostgreSQL DSN (D07_S01_PG_URL / TEST_PG_URL / LLM_GATEWAY_PG_URL / TEST_DATABASE_URL)")
	return ""
}

// withScratchDB creates a throwaway database, hands a connection to fn, and
// always drops it. Every gate in this file runs in its own scratch database so
// that a stale table or function from an earlier environment can never make a
// gate pass for the wrong reason.
func withScratchDB(t *testing.T, timeout time.Duration, fn func(ctx context.Context, conn *pgx.Conn)) {
	t.Helper()
	baseDSN := liveDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	scratch := fmt.Sprintf("d07_%s_%d_%d", strings.ToLower(t.Name()[len("TestStress_"):]),
		os.Getpid(), time.Now().UnixNano()%1_000_000)
	// The name must be a valid identifier; test names contain underscores only,
	// but guard anyway rather than emit unquoted SQL built from a test name.
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(scratch) {
		t.Fatalf("derived scratch database name %q is not a plain identifier", scratch)
	}

	admin, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect to %s: %v", redact(baseDSN), err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+scratch); err != nil {
		admin.Close(ctx)
		t.Fatalf("CREATE DATABASE %s: %v (gate needs a role with CREATEDB; it builds and drops its "+
			"own throwaway database)", scratch, err)
	}
	defer func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 60*time.Second)
		defer c()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)"); err != nil {
			t.Logf("cleanup: DROP DATABASE %s: %v", scratch, err)
		}
		admin.Close(cleanupCtx)
	}()

	conn, err := pgx.Connect(ctx, strings.Replace(baseDSN, "/postgres", "/"+scratch, 1))
	if err != nil {
		t.Fatalf("connect to scratch db: %v", err)
	}
	// Verify we really landed in the database we just created — a DSN whose
	// path segment is not literally /postgres would otherwise silently run the
	// gate against a pre-existing database.
	var landedIn string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&landedIn); err != nil || landedIn != scratch {
		conn.Close(ctx)
		t.Fatalf("scratch DSN substitution failed (query err=%v, landed on %q, want %q): use a DSN "+
			"whose database path segment is literally /postgres", err, landedIn, scratch)
	}
	defer conn.Close(ctx)

	for _, ext := range []string{"pg_trgm", "btree_gin"} {
		if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext); err != nil {
			t.Logf("extension %s unavailable (%v) — continuing, baseline may not need it here", ext, err)
		}
	}
	fn(ctx, conn)
}

// baselineRequestLogsDDL extracts the verbatim CREATE TABLE for request_logs
// from the repository baseline. It is executed as-is — no column list is ever
// restated here, which is the whole point: the gate cannot drift away from the
// real schema the way a hand-written INSERT list would.
func baselineRequestLogsDDL(t testing.TB) string {
	t.Helper()
	src := readRepoFile(t, baselineSchemaRel)
	anchor := regexp.MustCompile(`CREATE TABLE (?:IF NOT EXISTS )?(?:public\.)?request_logs\s*\(`).FindStringIndex(src)
	if anchor == nil {
		t.Fatalf("%s: no `CREATE TABLE ... request_logs (` found", baselineSchemaRel)
	}
	// The statement opens at anchor[0] and ends at the first ";" that follows
	// the column list — request_logs has no semicolon inside its column
	// defaults. The drift check below proves we stopped at the right place
	// rather than running on into the views defined after the table.
	relEnd := strings.Index(src[anchor[1]:], ";")
	if relEnd < 0 {
		t.Fatalf("%s: unterminated request_logs CREATE TABLE", baselineSchemaRel)
	}
	stmt := strings.TrimSpace(src[anchor[0] : anchor[1]+relEnd+1])
	if !strings.Contains(stmt, "PARTITION BY RANGE (ts)") {
		t.Fatalf("%s: extracted statement (%d chars) is not the partitioned parent — the extractor "+
			"ran past the table into a later object:\n%s",
			baselineSchemaRel, len(stmt), truncate(stmt, 300))
	}
	return stmt
}

// createSequencesForDDL creates every sequence the extracted DDL's column
// defaults reference. The gate executes the baseline statement verbatim and the
// baseline assumes its sequence already exists, so the gate supplies it rather
// than editing the statement.
func createSequencesForDDL(t testing.TB, ctx context.Context, conn *pgx.Conn, ddl string) {
	t.Helper()
	for _, m := range regexp.MustCompile(`nextval\('([^']+)'::regclass\)`).FindAllStringSubmatch(ddl, -1) {
		if _, err := conn.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS "+m[1]); err != nil {
			t.Fatalf("create sequence %s referenced by the baseline DDL: %v", m[1], err)
		}
	}
}

func mustExec(t testing.TB, ctx context.Context, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("exec failed: %v\n--- sql ---\n%s", err, truncate(sql, 400))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// installBaseline builds the partitioned request_logs parent (plus the two
// month partitions the archive cares about) from the repository's own DDL.
func installBaseline(t *testing.T, ctx context.Context, conn *pgx.Conn, withSepPartition bool) {
	t.Helper()
	ddl := baselineRequestLogsDDL(t)
	createSequencesForDDL(t, ctx, conn, ddl)
	mustExec(t, ctx, conn, ddl)
	mustExec(t, ctx, conn, partAug)
	if withSepPartition {
		mustExec(t, ctx, conn, partSep)
	}
}

// seedValueFor gives every NOT NULL column without a DEFAULT a value. An
// unmapped column fails loudly instead of silently inserting NULL.
func seedValueFor(col string) (string, bool) {
	switch col {
	case "id":
		return "", true // filled from generate_series
	case "request_id":
		return "'req-' || g", true
	case "tenant_id":
		return "'default'", true
	case "success":
		return "(g % 23 <> 0)", true
	case "gw_session_id":
		return "'sess-' || (g % 1000)", true
	case "provider_model":
		return "'claude-sonnet-4-5'", true
	case "upstream_status_code":
		return "200", true
	case "prompt_tokens":
		return "(100 + g % 4000)", true
	case "completion_tokens":
		return "(100 + g % 2000)", true
	case "cost_usd":
		return "((g % 10000)::numeric / 100000)", true
	case "request_status":
		return "(CASE WHEN g % 23 = 0 THEN 'failed' ELSE 'success' END)", true
	case "canonical_model":
		return "'claude-sonnet-4-5'", true
	}
	return "", false
}

// requiredSeedColumns discovers what actually has to be supplied, from the live
// catalog of the table we just created — no hardcoded column list can rot here.
func requiredSeedColumns(t *testing.T, ctx context.Context, conn *pgx.Conn) (cols, vals []string) {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_2026_08'
		  AND is_nullable = 'NO' AND column_default IS NULL ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("discover NOT NULL columns: %v", err)
	}
	var required []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		required = append(required, c)
	}
	rows.Close()
	rows.Err()
	if len(required) == 0 {
		t.Fatal("request_logs has no NOT NULL-without-default columns — parsing or baseline drifted")
	}

	for _, c := range required {
		if c == "ts" {
			continue // per-partition literal
		}
		v, ok := seedValueFor(c)
		if !ok {
			t.Fatalf("NOT NULL column %q has no DEFAULT and this gate has no seed value for it — add one "+
				"to seedValueFor (the gate is meant to fail loudly on baseline drift)", c)
		}
		cols = append(cols, c)
		vals = append(vals, v)
	}
	return cols, vals
}

// batchProjectionSQL rebuilds 754's candidates SELECT verbatim — same column
// order, same predicates — so the plan-shape gate EXPLAINs the real query
// rather than a hand-written approximation of it.
func batchProjectionSQL() string {
	return `SELECT id, request_id, ts, tenant_id, gw_session_id, provider_model,
       prompt_tokens, completion_tokens, cost_usd, upstream_status_code, success, error_kind
FROM request_logs_2026_08
WHERE id > 0
ORDER BY id
LIMIT 1000`
}

// TestStress_ArchiveRequestLogs_ExecutesOnBaselineShape is S-01a. It runs the
// repository's own migration against the repository's own baseline schema and
// asserts the archive actually lands rows — the assertion that was missing for
// the entire life of migration 754.
func TestStress_ArchiveRequestLogs_ExecutesOnBaselineShape(t *testing.T) {
	withScratchDB(t, 4*time.Minute, func(ctx context.Context, conn *pgx.Conn) {
		installBaseline(t, ctx, conn, true)
		cols, vals := requiredSeedColumns(t, ctx, conn)

		// Expired partition (2026-08): fully inside the archive window.
		mustExec(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO request_logs_2026_08 (ts,%s) SELECT TIMESTAMPTZ '2026-08-01 00:00:00+08' + (g %% 2678400) * INTERVAL '1 second',%s FROM generate_series(1,%d) g",
			strings.Join(cols, ","), strings.Join(vals, ","), scratchRequestLogsRows))

		// In-window partition (2026-09): must be left untouched.
		mustExec(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO request_logs_2026_09 (ts,%s) SELECT TIMESTAMPTZ '2026-09-15 00:00:00+08' + g * INTERVAL '1 second',%s FROM generate_series(1,1000) g",
			strings.Join(cols, ","), strings.Join(vals, ",")))
		mustExec(t, ctx, conn, "ANALYZE request_logs")

		ddl := executableSQL(t, migration754Rel)
		for _, want := range []string{"CREATE OR REPLACE FUNCTION", "archive_request_logs_default"} {
			if !strings.Contains(ddl, want) {
				t.Fatalf("%s: stripped DDL no longer contains %q — migration was reshaped, re-anchor "+
					"this gate before trusting it", migration754Rel, want)
			}
		}
		mustExec(t, ctx, conn, ddl)

		// --- the assertion that has never existed until now ---
		rows, err := conn.Query(ctx, "SELECT archived_partition, rows_archived FROM archive_request_logs_default($1)", 7)
		if err != nil {
			t.Fatalf("archive_request_logs_default(7) failed — this is the exact call bg.archiveOldRequestLogs "+
				"makes once a day; SQLSTATE 42703 here means a projected source column does not exist on "+
				"request_logs: %v", err)
		}
		type arch struct {
			part string
			rows int64
		}
		var got []arch
		for rows.Next() {
			var a arch
			if err := rows.Scan(&a.part, &a.rows); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			got = append(got, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}

		if len(got) != 1 {
			t.Fatalf("expected exactly 1 archived partition (only 2026-08 is past the cutoff), got %d: %+v", len(got), got)
		}
		if got[0].part != "request_logs_archive_2026_08" {
			t.Errorf("archived partition = %q, want request_logs_archive_2026_08", got[0].part)
		}
		if got[0].rows != scratchRequestLogsRows {
			t.Errorf("rows_archived = %d, want %d (every seeded row is new, nothing pre-exists)",
				got[0].rows, scratchRequestLogsRows)
		}

		var archived int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM request_logs_archive_2026_08").Scan(&archived); err != nil {
			t.Fatalf("count archive table: %v", err)
		}
		if archived != scratchRequestLogsRows {
			t.Errorf("archive table holds %d rows, want %d — rows_archived and the table disagree",
				archived, scratchRequestLogsRows)
		}

		var inWindow int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM request_logs_2026_09").Scan(&inWindow); err != nil {
			t.Fatalf("count in-window partition: %v", err)
		}
		if inWindow != 1000 {
			t.Errorf("in-window partition changed: %d rows, want 1000", inWindow)
		}
	})
}

// TestStress_ArchiveRequestLogs_BatchCursorIsIndexed is S-01b.
//
// 754 batches with `WHERE id > :last_id ORDER BY id LIMIT 1000` and its header
// calls that a "主键游标". request_logs has no primary key and no index whose
// leading column is `id` — its only unique index is (request_id, ts). Without
// migration 756's index, that "cursor" is a full scan of the partition per
// batch: measured on a 2,125,857-row month partition, one batch of 1,000 rows
// read 531,262 buffers (~4.2 GB) and the whole archive exceeded the caller's
// 30-minute statement_timeout and rolled back.
//
// The plan shape is asserted rather than a wall-clock threshold, so this gate
// is meaningful at 5,000 rows too: with no index at all the planner has
// nothing to choose but a Seq Scan, at any table size.
func TestStress_ArchiveRequestLogs_BatchCursorIsIndexed(t *testing.T) {
	withScratchDB(t, 4*time.Minute, func(ctx context.Context, conn *pgx.Conn) {
		installBaseline(t, ctx, conn, false)
		cols, vals := requiredSeedColumns(t, ctx, conn)
		mustExec(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO request_logs_2026_08 (ts,%s) SELECT TIMESTAMPTZ '2026-08-01 00:00:00+08' + g * INTERVAL '1 second',%s FROM generate_series(1,%d) g",
			strings.Join(cols, ","), strings.Join(vals, ","), scratchRequestLogsRows))
		mustExec(t, ctx, conn, "ANALYZE request_logs")

		// Before applying 756, the plan must be a sequential scan. This is the
		// control: it proves the assertion below discriminates rather than
		// passing unconditionally.
		if plan := explainBatch(ctx, t, conn); !strings.Contains(plan, "Seq Scan") {
			t.Fatalf("control run unexpectedly already used an index (plan:\n%s)\n"+
				"something else now indexes id — update this gate's premise, it is no longer "+
				"measuring what it claims to measure", plan)
		}

		// 756 is the fix; the delivery copy under embeddata/startup/ is asserted
		// byte-identical by ../data.
		mustExec(t, ctx, conn, executableSQL(t, migration756Rel))
		mustExec(t, ctx, conn, "ANALYZE request_logs_2026_08")

		plan := explainBatch(ctx, t, conn)
		if strings.Contains(plan, "Seq Scan") {
			t.Fatalf("754's batch query still sequential-scans request_logs_2026_08 after migration 756 "+
				"ran:\n%s\nThe cursor column (id) has no index, so every 1000-row batch re-reads the whole "+
				"month partition — cost is O(rows²/1000).", plan)
		}
		if !strings.Contains(plan, "Index Scan") && !strings.Contains(plan, "Bitmap") {
			t.Fatalf("754's batch query used neither an index nor a bitmap scan:\n%s\n"+
				"expected an index scan on the cursor column", plan)
		}
		t.Logf("batch plan after 756:\n%s", plan)
	})
}

func explainBatch(ctx context.Context, t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	rows, err := conn.Query(ctx, "EXPLAIN (COSTS OFF) "+batchProjectionSQL())
	if err != nil {
		t.Fatalf("EXPLAIN batch query: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("EXPLAIN returned no plan lines")
	}
	return strings.Join(lines, "\n")
}

// TestStress_ArchiveRequestLogs_RerunIsIdempotent is S-01c. It covers the
// ON CONFLICT claim and records the re-run cost, because 754 has no
// "already archived" marker and every daily tick re-reads the full window.
func TestStress_ArchiveRequestLogs_RerunIsIdempotent(t *testing.T) {
	withScratchDB(t, 4*time.Minute, func(ctx context.Context, conn *pgx.Conn) {
		installBaseline(t, ctx, conn, false)
		mustExec(t, ctx, conn, fmt.Sprintf(
			"INSERT INTO request_logs_2026_08 (id,request_id,ts,tenant_id,success,gw_session_id) SELECT g,'req-'||g,TIMESTAMPTZ '2026-08-01 00:00:00+08'+(g||' seconds')::interval,'default',true,'s-'||g FROM generate_series(1,%d) g",
			scratchRequestLogsRows))
		mustExec(t, ctx, conn, "ANALYZE request_logs")
		mustExec(t, ctx, conn, executableSQL(t, migration754Rel))
		mustExec(t, ctx, conn, executableSQL(t, migration756Rel))

		cold, err := timedArchive(ctx, conn, 7)
		if err != nil {
			t.Fatalf("cold run: %v", err)
		}
		warm, err := timedArchive(ctx, conn, 7)
		if err != nil {
			t.Fatalf("warm run: %v", err)
		}
		if cold.rows != scratchRequestLogsRows {
			t.Errorf("cold rows_archived = %d, want %d", cold.rows, scratchRequestLogsRows)
		}
		if warm.rows != 0 {
			t.Errorf("warm rows_archived = %d, want 0 — ON CONFLICT DO NOTHING is not absorbing re-runs", warm.rows)
		}
		// The migration documents the re-scan cost explicitly; the gate only
		// asserts it is not catastrophically worse than the cold run (which
		// would mean conflict handling is quadratic), and reports both.
		if warm.dur > 3*cold.dur {
			t.Errorf("re-run took %v vs cold %v — conflict absorption looks super-linear", warm.dur, cold.dur)
		}
		t.Logf("S-01 cost @%d rows: cold=%v warm=%v", scratchRequestLogsRows, cold.dur, warm.dur)

		var archived int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM request_logs_archive_2026_08").Scan(&archived); err != nil {
			t.Fatalf("count: %v", err)
		}
		if archived != scratchRequestLogsRows {
			t.Errorf("after re-run archive holds %d rows, want %d — re-run duplicated rows", archived, scratchRequestLogsRows)
		}
	})
}

// TestStress_ArchiveRequestLogs_RetentionGuard is S-01d: the 7..365 floor must
// actually fire.
func TestStress_ArchiveRequestLogs_RetentionGuard(t *testing.T) {
	withScratchDB(t, 2*time.Minute, func(ctx context.Context, conn *pgx.Conn) {
		installBaseline(t, ctx, conn, false)
		mustExec(t, ctx, conn, executableSQL(t, migration754Rel))

		for _, bad := range []int{0, 6, -1, 366, 1000} {
			if _, err := conn.Exec(ctx, "SELECT * FROM archive_request_logs_default($1)", bad); err == nil {
				t.Errorf("archive_request_logs_default(%d) succeeded — the 7..365 retention guard did not fire", bad)
			}
		}
		for _, ok := range []int{7, 30, 365} {
			if _, err := conn.Exec(ctx, "SELECT * FROM archive_request_logs_default($1)", ok); err != nil {
				t.Errorf("archive_request_logs_default(%d) must be accepted, got %v", ok, err)
			}
		}
	})
}

type archiveRun struct {
	rows int64
	dur  time.Duration
}

func timedArchive(ctx context.Context, conn *pgx.Conn, days int) (archiveRun, error) {
	start := time.Now()
	var run archiveRun
	rows, err := conn.Query(ctx, "SELECT archived_partition, rows_archived FROM archive_request_logs_default($1)", days)
	if err != nil {
		return run, err
	}
	for rows.Next() {
		var part string
		var n int64
		if err := rows.Scan(&part, &n); err != nil {
			rows.Close()
			return run, err
		}
		run.rows += n
	}
	rows.Close()
	run.dur = time.Since(start)
	return run, rows.Err()
}

func redact(dsn string) string {
	if i := strings.Index(dsn, "://"); i > 0 {
		if j := strings.Index(dsn[i+3:], "@"); j > 0 {
			return dsn[:i+3] + "***" + dsn[i+3+j:]
		}
	}
	return dsn
}
