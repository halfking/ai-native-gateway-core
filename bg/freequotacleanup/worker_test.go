package freequotacleanup

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectListTenantsTx 验证 listTenants 在事务内设 app.current_role 后做
// DISTINCT 查询. 失败/空回退由 expectListTenantsFallback 表达.
func expectListTenantsTx(mock sqlmock.Sqlmock, tenants ...string) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.current_role'")).
		WithArgs("super_admin", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"tenant_id"})
	for _, tn := range tenants {
		rows.AddRow(tn)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(rows)
	mock.ExpectCommit()
}

// expectListTenantsFallback 验证 listTenants 失败/空时退化为 ['default']
// (BeginTx + Query 失败 → Rollback, 然后开始 tenant=default 事务).
func expectListTenantsFallback(mock sqlmock.Sqlmock, queryErr error) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.current_role'")).
		WithArgs("super_admin", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	if queryErr != nil {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
			WillReturnError(queryErr)
	} else {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	}
	mock.ExpectRollback()
}

// TestCleanupOldWindows_MultipleTenants 验证按 tenant 循环并对每个租户设置
// app.current_tenant 后执行 DELETE.
func TestCleanupOldWindows_MultipleTenants(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectListTenantsTx(mock, "default", "tenant-b")

	// tenant=default
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 7))
	mock.ExpectCommit()

	// tenant=tenant-b
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("tenant-b", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.cleanupOldWindows(context.Background()); err != nil {
		t.Fatalf("cleanupOldWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestCleanupOldWindows_EmptyTable 验证空表时仅处理 default.
func TestCleanupOldWindows_EmptyTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectListTenantsTx(mock) // no rows

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.cleanupOldWindows(context.Background()); err != nil {
		t.Fatalf("cleanupOldWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestCleanupOldWindows_ListTenantsFailsBackToDefault 验证 listTenants
// 失败时退化为 default 租户 (RLS 受限场景).
func TestCleanupOldWindows_ListTenantsFailsBackToDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectListTenantsFallback(mock, sql.ErrConnDone)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.cleanupOldWindows(context.Background()); err != nil {
		t.Fatalf("cleanupOldWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
