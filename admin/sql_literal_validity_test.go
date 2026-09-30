//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 会话存储解耦 v3 审计（2026-10-01）：`''::jsonb` 让三个端点的查询在**解析期**
// 就被 PostgreSQL 拒绝，不是慢，是必错。
//
// 真实故障（2026-10-01 真库端到端复现）：
//
//	ERROR: invalid input syntax for type json
//	LINE 9: COALESCE(rb.request_body, ''::jsonb) AS request_body,
//
// PostgreSQL 会在**解析**阶段求值常量 `''::jsonb`，`''` 不是合法 JSON 文档。
// 所以这不是「某些行取不到值」，而是**这条 SQL 永远执行不了**：
//
//   - `admin/session_export.go` —— 路由 `/api/admin/session-export` 已注册
//     （`cmd/gateway/main.go`），整条路径 100% 失败；
//   - `domains/sessionforensics/export.go` ×2 —— 同一段 SQL 的取证导出；
//   - `admin/quality_correlations.go` ×2 —— 只在 `images` / `code_block` 两个
//     分桶下炸，其余分桶正常，是最容易被误判成「偶发」的那种。
//
// 引入时间分别是 2026-07-08 与 2026-08-28，都是长期潜伏，不是新回归。
//
// **为什么既有测试没抓到**：`quality_correlations_test.go` 只测了纯函数
// `bucketIndex()`；`session_export_test.go` 只测鉴权与租户。SQL 字面量从未
// 真的发给过 PostgreSQL——mock 不解析 SQL，于是「语法/类型错误」这一整类
// 缺陷在单测里是隐形的。真正能抓它的门必须连真库，见
// `TestSessionExportMessagesSQL_ExecutesOnRealDatabase`。

// TestNoEmptyStringCastToJSONInSQLLiterals is the class-wide net.
//
// Scope matters here, and the first version of this guard got it wrong: it
// banned **every** `”::<type>` cast and immediately flagged
// `credential_models_dto.go`'s `offerListSQLCompat = `”::text“. That one is
// **valid** — an empty string is a perfectly legal text value, and casting it
// is how that compat path expresses "no source". A guard that false-positives
// on correct code gets disabled, and a disabled guard is worse than none.
// Only the JSON types are genuinely broken: JSON has no empty-document
// representation, so the cast is a parse-time error.
//
// Scoped to the packages that emit SQL, and to non-test files — a test may
// legitimately name the bad literal while asserting its absence.
func TestNoEmptyStringCastToJSONInSQLLiterals(t *testing.T) {
	// `''::json` / `''::jsonb` (and the \"\" spelling Go literals can carry).
	pat := regexp.MustCompile(`(?i)(''|"")\s*::\s*jsonb?\b`)

	roots := []string{"../admin", "../domains/sessionforensics", "../domains/sessionsummary", "../bg"}
	for _, root := range roots {
		files, err := filepath.Glob(filepath.Join(root, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", root, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			// Strip comments first: a comment that quotes the bad literal while
			// explaining the fix must not fail the build.
			code := stripGoComments(string(raw))
			for i, line := range strings.Split(code, "\n") {
				if !pat.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d casts an empty string literal to a SQL type:\n\t%s\n"+
					"PostgreSQL evaluates this constant at parse time, so the whole "+
					"statement fails — it is never a per-row problem. Use '{}'::jsonb "+
					"(or NULL) instead.", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestEmptyStringCastRejectsEmptyStringLiterals is a self-check on the guard
// itself: a regex that silently matches nothing is indistinguishable from a
// clean tree. If the pattern ever stops catching the literal it is meant to
// catch, the guard above becomes decorative.
func TestEmptyStringCastRejectsEmptyStringLiterals(t *testing.T) {
	pat := regexp.MustCompile(`(?i)(''|"")\s*::\s*jsonb?\b`)
	for _, bad := range []string{
		"COALESCE(rb.request_body, ''::jsonb) AS request_body",
		"COALESCE(rb.request_body, \"\"::jsonb) AS request_body",
		"x = ''::json",
		"''::JSONB",
	} {
		if !pat.MatchString(bad) {
			t.Errorf("guard pattern failed to match %q — the class-wide guard is blind", bad)
		}
	}
	// Must not fire on correct code. `''::text` is the one that taught this
	// guard its scope: empty string IS a legal text value.
	for _, good := range []string{
		"if s == \"\" {",
		"if tenantID != \"\" {",
		"COALESCE(rb.request_body, '{}'::jsonb)",
		"offerListSQLCompat     = `''::text`",
		"COALESCE(NULLIF(TRIM(mo.provider_modality), ''), 'text')",
	} {
		if pat.MatchString(good) {
			t.Errorf("guard pattern false-positives on %q", good)
		}
	}
}
