package admin

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// R38 RLS 审计钉桩：HandleResetCredentialSuccessRate 的 DELETE 谓词读
// app.current_role / app.current_tenant，历史上跑在裸池连接（无事务无 GUC），
// current_setting 恒 NULL → 管理员"重置成功率"静默 no-op。修复后删除必须发生
// 在设了 super_admin GUC 的事务内。pgxmock 严格期望即证明：任何把 DELETE 挪回
// 裸连接（绕过 Begin/set_config）的实现都会让测试失败。
func TestResetCredentialSuccessRateRowsRunsInsideSuperAdminGUCTx(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT set_config\('app.bypass_rls', 'true', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`DELETE FROM request_logs_hot`).
		WithArgs(int64(17), "claude-sonnet-4-6").
		WillReturnResult(pgxmock.NewResult("DELETE", 3))
	mock.ExpectCommit()

	deleted, err := resetCredentialSuccessRateRows(context.Background(), mock, 17, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("GUC-tx contract broken: %v", err)
	}
}

// 谓词形状钉桩：DELETE 的守卫分支必须保留 super_admin/tenant 两个 GUC 分支
// （降权后它是真实的第二道防线，见 docs/design/rls-tenant-isolation-architecture.md §3.2）。
func TestResetCredentialSuccessRateRowsKeepsGUCGuardPredicate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`set_config\('app.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`set_config\('app.bypass_rls', 'true', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`DELETE FROM request_logs_hot.*current_setting\('app.current_role', true\) = 'super_admin'.*tenant_id = current_setting\('app.current_tenant', true\)`).
		WithArgs(int64(1), "m").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	if _, err := resetCredentialSuccessRateRows(context.Background(), mock, 1, "m"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("GUC guard predicate missing: %v", err)
	}
}
