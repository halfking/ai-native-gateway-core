package provider

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// File: sql_comment_syntax_test.go
//
// Why this file exists:
//   2026-08-09 audit (linked to AUDIT-2026-08-09-seq1479-symlink-mislabel-
//   and-audit-sql-regression.md): 154 production swap to
//   releases/1478-c4ab07de/ caused 79.2% of HTTP responses to be 500 for
//   ~7 minutes with
//     "failed to get candidates from provider" +
//     "ERROR: syntax error at or near \"audit\" (SQLSTATE 42601)".
//
//   Root cause: a 10-line Go-style `//` block comment was inserted inside
//   the SQL string literal of `loadCandidatesByModalityDB`
//   (provider/client.go:1094-1103) by audit-gate commit 58c579d7.
//   PostgreSQL does NOT recognize `//` as a comment marker (only `--`
//   and `/* */`), so the parser tripped on the first non-quoted token
//   ("audit") and returned SQLSTATE 42601.
//
//   This test scans every multi-line backtick raw string in the provider
//   package's SQL-bearing files and fails fast if any line begins (after
//   whitespace) with `//`. The check is pure lexical — no DB connection
//   required.
//
// ★★ 2026-10-29 两处加固：
//
//	① **扫描器换了实现**（tests/testutil.ScanMultiLineRawStrings）——旧的
//	   扫描器在「一行两个反引号」（= raw string 被 `+` 拼接拆开）时会在第一个
//	   反引号处把块关掉 ⇒ **拼接之后的那一半 SQL 整段不再被扫** ⇒那一段里的
//	   `//` 注释对门完全隐形。★ 而这正是本仓**现在**的形态：模态接受臂被收成
//	   唯一片段后是用 `+` 拼进主 SQL 的（/*MODALITY_VERDICT_GATE*/ 同款）。
//	   ⇒ 实测证据：TestSQLCommentScanner_SeesPastConcatenation。
//	② **扫描的文件清单扩了**：判据的唯一片段住在 modality_accept.go ⇒ 它
//	   也必须在扫描范围内（否则「判据搬家了，门还守着旧家」）。
//
// ★ 扫描器本身是**共享实现**（tests/testutil）—— domains/streaming 侧那道
//   同款门用同一份实现（同一把尺子只能有一份实现）。

// The files that carried SQL in this package at the time of the 2026-10-29
// change were client.go, modality_accept.go and modality_mismatch.go.
//
// ★ The gate no longer keeps a list: it scans **every non-test .go file in the
// package** (testutil.ScanPackageGoStyleSQLComments) ⇒ a new SQL file is
// covered automatically. A hand-maintained list rots — the judgement moved
// from client.go to modality_accept.go on 2026-10-29, and a list that was not
// updated would have left the gate watching the old home and blind to the new
// one.
//
// TestNoGoStyleCommentsInsideSQLRawStrings pins the regression by lexically
// inspecting every multi-line raw-string literal in the provider package's
// SQL-bearing files.
//
// The scan is intentionally conservative: it only fails on lines that begin
// with `//` (a single-line Go-style comment marker) inside a raw string. PG
// will reject such lines with SQLSTATE 42601 "syntax error" pointing at the
// first non-quoted token of the offending line.
func TestNoGoStyleCommentsInsideSQLRawStrings(t *testing.T) {
	dir := providerPackageDir(t)
	findings, err := testutil.ScanPackageGoStyleSQLComments(dir)
	if err != nil {
		t.Fatalf("扫描 provider 包失败：%v", err)
	}
	for _, f := range findings {
		t.Errorf("%s:%d: Go-style // comment inside a multi-line raw-string SQL block — "+
			"PostgreSQL only accepts -- and /* */ as comment markers; a leading // turns "+
			"this block into a SQLSTATE 42601 syntax error at parse time. See "+
			"AUDIT-2026-08-09-seq1479-symlink-mislabel-and-audit-sql-regression.md. Offending line: %q",
			f.File, f.Line, f.Text)
	}
}

// providerPackageDir returns the directory of this package (via runtime.Caller),
// so the test stays hermetic regardless of where `go test` is invoked from.
func providerPackageDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return thisFile[:strings.LastIndex(thisFile, "/")]
}

// TestSQLCommentScanner_SeesPastConcatenation 是**扫描器自己的自证**：
// 证明它在「raw string 被 `+` 拼接拆开」之后**仍能看见后半段**。
//
// ★ 为什么必须做（2026-10-29 实测）：旧扫描器按「行尾单个反引号 = 块开头」
//
//	/「块内第一个反引号 = 块结尾」配对 ⇒ 拼接行的第一个反引号就把块关掉 ⇒
//	**拼接之后的那一半 SQL 整段不再被扫** ⇒ 那里的 `//` 注释对门隐形。
//	⇒ 实测（夹具）：
//	  A 未拼接 ⇒ 命中；
//	  B 拼接   ⇒ 旧实现**不命中**（盲区），新实现命中。
//
// ⇒ 这条断言锁住新实现的行为，防止它退化回旧形态。
func TestSQLCommentScanner_SeesPastConcatenation(t *testing.T) {
	// 夹具 A：一个普通（未拼接）的多行 raw string，里面有一行 `//`。
	const unsplit = "package p\n\nfunc f() string {\n\treturn `\n   SELECT 1\n   // go style comment\n   FROM t\n`\n}\n"
	// 夹具 B：同一个 raw string，但**被一行上的 `+` 拼接拆开**——拆开的那一
	// 行有两个反引号，`//` 注释落在**后半段**。
	const split = "package p\n\nfunc f() string {\n\treturn `\n   SELECT 1\n   FROM t WHERE (` + arm + `\n   // go style comment\n   AND x = 1\n`\n}\n"

	if got := testutil.GoStyleSQLCommentFindings(unsplit); len(got) != 1 {
		t.Errorf("夹具 A（未拼接）：命中 %d 条，want 1 —— 扫描器连最基本的形态都抓不到", len(got))
	} else if got[0] != "6" {
		t.Errorf("夹具 A（未拼接）：命中行 = %s，want 6", got[0])
	}
	got := testutil.GoStyleSQLCommentFindings(split)
	if len(got) != 1 {
		t.Errorf("夹具 B（拼接）：命中 %d 条，want 1 —— ★扫描器在拼接点失明了 ⇒ 拼接之后的那一半 SQL 里的 `//` 注释对门完全隐形（这是本仓现在的形态：模态接受臂是用 `+` 拼进主 SQL 的）", len(got))
	}
	// ★ 命中位置也钉住：必须是**后半段**那一行（夹具 B 里第 7 行）。
	if len(got) == 1 && got[0] != "7" {
		t.Errorf("夹具 B（拼接）：命中行 = %s，want 7（后半段那一行）", got[0])
	}
}

// readProviderFile returns the file contents of a sibling .go file in this
// package directory. We use runtime.Caller to locate our own source file at
// test time, which keeps the test hermetic regardless of where `go test`
// is invoked from.
func readProviderFile(t *testing.T, fname string) string {
	t.Helper()
	// Locate this test file's directory via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := thisFile[:strings.LastIndex(thisFile, "/")]
	// thisFile has the form /…/provider/sql_comment_syntax_test.go
	data, err := os.ReadFile(dir + "/" + fname)
	if err != nil {
		t.Fatalf("read %s: %v", fname, err)
	}
	return string(data)
}

// TestBrokenPairExcludeSQL_FollowsProbeMode pins the R36 A-3 fix: the
// defect-(2) broken-pair filter must consult the ACTIVE probe system's
// verdict table (v_node_probe_state_compat under the new stack,
// model_probe_state under the legacy stack).
func TestBrokenPairExcludeSQL_FollowsProbeMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	got := brokenPairExcludeSQL("mps", "c.id", "mo.raw_model_name")
	for _, want := range []string{
		"NOT EXISTS (",
		"FROM v_node_probe_state_compat mps",
		"mps.credential_id = c.id",
		"mps.raw_model_name = mo.raw_model_name",
		"mps.state = 'broken_confirmed'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brokenPairExcludeSQL missing %q under new mode:\n%s", want, got)
		}
	}

	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	got = brokenPairExcludeSQL("mps", "c.id", "mo.raw_model_name")
	if !strings.Contains(got, "FROM model_probe_state mps") {
		t.Errorf("brokenPairExcludeSQL must keep model_probe_state under legacy mode:\n%s", got)
	}
}
