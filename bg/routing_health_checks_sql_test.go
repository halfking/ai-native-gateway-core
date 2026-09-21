package bg

// routing_health_checks_sql_test.go — canonical_id_null 检查/自动修复 SQL 形状回归。
//
// 背景（2026-09-11，R10 审计）：迁移 693 给 provider_models 加了
// canonical_cleared_at 管理员解绑标记，并把 UpsertCredentialModel 的
// ON CONFLICT 分支改成标记存在时保留存量 canonical_id。routing_health_checks
// 的 exact-match 自动修复走的是独立 UPDATE 语句，当时漏加标记守卫——管理员
// 解绑后（canonical_id=NULL, canonical_cleared_at=now()），只要存在同名的
// models_canonical 行，下一次 RunChecks 就会把它重新标成 critical 并静默
// 复活绑定，693 的"解绑持久化"在这一路径上失效。
//
// 本文件把"标记守卫必须出现在检查查询与自动修复两条 SQL 里"钉成形状断言，
// 无 PG 的 CI 也能挡住回归。

import (
	"os"
	"strings"
	"testing"
)

func TestCanonicalIDNullCheckQueryExcludesAdminUnbound(t *testing.T) {
	var query string
	found := false
	for _, chk := range AllHealthChecks() {
		if chk.CheckID == "canonical_id_null" {
			query = chk.Query
			found = true
			break
		}
	}
	if !found {
		t.Fatal("canonical_id_null check missing from AllHealthChecks()")
	}
	if !strings.Contains(query, "canonical_cleared_at IS NULL") {
		t.Fatalf("canonical_id_null check query lost the migration-693 admin-unbind guard:\n%s", query)
	}
}

func TestAutoFixCanonicalIDExcludesAdminUnbound(t *testing.T) {
	// autoFixCanonicalID 内联 SQL 无导出表面，这里以源码形状断言钉住
	// （与 credential_probe_v2_probenow_notify_test 同款手法）：守卫缺失即回归。
	src, err := os.ReadFile("routing_health_checks.go")
	if err != nil {
		t.Fatalf("read routing_health_checks.go: %v", err)
	}
	source := string(src)
	const fn = "func autoFixCanonicalID"
	start := strings.Index(source, fn)
	if start < 0 {
		t.Fatal("autoFixCanonicalID not found")
	}
	end := strings.Index(source[start:], "\nfunc ")
	body := source[start:]
	if end >= 0 {
		body = source[start : start+end]
	}
	if !strings.Contains(body, "canonical_cleared_at IS NULL") {
		t.Fatalf("autoFixCanonicalID lost the migration-693 admin-unbind guard:\n%s", body)
	}
}

// R37 SQL 审计钉桩：billing_mismatch 的 fix_sql 由 provider_models.raw_model_name
// （上游模型目录可影响）渲染而成，历史上裸拼 '%s' —— 落库后被 ExecuteFix 执行
// 即存储型 SQL 注入。现契约：①字面量必须经 pgQuoteLiteral 转义（单引号翻倍）；
// ②文本带 display-only 标注（执行通道已切 admin.cannedFix 参数化语句）。
func TestBillingMismatchFixSQLEscapesRawModelName(t *testing.T) {
	src, err := os.ReadFile("routing_health_checks.go")
	if err != nil {
		t.Fatalf("read routing_health_checks.go: %v", err)
	}
	source := string(src)
	if !strings.Contains(source, "pgQuoteLiteral(credPlan)") || !strings.Contains(source, "pgQuoteLiteral(parts[1])") {
		t.Fatal("billing_mismatch fix_sql must render raw_model_name/plan_type via pgQuoteLiteral (injection-hardened display text)")
	}
	if !strings.Contains(source, "display only, applied via parameterized fix channel") {
		t.Fatal("fix_sql must be marked display-only; execution goes through admin.cannedFix")
	}
	// 助手本身：单引号必须翻倍。
	if got := pgQuoteLiteral("x', billing_mode='hacked"); got != `'x'', billing_mode=''hacked'` {
		t.Fatalf("pgQuoteLiteral escaping wrong: %q", got)
	}
}
