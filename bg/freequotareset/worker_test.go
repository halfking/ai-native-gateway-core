package freequotareset

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestResetExpiredWindows_MultipleTenants 验证 worker 按 tenant 循环,
// 每个事务内 SET LOCAL app.current_tenant 并执行 UPDATE.
func TestResetExpiredWindows_MultipleTenants(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// 1) listTenants 查询.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).
			AddRow("default").
			AddRow("tenant-a"))

	// 2) tenant=default 事务: set_config + UPDATE.
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()

	// 3) tenant=tenant-a 事务: set_config + UPDATE.
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("tenant-a", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.resetExpiredWindows(context.Background()); err != nil {
		t.Fatalf("resetExpiredWindows: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestResetExpiredWindows_EmptyTable 验证空表时仅处理 default 租户.
func TestResetExpiredWindows_EmptyTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.resetExpiredWindows(context.Background()); err != nil {
		t.Fatalf("resetExpiredWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestResetExpiredWindows_ListTenantsFailsBackToDefault 验证 listTenants
// 失败时回退到 default tenant (例如 RLS-restricted 角色查不到任何行).
func TestResetExpiredWindows_ListTenantsFailsBackToDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// SELECT 失败 → fallback to ['default'].
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnError(sql.ErrConnDone)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.resetExpiredWindows(context.Background()); err != nil {
		t.Fatalf("resetExpiredWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestResetExpiredWindows_TenantFailureDoesNotAbort 验证单个租户失败不影响其他租户.
func TestResetExpiredWindows_TenantFailureDoesNotAbort(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).
			AddRow("default").
			AddRow("tenant-a"))

	// tenant=default 失败.
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("default", true).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	// tenant=tenant-a 仍应执行.
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("tenant-a", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE free_quota_tracker`)).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectCommit()

	w := NewWorker(db, time.Minute)
	if err := w.resetExpiredWindows(context.Background()); err != nil {
		t.Fatalf("resetExpiredWindows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}