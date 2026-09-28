// Package data - D07 数据测试：request_logs 归档链路的列契约。
//
// S-01 首轮（2026-09-29 真库 PostgreSQL 17.10 实测）实测结论：
// migration 754 的 archive_request_logs_default() 是动态 SQL（format('%I') +
// EXECUTE），列名在 CREATE FUNCTION 时不会被解析，所以 D-01「形状核对」和
// SF-01「静态守卫」全部通过；而真库首跑直接 SQLSTATE 42703：
//
//	ERROR:  column "session_id" does not exist
//	HINT:  Perhaps you meant to reference the column "request_logs_2026_08.gw_session_id".
//
// 即该函数自落地起从未成功执行过一次：bg.archiveOldRequestLogs 每天 03:xx
// 只会打一条 "request_logs archive failed" 然后空手而归。
//
// 本文件是不依赖数据库的守门人：从仓库自己的 baseline DDL
// （installer/cmd/llm-gw-installer/embeddata/01-schema.sql）解析出
// request_logs 的真实列集合，再解析 754 函数体里 candidates CTE 显式列出的
// 源列，逐一断言存在。任何后续改动若再次把列名写错（拼错、用了 session_* 族的
// 列名、改了 request_logs 的列名而没同步 754），本守卫立即变红。
//
// 跑测：
//
//	go test ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	baselineSchemaRel = "installer/cmd/llm-gw-installer/embeddata/01-schema.sql"
	// canonical lives in sql/migrations/startup/ — that is where
	// migration_754_test.go pins the contract; the installer ships a byte copy
	// under embeddata/startup/. Fixing one and forgetting the other is the
	// failure mode TestData_ArchiveRequestLogs_DeliveryCopyIsByteIdentical
	// exists to prevent, so the guard reads the canonical one.
	canonicalMigrationRel = "sql/migrations/startup/754_archive_request_logs_default.sql"
	deliveryMigrationRel  = "installer/cmd/llm-gw-installer/embeddata/startup/754_archive_request_logs_default.sql"
)

// repoRoot walks up from this source file to the module root.
func repoRoot(t testing.TB) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot locate repo root")
	}
	// <root>/tests/48h-audit/D07-hot-columnar/data/<this file>
	root := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root guess %q has no go.mod: %v", root, err)
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

var (
	// request_logs 的 CREATE TABLE 头，取到第一个语句结束分号为止。
	reReqLogsAnchor = regexp.MustCompile(`CREATE TABLE (?:IF NOT EXISTS )?(?:public\.)?request_logs\s*\(`)
	// DDL 里缩进 4 空格的列名（顶层列定义）。
	reDDLColumn = regexp.MustCompile(`(?m)^\s{4}([a-z_][a-z0-9_]*)\s+[a-z]`)
	// 归档函数的源投影：candidates AS ( SELECT <cols> FROM %I ...
	reCandidates = regexp.MustCompile(`(?s)candidates\s+AS\s*\(\s*SELECT\s+(.*?)\s+FROM\s+%I`)
)

// requestLogsColumnsFromBaseline parses the repository's own baseline DDL and
// returns the real column set of public.request_logs. This is the SSOT the
// migration is checked against — deliberately NOT the live database, so the
// guard still bites on a laptop with no PG running.
func requestLogsColumnsFromBaseline(t testing.TB) map[string]bool {
	t.Helper()
	src := readRepoFile(t, baselineSchemaRel)

	loc := reReqLogsAnchor.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("%s: no `CREATE TABLE ... request_logs (` found — baseline shape changed, "+
			"this guard must be updated to the new anchor before it can be trusted",
			baselineSchemaRel)
	}
	end := strings.Index(src[loc[1]:], ";")
	if end < 0 {
		t.Fatalf("%s: unterminated CREATE TABLE request_logs body", baselineSchemaRel)
	}
	body := src[loc[1] : loc[1]+end]

	cols := map[string]bool{}
	for _, m := range reDDLColumn.FindAllStringSubmatch(body, -1) {
		cols[m[1]] = true
	}
	if len(cols) < 50 {
		t.Fatalf("%s: parsed only %d request_logs columns from baseline (real table has 137) — "+
			"the column regex stopped matching and the guard below would vacuously pass",
			baselineSchemaRel, len(cols))
	}
	return cols
}

// archiveSourceColumns returns the column list the migration projects out of the
// source partition, i.e. the candidates CTE's explicit SELECT list.
func archiveSourceColumns(t testing.TB) []string {
	t.Helper()
	m := reCandidates.FindStringSubmatch(readRepoFile(t, canonicalMigrationRel))
	if m == nil {
		t.Fatalf("%s: no `candidates AS ( SELECT ... FROM %%I` block found — "+
			"migration 754 was reshaped, re-anchor this guard before trusting it",
			canonicalMigrationRel)
	}
	var cols []string
	for _, raw := range strings.Split(m[1], ",") {
		c := strings.TrimSpace(raw)
		if c == "" || strings.ContainsAny(c, "()") {
			t.Fatalf("%s: candidates SELECT list has an unexpected entry %q — "+
				"expected a bare column name per line", canonicalMigrationRel, c)
		}
		cols = append(cols, c)
	}
	if len(cols) < 5 {
		t.Fatalf("%s: candidates SELECT list yielded only %d columns — parse broke",
			canonicalMigrationRel, len(cols))
	}
	return cols
}

// TestData_ArchiveRequestLogs_SourceColumnsExist is the guard that would have
// caught the 42703 before the migration reached any database.
func TestData_ArchiveRequestLogs_SourceColumnsExist(t *testing.T) {
	cols := requestLogsColumnsFromBaseline(t)
	src := archiveSourceColumns(t)

	var missing []string
	for _, c := range src {
		if !cols[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("archive_request_logs_default projects %d column(s) that do not exist on "+
			"request_logs per %s: %v.\n"+
			"Real request_logs has no `session_id` (it is `gw_session_id`) and no `status_code` "+
			"(the integer HTTP status is `upstream_status_code`; `request_status` is text).\n"+
			"These only surface at runtime as SQLSTATE 42703 because the body is dynamic SQL, so "+
			"nothing in the installer or this test suite can fail until a partition is actually read.",
			len(missing), baselineSchemaRel, missing)
	}
}

// TestData_ArchiveRequestLogs_ProjectionCountMatchesInsertColumns pins the other
// half of the projection: the INSERT must select exactly as many expressions as
// the archive table declares columns, otherwise the row lands permuted or errors.
func TestData_ArchiveRequestLogs_ProjectionCountMatchesInsertColumns(t *testing.T) {
	sql := readRepoFile(t, canonicalMigrationRel)

	ins := regexp.MustCompile(`(?s)INSERT INTO %I\s*\(\s*(.*?)\s*\)\s*SELECT\s+(.*?)\s+FROM candidates`).FindStringSubmatch(sql)
	if ins == nil {
		t.Fatalf("%s: no `INSERT INTO %%I ( ... ) SELECT ... FROM candidates` block found",
			canonicalMigrationRel)
	}
	target := splitCols(ins[1])
	projected := splitCols(ins[2])

	if len(target) != len(projected) {
		t.Fatalf("archive INSERT lists %d columns but projects %d: %v vs %v",
			len(target), len(projected), target, projected)
	}
	// The projected list must be the source columns with request_logs' two
	// renamed columns mapped back — guard against a silent semantic swap.
	// Archive table column name -> the request_logs column that feeds it.
	want := map[string]string{
		"session_id":        "gw_session_id",
		"status_code":       "upstream_status_code",
		"model":             "provider_model",
		"tenant_id":         "tenant_id",
		"request_id":        "request_id",
		"ts":                "ts",
		"success":           "success",
		"error_kind":        "error_kind",
		"cost_usd":          "cost_usd",
		"prompt_tokens":     "prompt_tokens",
		"completion_tokens": "completion_tokens",
	}
	for i, c := range target {
		if mapped, ok := want[c]; ok && projected[i] != mapped {
			t.Errorf("archive column %q is fed from source column %q; expected %q "+
				"(the request_logs name for it)", c, projected[i], mapped)
		}
	}
}

func splitCols(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		if c := strings.TrimSpace(raw); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// TestData_ArchiveRequestLogs_DeliveryCopyIsByteIdentical guards the two copies
// of migration 754. `sql/migrations/startup/` is canonical (migration_754_test.go
// pins the contract there); the installer ships a byte copy under
// `embeddata/startup/`.
//
// This guard exists because the 42703 fix was originally applied to only the
// delivery copy — the canonical file, the file `bg`'s cadence test reads and the
// file the S-01 first-run gate executes, were all left still broken while
// everything looked green. A SQL fix that lands in one copy of a two-copy
// migration is a fix that has not landed.
func TestData_ArchiveRequestLogs_DeliveryCopyIsByteIdentical(t *testing.T) {
	canonical := readRepoFile(t, canonicalMigrationRel)
	delivery := readRepoFile(t, deliveryMigrationRel)

	if canonical != delivery {
		t.Fatalf("%s and %s have diverged (%d vs %d bytes).\n"+
			"The delivery copy is what the installer executes on a fresh host; the canonical copy is "+
			"what the contract tests read. A fix applied to only one of them means the other is "+
			"still broken — sync them and re-run.",
			canonicalMigrationRel, deliveryMigrationRel, len(canonical), len(delivery))
	}
	// Cheap direction check so a wholesale file swap cannot pass as "identical
	// but both wrong".
	for _, want := range []string{"gw_session_id", "upstream_status_code"} {
		if !strings.Contains(canonical, want) {
			t.Fatalf("%s no longer references %q — the 42703 fix was reverted in canonical; "+
				"re-apply it and sync the delivery copy", canonicalMigrationRel, want)
		}
	}
}
