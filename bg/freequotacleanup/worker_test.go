package freequotacleanup

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestCleanupOldWindows_MultipleTenants 验证按 tenant 循环并对每个租户设置
// app.current_tenant 后执行 DELETE.
func TestCleanupOldWindows_MultipleTenants(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).
			AddRow("default").
			AddRow("tenant-b"))

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

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))

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

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT DISTINCT tenant_id FROM free_quota_tracker`)).
		WillReturnError(sql.ErrConnDone)

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