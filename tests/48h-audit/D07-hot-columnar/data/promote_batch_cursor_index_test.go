// Package data - D07 数据测试（第二批）：promote_* 批游标必须有首列索引。
//
// 背景（R79 / D07 S-01）：754 归档函数的批游标 `WHERE id > :last ORDER BY id
// LIMIT 1000` 被头注称作「主键游标」，但 request_logs 根本没有 id 索引——
// 每批次退化成全分区扫描，成本 O(rows²/1000)。S-01 实测：2.1M 行月分区冷归档
// 30:00.028 被 statement_timeout 击杀并整笔回滚；补索引后 25.96s。
//
// 那一轮只修了 request_logs 一族。本文件是那道**跨族普查门**，并且刻意分成两件事：
//
//	TestData_PromoteBatchCursor_HasLeadingIndex  (Gate A)
//	    活函数（真被 promoteSpecs 调用）的批游标必须有首列索引。
//	TestData_PromoteFunction_ReferencedTableExists (Gate B)
//	    真库里的 promote_* 函数是否引用了已不存在的表——本次普查的副产物：
//	    11 个短名 promote_*_batch 函数在 Go 侧零调用方，其中 3 个引用的表
//	    根本不存在（credit_ledger_default / request_logs_bodies_default /
//	    tool_usage_stats_default）。它们今天不会炸，因为没人调；一旦有人
//	    「顺手接上」就会立刻 42P01。
//
// 为什么要分两件事：缺索引的活函数是**性能**缺陷（今天可能不痛），
// 死函数引用不存在的表是**可达性**缺陷（今天不痛，一旦接线就炸）。
// 把两者塞进一个列表会让人分不清该先修哪个。
//
// ## fail-closed 是本门的关键设计
//
// 函数体解析天然脆弱。脆弱的解析器配上「跳过没解析出来的函数」，就会退化成
// 真空门——这正是本会话反复吃亏的形态。所以这里的选择是：
// **任何一个活 promote_* 函数解析不出批游标，直接让本门失败**，而不是跳过它。
// 迁移改了函数体形态而本门没跟上，必须表现为红，而不是安静地少查几张表。
// （这条不是假设：写这个门时它当场从 7/26 覆盖率的静默通过，抓出抽取器只认得
// 单行函数体；随后又在 27/27 上抓出三处误报源表。）
//
// 跑测（无库自动 skip，有库即跑；只需能读 pg_proc/pg_class 的角色）：
//
//	D07_S01_PG_URL=postgres://user:pass@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 120s ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const partitionManagerRel = "bg/partition_manager.go"

// promoteCursor is one promote_* function's batch-cursor contract.
type promoteCursor struct {
	fn    string
	table string
	first string
	live  bool
	// firstAttnum is attnum of `first` on `table`, 0 when the column is absent.
	firstAttnum int
	leadingIdx  []string
}

// knownUnindexedCursors is the accepted-debt allowlist for Gate A.
//
// Every entry must carry a measurement, not just a table name: a bare table
// name in an allowlist is indistinguishable from a permanent waiver. These are
// P2 (latent scalability), not P1 — at the measured ingest rate the table never
// approaches a multi-batch backlog, so the O(rows²/batch_size) cursor is never
// actually paid. The cost is latent, not billed today.
var knownUnindexedCursors = map[string]string{
	"candidate_failure_logs_hot": "P2 latent (R79): 211 rows / 5.2 MB, ingest ~24 rows/h -> 8h window " +
		"~190 rows, far under the 5000-row batch size, so the quadratic cursor is never paid. " +
		"No index has `ts` as a leading column at all (EXPLAIN: Seq Scan + Sort). Re-rate if " +
		"ingest grows ~25x or the promote cycle stalls. Fix = CREATE INDEX ON candidate_failure_logs_hot (ts).",
	"auto_route_selections_hot": "P2 latent (R79): 862 rows / 872 kB, ingest ~359 rows/h -> 8h window " +
		"~2,900 rows, still one batch. The only `ts`-leading indexes are PARTIAL " +
		"(idx_ars_hot_session WHERE session_id IS NOT NULL, idx_ars_hot_unsettled WHERE settled_at " +
		"IS NULL) and the promote predicate `ts < statement_timestamp() - p_retention` implies " +
		"neither, so the batch still sorts (EXPLAIN-confirmed). Fix = CREATE INDEX ON auto_route_selections_hot (ts).",
}

// knownMissingTableFunctions is the accepted-debt allowlist for Gate B.
//
// These are P3 documentation debt, not a live fault: no Go caller, so nothing
// executes them, and the parent tables have no DEFAULT partition at all — there
// is nothing to drain. They are registered rather than dropped because dropping
// schema objects is a migration decision, and this audit round does not make
// that call for the owner.
//
// What they are: a loaded gun. `promote_cl_batch` reads and looks like a working
// drain function; wiring it into a spec would fail with 42P01 on the first
// cycle. Each entry therefore records the parent table and the reason.
var knownMissingTableFunctions = map[string]string{
	"promote_credit_ledger_default_batch": "P3 (R79): parent credit_ledger is PARTITIONED with 4 monthly " +
		"partitions and NO default partition, so there is nothing to drain; 0 Go callers. " +
		"Registering it would 42P01 immediately. Drop it or add the DEFAULT partition deliberately.",
	"promote_request_logs_bodies_default_batch": "P3 (R79): parent request_logs_bodies has 2 monthly " +
		"partitions and NO default partition; 0 Go callers. Same shape as the credit_ledger case.",
	"promote_tool_usage_stats_default_batch": "P3 (R79): parent tool_usage_stats has no default partition; " +
		"0 Go callers. Same shape as the credit_ledger case.",
}

// unqualify strips a table alias from a column reference: `h.ts` -> `ts`.
func unqualify(col string) string {
	if i := strings.LastIndex(col, "."); i >= 0 {
		return col[i+1:]
	}
	return col
}

var reCache sync.Map

func mustCompile(pattern string) *regexp.Regexp {
	if v, ok := reCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		panic("bad gate regex " + pattern + ": " + err.Error())
	}
	reCache.Store(pattern, re)
	return re
}

// substringFirstAt returns capture group 1 of the first match together with the
// byte offset at which that match starts, so callers can reason about what
// precedes exactly that occurrence.
func substringFirstAt(src, pattern string) (string, int) {
	loc := mustCompile(pattern).FindStringSubmatchIndex(src)
	if loc == nil || len(loc) < 4 {
		return "", -1
	}
	return src[loc[2]:loc[3]], loc[0]
}

// sourceTableForBatch resolves the batch statement's source table.
//
// ordIdx must be the offset of the batch's own ORDER BY — the one followed by
// LIMIT p_batch_size/batch_size, not merely the first ORDER BY in the body
// (promote_session_turns_hot derives its column list with `ORDER BY attnum`
// long before the batch).
//
// The table is the FIRST FROM of the plpgsql statement containing that ORDER BY.
// Both halves of that rule fixed real misreports this gate produced while being
// written: the body validates the parent with `FROM pg_partitioned_table` in an
// EARLIER statement, and the batch's own FROM comes right after its SELECT list
// while subquery FROMs (`NOT EXISTS (... FROM session_turns archived ...)`)
// come later, inside the WHERE.
func sourceTableForBatch(src string, ordIdx int) string {
	if ordIdx < 0 {
		return ""
	}
	prefix := src[:ordIdx]
	if semi := strings.LastIndex(prefix, ";"); semi >= 0 {
		prefix = prefix[semi+1:]
	}
	all := mustCompile(`FROM\s+(?:public\.)?([a-z_][a-z0-9_]*)`).FindAllStringSubmatchIndex(prefix, -1)
	if len(all) == 0 {
		return ""
	}
	return prefix[all[0][2]:all[0][3]]
}

// livePromoteFunctions reads bg/partition_manager.go and returns the fnName
// values registered in promoteSpecs() ONLY.
//
// Two scoping rules, each of which this gate got wrong first and had to be
// corrected against real output:
//
//   - The whole file contains other spec lists (ensureSpecs / archiveSpecs /
//     dropSpecs) whose fnName entries are ensure_*/archive_*/drop_*. Reading the
//     entire file made this gate demand 19 functions that the promote cycle
//     never calls. Scope to the promoteSpecs() function body via brace matching.
//   - Commented-out specs are stripped: promote_model_probe_runs_hot_to_partition
//     is commented out upstream, and counting it as live would make this gate
//     police a function nothing calls.
func livePromoteFunctions(t *testing.T) map[string]bool {
	t.Helper()
	src := readRepoFile(t, partitionManagerRel)

	start := strings.Index(src, "func promoteSpecs(")
	if start < 0 {
		t.Fatalf("%s: no `func promoteSpecs(` found — the extractor needs re-anchoring", partitionManagerRel)
	}
	depth, end := 0, -1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("%s: unterminated promoteSpecs() body", partitionManagerRel)
	}
	body := src[start:end]

	live := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if m := mustCompile(`fnName:\s*"([a-z_][a-z0-9_]*)"`).FindStringSubmatch(line); m != nil {
			live[m[1]] = true
		}
	}
	if len(live) < 5 {
		t.Fatalf("%s: only %d fnName entries parsed out of promoteSpecs — the extractor drifted, "+
			"and a gate that silently checks a subset is exactly the failure mode this file exists "+
			"to prevent", partitionManagerRel, len(live))
	}
	return live
}

// auditLiveDSN resolves a READ-ONLY catalog DSN. This package only reads
// pg_proc / pg_class / pg_index against whatever database the DSN names.
//
// It deliberately does NOT read D07_S01_ADMIN_URL: that variable is the
// maintenance DSN ../stress uses to CREATE and DROP scratch databases, and
// sharing it here would make a read-only catalog gate point at a connection
// that is expected to write.
func auditLiveDSN(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"D07_S01_PG_URL", "TEST_PG_URL", "LLM_GATEWAY_PG_URL", "TEST_DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	t.Skip("no read-only PostgreSQL DSN (D07_S01_PG_URL / TEST_PG_URL / LLM_GATEWAY_PG_URL / " +
		"TEST_DATABASE_URL). D07_S01_ADMIN_URL is intentionally not consulted here — it is the " +
		"maintenance DSN ../stress uses to build throwaway databases.")
	return ""
}

func connectAuditDB(t *testing.T) (*pgx.Conn, context.Context) {
	t.Helper()
	dsn := auditLiveDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn, ctx
}

// parsePromoteCursors extracts the batch cursor of every promote_* function.
//
// prosrc keeps the original newlines and indentation, so every inter-token gap
// is \s+ — matching on a literal space silently matched only the 7 functions
// whose bodies happen to be single-line.
func parsePromoteCursors(ctx context.Context, t *testing.T, conn *pgx.Conn, live map[string]bool) []promoteCursor {
	t.Helper()

	rows, err := conn.Query(ctx, `
		SELECT p.proname, p.prosrc
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.proname LIKE 'promote\_%'
		ORDER BY p.proname`)
	if err != nil {
		t.Fatalf("enumerate promote_* functions: %v", err)
	}
	defer rows.Close()

	type raw struct {
		name string
		src  string
	}
	var all []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.name, &r.src); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no promote_* functions found in public — either the DB has none or this gate is " +
			"pointed at the wrong database; refusing to pass vacuously")
	}

	var out []promoteCursor
	var unparsed []string
	for _, r := range all {
		ordRaw, ordIdx := substringFirstAt(r.src,
			`ORDER BY\s+((?:[a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_]*(\s*,\s*(?:[a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_]*)*)\s+LIMIT\s+(p_batch_size|batch_size)`)
		if ordRaw == "" || ordIdx < 0 {
			// Distinguish "not a pager" from "a pager this gate cannot read".
			// Only the latter is a defect.
			if hasBatchLimit(r.src) {
				unparsed = append(unparsed, r.name)
			}
			continue
		}
		tbl := sourceTableForBatch(r.src, ordIdx)
		if tbl == "" {
			unparsed = append(unparsed, r.name+"(table)")
			continue
		}
		out = append(out, promoteCursor{
			fn:    r.name,
			table: tbl,
			first: unqualify(strings.TrimSpace(strings.Split(ordRaw, ",")[0])),
			live:  live[r.name],
		})
	}

	var unparsedLive []string
	for _, u := range unparsed {
		if live[strings.TrimSuffix(u, "(table)")] {
			unparsedLive = append(unparsedLive, u)
		}
	}
	if len(unparsedLive) > 0 {
		t.Fatalf("fail-closed: %d LIVE promote_* function(s) mention a batch LIMIT but this gate "+
			"could not extract their batch cursor: %v.\n"+
			"A migration reshaped the function body and the extractor did not follow. Fix the "+
			"extractor (or teach this gate the new shape) — do NOT skip them, because a silently "+
			"skipped function is exactly the vacuous-gate failure this gate exists to prevent.",
			len(unparsedLive), unparsedLive)
	}
	return out
}

func hasBatchLimit(src string) bool {
	return mustCompile(`LIMIT\s+(p_batch_size|batch_size)`).MatchString(src)
}

// resolveCursor fills firstAttnum and leadingIdx.
func resolveCursor(ctx context.Context, conn *pgx.Conn, c *promoteCursor) {
	var attnum int
	err := conn.QueryRow(ctx, `SELECT a.attnum FROM pg_class t JOIN pg_attribute a ON a.attrelid = t.oid
		WHERE t.relname = $1 AND a.attname = $2 AND a.attnum > 0 AND NOT a.attisdropped`,
		c.table, c.first).Scan(&attnum)
	if err != nil {
		// Column or table absent: the cursor is unusable in a worse way. Leave
		// attnum at 0 so the caller reports it rather than crashing the sweep.
		return
	}
	c.firstAttnum = attnum

	// PARTIAL indexes are deliberately excluded. A partial index only serves a
	// query whose WHERE implies its predicate; the promote batch predicate is
	// `ts < cutoff`, which implies nothing about e.g. `WHERE settled_at IS NULL`.
	// Counting them made this gate call auto_route_selections_hot "indexed" while
	// EXPLAIN showed the planner ignoring idx_ars_hot_unsettled and still sorting.
	// When a partial index genuinely serves the batch query, that is an
	// EXPLAIN-backed claim and belongs in the allowlist justification.
	idx, err := conn.Query(ctx, `SELECT i.relname FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		WHERE t.relname = $1 AND ix.indkey[0] = $2 AND ix.indpred IS NULL ORDER BY i.relname`,
		c.table, attnum)
	if err != nil {
		return
	}
	defer idx.Close()
	for idx.Next() {
		var n string
		if err := idx.Scan(&n); err == nil {
			c.leadingIdx = append(c.leadingIdx, n)
		}
	}
}

// TestData_PromoteBatchCursor_HasLeadingIndex is Gate A.
func TestData_PromoteBatchCursor_HasLeadingIndex(t *testing.T) {
	conn, ctx := connectAuditDB(t)
	live := livePromoteFunctions(t)
	cursors := parsePromoteCursors(ctx, t, conn, live)

	var liveCount int
	for i := range cursors {
		if cursors[i].live {
			liveCount++
		}
	}
	t.Logf("parsed %d promote_* batch cursors from pg_proc; %d are registered in promoteSpecs",
		len(cursors), liveCount)

	var unindexed []promoteCursor
	for i := range cursors {
		c := &cursors[i]
		if !c.live {
			continue
		}
		resolveCursor(ctx, conn, c)
		if len(c.leadingIdx) == 0 {
			unindexed = append(unindexed, *c)
		}
	}

	var real []string
	for _, c := range unindexed {
		if _, ok := knownUnindexedCursors[c.table]; ok {
			t.Logf("accepted debt: %s (cursor=%s) — %s", c.table, c.first, knownUnindexedCursors[c.table])
			continue
		}
		real = append(real, c.fn+" (table="+c.table+", cursor="+c.first+")")
	}

	// Self-shrinking allowlist: a fixed table must not keep its waiver.
	stillUnindexed := map[string]bool{}
	for _, c := range unindexed {
		stillUnindexed[c.table] = true
	}
	for table := range knownUnindexedCursors {
		if !stillUnindexed[table] {
			t.Errorf("allowlist entry %q is stale: that table's batch cursor is now indexed (or the "+
				"table/function is gone). Remove the entry and record the fix — a self-shrinking "+
				"allowlist is the only kind that does not silently absorb new debt", table)
		}
	}

	if len(real) > 0 {
		t.Errorf("%d LIVE promote_* batch cursor(s) order by a column with no leading-column index on "+
			"their source table:\n  %s\n"+
			"Each batch then sorts the whole table, making the drain loop O(rows² / batch_size). "+
			"Measured precedent: request_logs archived this way took >30min and was rolled back at "+
			"2.1M rows. Add CREATE INDEX <table> (<cursor column>) or record a measured waiver.",
			len(real), strings.Join(real, "\n  "))
	}
}

// TestData_PromoteFunction_ReferencedTableExists is Gate B.
//
// R79 sweep by-product: 11 short-name promote_*_batch functions exist in the
// database with zero Go callers, and 3 of them reference tables that no longer
// exist. They cannot fail today because nothing calls them; the risk is that
// someone wires one up "because the function is already there" and gets 42P01
// on the first cycle.
func TestData_PromoteFunction_ReferencedTableExists(t *testing.T) {
	conn, ctx := connectAuditDB(t)
	live := livePromoteFunctions(t)

	rows, err := conn.Query(ctx, `
		SELECT p.proname, p.prosrc
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.proname LIKE 'promote\_%'
		ORDER BY p.proname`)
	if err != nil {
		t.Fatalf("enumerate promote_* functions: %v", err)
	}

	type fnBody struct {
		name string
		src  string
	}
	var bodies []fnBody
	for rows.Next() {
		var fb fnBody
		if err := rows.Scan(&fb.name, &fb.src); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		bodies = append(bodies, fb)
	}
	rows.Close()
	// The per-function existence checks below reuse this connection, so the
	// cursor MUST be drained and closed first — pgx reports "conn busy"
	// otherwise. Reading everything up front is also what makes the pass
	// single-round-trip.
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	type dead struct {
		fn      string
		missing []string
	}
	var orphans []dead
	liveSeen := map[string]bool{}

	for _, fb := range bodies {
		name, src := fb.name, fb.src
		if live[name] {
			liveSeen[name] = true
			continue
		}
		// Only tables inside the batch statement matter; a table named in a
		// comment or an error message does not.
		ordIdx := mustCompile(`ORDER BY\s+[a-z_][a-z0-9_]*(\s*,\s*[a-z_][a-z0-9_]*)*\s+LIMIT\s+(p_batch_size|batch_size)`).FindStringIndex(src)
		if ordIdx == nil {
			continue
		}
		tbl := sourceTableForBatch(src, ordIdx[0])
		if tbl == "" {
			continue
		}
		var exists bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "+
				"WHERE n.nspname='public' AND c.relname=$1)", tbl).Scan(&exists); err != nil {
			t.Fatalf("check table %q: %v", tbl, err)
		}
		if !exists {
			orphans = append(orphans, dead{fn: name, missing: []string{tbl}})
		}
	}

	// Every registered spec should exist in the DB; a registered-but-absent
	// function would break the promote cycle loudly, so check the converse too.
	for name := range live {
		if !liveSeen[name] {
			t.Errorf("promoteSpecs() registers %q but no such function exists in public — the "+
				"promote cycle will fail on this table every cycle", name)
		}
	}

	var realOrphans []dead
	for _, o := range orphans {
		if why, ok := knownMissingTableFunctions[o.fn]; ok {
			t.Logf("accepted debt: %s — %s", o.fn, why)
			continue
		}
		realOrphans = append(realOrphans, o)
	}
	for fn := range knownMissingTableFunctions {
		stillMissing := false
		for _, o := range orphans {
			if o.fn == fn {
				stillMissing = true
			}
		}
		if !stillMissing {
			t.Errorf("Gate B allowlist entry %q is stale: the function now resolves, or is gone. "+
				"Remove the entry and record what fixed it", fn)
		}
	}
	orphans = realOrphans

	if len(orphans) > 0 {
		var lines []string
		for _, o := range orphans {
			lines = append(lines, o.fn+" -> missing table "+strings.Join(o.missing, ","))
		}
		t.Errorf("%d unreferenced promote_* function(s) batch over a table that does not exist:\n  %s\n"+
			"They are unreachable today (no Go caller), so nothing fails — but they are a loaded gun: "+
			"wiring one up yields SQLSTATE 42P01 on the first cycle. Either drop them or restore the "+
			"table; do not leave a function that looks callable and cannot be.",
			len(orphans), strings.Join(lines, "\n  "))
	}
}
