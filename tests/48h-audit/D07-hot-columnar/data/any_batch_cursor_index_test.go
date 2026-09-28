// Package data - D07 数据测试（第三批）：**全库**批游标普查（不限 promote_*）。
//
// 前两批的边界：
//   - 第二批（promote_batch_cursor_index_test.go）只普查了 `promote_*` 函数族。
//   - 本文件把范围扩到 public schema 里**任何**带「ORDER BY <cols> LIMIT <n>」
//     形态的函数，并把「活 / 死」判定从「是否在 promoteSpecs() 里」升级为
//     「函数名是否在仓内 .go 源码中被作为字符串引用」。
//
// 本轮真库全量扫描的结论（4 个命中，其中 1 个已在 R79 修复）：
//
//	archive_request_logs_default            id 游标       —— R79 已修（754 补列名 + 756 补 id 索引）
//	archive_request_wal                     created_at 游标 —— **死函数**，见下
//	ensure_request_logs_partition           ctid 物理序    —— 活函数，但 ctid 无法建索引
//	repair_request_logs_detached_partitions ctid 物理序    —— 仅测试引用
//
// ## 一条新登记的发现：archive_request_wal 至今仍存在于真库
//
// 迁移 331（2026-07-04）明写「Drop archive functions: archive_request_logs(),
// archive_request_wal()」，`bg/partition_manager.go:1269` 也照此注释。但：
//
//   - 331 **不在** `installer/cmd/llm-gw-installer/embeddata/startup/`（R71 已核实）；
//   - 本机真库 schema_migrations 只到 V359，331 从未在此库应用过；
//   - `archive_request_wal` 因此**仍然存在于真库**，只是全仓 .go 侧零调用方。
//
// 这把 R79 登记的 P3（父表 `request_logs_archive` 未被 331 删除、0 分区）
// 从「表」扩到了「函数」：**331 整条通道缺席，它声明要删的对象一个都没删。**
//
// ## ctid 游标为什么不是缺陷
//
// `ensure_request_logs_partition` 的排空循环用 `ORDER BY ctid LIMIT 50000`。
// ctid 是系统列，**无法建索引**——所以「缺首列索引」这条规则对它不适用，
// 把它判红是规则用错了对象。物理序 + `EXIT WHEN drained = 0` 的收敛保证
// 本来就是合法策略。本门据此把 ctid 归入「不可索引」类，只登记不判红，
// 并在输出里显式说明理由——而不是悄悄把它算进「已覆盖」。
//
// 跑测（无库自动 skip；只需能读 pg_proc/pg_class 的角色）：
//
//	D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 180s ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// systemColumns can never carry a btree, so "is the cursor column indexed?" is
// the wrong question for them. See the ctid discussion in the file header.
var systemColumns = map[string]bool{
	"ctid": true, "xmin": true, "xmax": true, "cmin": true, "cmax": true,
	"tableoid": true,
}

type dbFunc struct {
	name   string
	cursor string // first ORDER BY column, alias-stripped
	table  string
	// dynamic is set when the batch reads FROM %I (a format() placeholder fed
	// by a plpgsql variable), so the concrete table cannot be resolved from the
	// function body at all. archive_request_logs_default is the canonical case:
	// its source is whatever pg_inherits hands it.
	dynamic  bool
	system   bool // cursor is a system column: un-indexable by construction
	refered  bool // name appears as a Go string literal somewhere in the repo
	leading  []string
	attnum   int
	found    bool // the cursor column exists on the table
	tableHit bool // the table exists
}

// batchCursorPattern matches a real keyset/physical pager inside a function
// body: an ORDER BY over concrete column references immediately followed by a
// LIMIT.
//
//   - the `[a-z_]` class keeps `ORDER BY 1 LIMIT 12` out — those loops
//     enumerate the last 12 months, they are not cursors.
//   - LIMIT accepts a literal, a %L placeholder (dynamic SQL inside format()),
//     or a named batch parameter: the three spellings present in the corpus.
var batchCursorPattern = regexp.MustCompile(
	`ORDER BY\s+((?:[a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_]*(\s*,\s*(?:[a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_]*)*)\s+LIMIT\s+(\d+|%L|p_batch_size|batch_size)`)

// goStringLiteral matches a double-quoted identifier in Go source.
var goStringLiteral = regexp.MustCompile(`"([a-z_][a-z0-9_]{3,})"`)

// repoFuncRefs scans the repository's .go files for string literals, which is how
// SQL function names reach Go (pgx query strings, migration test fixtures).
// Deliberately coarse: a name mentioned anywhere counts as referenced, which
// errs toward "treat as live" and therefore toward a stricter gate.
func repoFuncRefs(t *testing.T) map[string]bool {
	t.Helper()
	root := repoRoot(t)
	refs := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable path: skip rather than abort the sweep
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".build-local", "dist", "web", "installer":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range goStringLiteral.FindAllStringSubmatch(string(b), -1) {
			refs[m[1]] = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo for function references: %v", walkErr)
	}
	if len(refs) < 100 {
		t.Fatalf("repo scan found only %d string literals — the walker drifted, and a gate that "+
			"silently classifies everything as dead is worse than no gate", len(refs))
	}
	return refs
}

// unqualify strips a table alias from a column reference: `h.ts` -> `ts`.
func unqualify(col string) string {
	if i := strings.LastIndex(col, "."); i >= 0 {
		return col[i+1:]
	}
	return col
}

// sourceTableForBatch resolves the batch statement's source table.
//
// ordIdx must be the offset of the batch's own ORDER BY — the one followed by a
// LIMIT, not merely the first ORDER BY in the body (promote_session_turns_hot
// derives its column list with `ORDER BY attnum` long before the batch).
//
// The table is the FIRST FROM of the plpgsql statement containing that ORDER BY.
// Both halves of that rule fixed real misreports while this suite was written:
// the body validates the parent with `FROM pg_partitioned_table` in an EARLIER
// statement, and the batch's own FROM comes right after its SELECT list while
// subquery FROMs come later, inside the WHERE.
func sourceTableForBatch(src string, ordIdx int) string {
	if ordIdx < 0 {
		return ""
	}
	prefix := src[:ordIdx]
	if semi := strings.LastIndex(prefix, ";"); semi >= 0 {
		prefix = prefix[semi+1:]
	}
	// A format() placeholder (%I / %s) is not a resolvable table name. Return
	// the placeholder itself so the caller can classify it as dynamic instead
	// of falling through to "no table found" — which under fail-closed would be
	// right for the wrong reason.
	if loc := regexp.MustCompile(`FROM\s+(%[IsL])`).FindStringSubmatchIndex(prefix); loc != nil {
		return prefix[loc[2]:loc[3]]
	}
	all := regexp.MustCompile(`FROM\s+(?:public\.)?([a-z_][a-z0-9_]*)`).FindAllStringSubmatchIndex(prefix, -1)
	if len(all) == 0 {
		return ""
	}
	return prefix[all[0][2]:all[0][3]]
}

var dynamicTablePlaceholder = regexp.MustCompile(`^%[IsL]$`)

func collectDBFuncs(ctx context.Context, t *testing.T, conn *pgx.Conn, refs map[string]bool) []dbFunc {
	t.Helper()
	// Scope: the promote_* family is NOT re-checked here. It already has a
	// dedicated gate (promote_batch_cursor_index_test.go) with two things this
	// file deliberately does not have — spec-accurate liveness (parsed from
	// promoteSpecs(), not from a repo-wide string grep) and a per-table debt
	// ledger. Re-deriving the same findings here with cruder inputs produced the
	// same six items framed differently, i.e. noise that would have left this
	// gate permanently red for debt the other gate already manages.
	rows, err := conn.Query(ctx, `
		SELECT p.proname, p.prosrc
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.prokind = 'f'
		  AND p.proname NOT LIKE 'promote\_%'
		ORDER BY p.proname`)
	if err != nil {
		t.Fatalf("enumerate public functions: %v", err)
	}
	defer rows.Close()

	type body struct{ name, src string }
	var all []body
	for rows.Next() {
		var b body
		if err := rows.Scan(&b.name, &b.src); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no public functions found — refusing to pass vacuously")
	}

	var out []dbFunc
	var unparsed []string
	for _, b := range all {
		loc := batchCursorPattern.FindStringSubmatchIndex(b.src)
		if loc == nil {
			continue
		}
		ord := b.src[loc[2]:loc[3]]
		tbl := sourceTableForBatch(b.src, loc[0])
		if tbl == "" {
			unparsed = append(unparsed, b.name+"(table)")
			continue
		}
		cur := unqualify(strings.TrimSpace(strings.Split(ord, ",")[0]))
		out = append(out, dbFunc{
			name:    b.name,
			cursor:  cur,
			table:   tbl,
			dynamic: dynamicTablePlaceholder.MatchString(tbl),
			system:  systemColumns[cur],
			refered: refs[b.name],
		})
	}

	// Only a REFERENCED function turning unparseable is a defect. An
	// unreferenced one nobody can call anyway; failing on it would be the
	// gate policing a hypothetical.
	var badRefs []string
	for _, u := range unparsed {
		name := strings.TrimSuffix(u, "(table)")
		if refs[name] {
			badRefs = append(badRefs, u)
		}
	}
	if len(badRefs) > 0 {
		t.Fatalf("fail-closed: %d REFERENCED function(s) have a batch pager this gate could not "+
			"resolve a source table for: %v.\n"+
			"A migration reshaped the body and the extractor did not follow. Fix the extractor — "+
			"do NOT skip them, because a silently skipped function is exactly the vacuous-gate "+
			"failure this suite exists to prevent.", len(badRefs), badRefs)
	}
	return out
}

func resolveCursor(ctx context.Context, conn *pgx.Conn, f *dbFunc) {
	var attnum int
	if err := conn.QueryRow(ctx, `SELECT a.attnum FROM pg_class t JOIN pg_attribute a ON a.attrelid = t.oid
		WHERE t.relname = $1 AND a.attname = $2 AND a.attnum > 0 AND NOT a.attisdropped`,
		f.table, f.cursor).Scan(&attnum); err == nil {
		f.attnum = attnum
		f.found = true
	} else {
		return
	}
	// PARTIAL indexes are excluded on purpose: one only serves a query whose
	// WHERE implies its predicate, and the batch predicates here (ts < cutoff)
	// imply nothing about e.g. `WHERE settled_at IS NULL`. Counting them made
	// an earlier version of this gate report a table as covered while EXPLAIN
	// showed the planner ignoring the index and still sorting.
	idx, err := conn.Query(ctx, `SELECT i.relname FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		WHERE t.relname = $1 AND ix.indkey[0] = $2 AND ix.indpred IS NULL
		ORDER BY i.relname`, f.table, attnum)
	if err != nil {
		return
	}
	defer idx.Close()
	for idx.Next() {
		var n string
		if err := idx.Scan(&n); err == nil {
			f.leading = append(f.leading, n)
		}
	}
}

// TestData_AnyBatchCursor_IndexedOrUnindexable is the全库 sweep.
//
// The invariant is NOT "every cursor has an index" — that rule is wrong for
// system columns, which cannot carry one. It is:
//
//	every REFERENCED batch pager either (a) has a leading-column index, or
//	(b) orders by a system column, where physical-order paging plus an explicit
//	drain-exit condition is the correct strategy.
//
// Anything else is a defect; unreferenced pagers are logged, not failed.
func TestData_AnyBatchCursor_IndexedOrUnindexable(t *testing.T) {
	conn, ctx := connectAuditDB(t)
	refs := repoFuncRefs(t)
	funcs := collectDBFuncs(ctx, t, conn, refs)

	if len(funcs) < 4 {
		t.Fatalf("only %d non-promote batch pagers found in public — the corpus shrank or the "+
			"pattern broke; a sweep that quietly finds almost nothing is not evidence of health "+
			"(this corpus currently holds archive_request_logs_default, archive_request_wal, "+
			"ensure_request_logs_partition and repair_request_logs_detached_partitions)", len(funcs))
	}
	t.Logf("found %d non-promote public functions with a batch cursor (promote_* is covered by "+
		"promote_batch_cursor_index_test.go)", len(funcs))

	var defects []string
	for i := range funcs {
		f := &funcs[i]
		if f.dynamic {
			// The concrete table is chosen at run time (pg_inherits / a plpgsql
			// variable), so an index on it cannot be checked from the body. This
			// is neither pass nor fail on its own: it is a claim that needs a
			// recorded justification, or a new table could appear under this
			// shape with nothing indexing its cursor column.
			if why, ok := dynamicSourceTables[f.name]; ok {
				t.Logf("dynamic source table (justification on file): %s cursor=%s — %s",
					f.name, f.cursor, why)
				continue
			}
			defects = append(defects, f.name+
				" reads FROM "+f.table+" (a format() placeholder, so its concrete source table is "+
				"resolved at run time) and has no recorded justification that the cursor column is "+
				"indexed on every table it can land on. Add one, or restructure so the table is static")
			continue
		}
		if f.system {
			t.Logf("system-column cursor (no index possible, paging strategy is correct): "+
				"%s -> %s ORDER BY %s", f.name, f.table, f.cursor)
			continue
		}
		resolveCursor(ctx, conn, f)
		if len(f.leading) > 0 {
			continue
		}
		if !f.refered {
			t.Logf("unreferenced pager: %s -> %s ORDER BY %s (%s)",
				f.name, f.table, f.cursor, missingReason(f))
			continue
		}
		defects = append(defects, f.name+" -> "+f.table+" ORDER BY "+f.cursor+" ("+missingReason(f)+")")
	}

	if len(defects) > 0 {
		t.Errorf("%d REFERENCED batch pager(s) neither have a leading-column index nor order by a "+
			"system column:\n  %s\n"+
			"Each batch re-sorts the whole source, making the loop O(rows² / batch). Measured "+
			"precedent: request_logs archived this way exceeded a 30-minute statement_timeout at "+
			"2.1M rows. Add CREATE INDEX <table> (<cursor column>) or record a measured waiver.",
			len(defects), strings.Join(defects, "\n  "))
	}
}

// dynamicSourceTables records why a format()-templated source table is safe
// with respect to its cursor column. Each entry must state what the table turns
// out to be and why the cursor is indexed there — a bare function name is
// indistinguishable from an unexamined waiver.
var dynamicSourceTables = map[string]string{
	"archive_request_logs_default": "src_part is whatever pg_inherits hands the function: a " +
		"request_logs monthly partition. R79 (this round's first half) added migration 756, " +
		"CREATE INDEX idx_request_logs_id ON request_logs(id) on the PARTITIONED PARENT, which " +
		"PostgreSQL cascades to every existing and future partition. Before 756 the cursor was " +
		"unindexed and 2.1M rows took >30min and rolled back. 756 is not applied to this local DB " +
		"yet, which is exactly why a static check here cannot verify it.",
	"archive_request_wal": "DEAD function, and 331 (2026-07-04) intended to drop exactly this. 331 is " +
		"not in the installer startup channel and was never applied here (schema_migrations stops at " +
		"V359), so the function outlived its own deprecation. Its source is a request_wal monthly " +
		"partition and its cursor is created_at; the request_wal partitioned parent has NO " +
		"created_at-leading index (only gw_session_id / request_id / status / tenant_id composites), " +
		"so reviving this would reproduce the S-01 N² shape. No Go caller, so nothing is paid today. " +
		"Correct fix is to apply 331's intent (drop it), not to add an index nobody queries.",
}

func missingReason(f *dbFunc) string {
	switch {
	case !f.tableHit && !f.found:
		return "table/column not resolvable"
	case !f.found:
		return "column " + f.cursor + " not found on " + f.table
	case len(f.leading) == 0:
		return "no non-partial index with " + f.cursor + " as leading column"
	}
	return "unknown"
}
