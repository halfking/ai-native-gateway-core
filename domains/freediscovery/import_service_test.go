package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// resultRows 构造两条 pending 结果.
func resultRows() *sqlmock.Rows {
	cols := []string{
		"id", "task_id", "provider_code", "model_id", "display_name",
		"context_window", "max_tokens", "free_type",
		"monthly_tokens", "daily_tokens", "pool_key",
		"tos_verdict", "tos_notes", "import_status",
	}
	return sqlmock.NewRows(cols).
		AddRow(201, 101, "groq", "llama-3.1-8b-instant", "Llama 3.1 8B", 131072, 8192, "recurring-uncapped", 0, 14400, "", "caution", "free tier", "pending").
		AddRow(202, 101, "groq", "compound-beta", "Compound Beta", 128000, 8192, "recurring-uncapped", 0, 14400, "", "caution", "free tier", "pending")
}

func TestImportService_InvalidPolicy(t *testing.T) {
	db, _ := newMockDB(t)
	s := NewImportService(db)
	_, err := s.Import(context.Background(), ImportRequest{
		TaskID: 1, TenantID: "t", ConflictPolicy: "nuke",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid conflict_policy") {
		t.Fatalf("invalid policy must fail, got %v", err)
	}
}

func TestImportService_EmptyPendingIsNoop(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WithArgs(int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("empty import: %v", err)
	}
	if summary.Imported != 0 || summary.Conflicted != 0 {
		t.Fatalf("empty summary expected, got %+v", summary)
	}
}

func TestImportService_NewRowsInserted(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	// 导入事务: 逐条 probe → insert → mark imported 交错
	runBegin(mock)
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 2 || summary.Conflicted != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestImportService_ConflictSkip(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	runBegin(mock)
	// 第一条冲突 (已存在启用条目) → skip; 第二条无冲突 → insert
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id", "enabled"}).AddRow(int64(555), true))
	// skip: 标记 conflict 状态
	mock.ExpectExec("UPDATE discovery_results SET import_status='conflict'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{
		TaskID: 101, TenantID: "tenant-a", ConflictPolicy: ConflictSkip,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 1 || summary.Conflicted != 1 {
		t.Fatalf("skip policy summary: %+v", summary)
	}
}

func TestImportService_ConflictOverwrite(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	runBegin(mock)
	// 第一条冲突 → overwrite (UPDATE 不产生 conflict 标记)
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id", "enabled"}).AddRow(int64(555), true))
	// 占位符契约: $1=existingID, $2..$8=字段, $9=taskID, $10=now — 参数个数必须精确匹配
	mock.ExpectExec("UPDATE free_resource_catalog SET").
		WithArgs(int64(555), "Llama 3.1 8B", "recurring-uncapped", int64(0), int64(14400),
			"", "caution", "free tier", int64(101), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{
		TaskID: 101, TenantID: "tenant-a", ConflictPolicy: ConflictOverwrite,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 2 || summary.Conflicted != 0 {
		t.Fatalf("overwrite policy summary: %+v", summary)
	}
}

func TestImportService_ConflictMerge(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	runBegin(mock)
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id", "enabled"}).AddRow(int64(555), true))
	// merge 走 COALESCE/NULLIF 分支 UPDATE; 占位符 $1..$10 连续编号
	mock.ExpectExec("UPDATE free_resource_catalog SET").
		WithArgs(int64(555), "Llama 3.1 8B", "recurring-uncapped", int64(0), int64(14400),
			"", "caution", "free tier", int64(101), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{
		TaskID: 101, TenantID: "tenant-a", ConflictPolicy: ConflictMerge,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 2 || summary.Conflicted != 0 {
		t.Fatalf("merge policy summary: %+v", summary)
	}
}

func TestImportService_InsertFailureRollsBackAll(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	runBegin(mock)
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").WillReturnError(sql.ErrNoRows)
	// 第一条成功, 第二条炸 → 整体回滚
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnError(errors.New("constraint violation"))
	mock.ExpectRollback()

	_, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err == nil || !strings.Contains(err.Error(), "constraint violation") {
		t.Fatalf("insert failure must propagate, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("rollback expected: %v", err)
	}
}

func TestImportService_DuplicateKeyRaceTreatedAsConflict(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_results WHERE task_id=\\$1 AND import_status='pending'").
		WillReturnRows(resultRows())
	mock.ExpectRollback()

	runBegin(mock)
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").WillReturnError(sql.ErrNoRows)
	// 并发竞态: 冲突探测无行, INSERT 撞唯一约束
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnError(errors.New(`pq: duplicate key value violates unique constraint "free_resource_catalog_provider_model_tenant_key"`))
	// 竞态冲突同样标记 import_status='conflict'
	mock.ExpectExec("UPDATE discovery_results SET import_status='conflict'").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, enabled FROM free_resource_catalog").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status='imported'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("race import: %v", err)
	}
	if summary.Imported != 1 || summary.Conflicted != 1 {
		t.Fatalf("race should count as conflict: %+v", summary)
	}
}
