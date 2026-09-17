package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// R37 SQL 审计钉桩：ExecuteFix 历史上直接 Exec 库中 fix_sql 自由文本，而
// billing_mismatch 的 fix_sql 由 provider_models.raw_model_name（上游模型目录
// 可影响）裸拼而成 —— 二阶存储型 SQL 注入。修复后执行端只允许 cannedFix 的
// 参数化语句，fix_sql 降级为展示文本。pgxmock 严格期望即证明：任何对注入
// payload 的执行都会让测试失败。
func TestExecuteFix_BillingMismatchRunsOnlyCannedStatement(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// 库中 fix_sql 携带注入 payload（真实路径：raw_model_name 落库时携带引号逃逸序列）。
	mock.ExpectQuery(`SELECT entity_type, entity_id FROM routing_health_checks WHERE id = \$1 AND status = 'open'`).
		WithArgs(int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"entity_type", "entity_id"}).
			AddRow("billing_mismatch", int64(42)))
	// 唯一允许执行的语句 = 参数化罐头（$1 = entity_id），plan_type 取自 credentials 联表。
	mock.ExpectExec(`UPDATE credential_model_bindings cmb SET billing_mode = c\.plan_type.*FROM credentials c WHERE c\.id = cmb\.credential_id AND cmb\.id = \$1`).
		WithArgs(int64(42)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE routing_health_checks SET status = 'manual_fixed'`).
		WithArgs(int64(7)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	status, _ := runHealthCheckFix(context.Background(), mock, 7)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("canned-statement-only contract broken: %v", err)
	}
}

func TestExecuteFix_UnsupportedEntityTypeRejectedWithoutExec(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// 注释型/人工型发现（probe_missing 等）历史上 Exec 必 500；现在必须 400
	// 且不执行任何语句。
	mock.ExpectQuery(`SELECT entity_type, entity_id FROM routing_health_checks WHERE id = \$1 AND status = 'open'`).
		WithArgs(int64(9)).
		WillReturnRows(pgxmock.NewRows([]string{"entity_type", "entity_id"}).
			AddRow("probe_missing", int64(3)))

	status, _ := runHealthCheckFix(context.Background(), mock, 9)
	if status != 400 {
		t.Fatalf("status = %d, want 400 (no one-click fix)", status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no-statement contract broken: %v", err)
	}
}

func TestExecuteFix_HTTPWrapperPassthrough(t *testing.T) {
	// HTTP 包装层：非法 JSON → 400；核心契约由上面两个用例覆盖。
	h := &HealthCheckHandler{db: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/fix", strings.NewReader(`not-json`))
	h.ExecuteFix(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 for invalid json", rec.Code)
	}
}

func TestCannedFix_CanonicalIDNullKeepsAdminUnbindGuard(t *testing.T) {
	// 与 bg.autoFixCanonicalID 同款 693 守卫：管理员解绑标记存在时一键修复
	// 不得复活绑定。
	stmt, ok := cannedFix("canonical_id_null")
	if !ok {
		t.Fatal("canonical_id_null must have a canned fix")
	}
	if !strings.Contains(stmt, "canonical_cleared_at IS NULL") {
		t.Fatalf("canonical_id_null canned fix lost the migration-693 guard:\n%s", stmt)
	}
}
