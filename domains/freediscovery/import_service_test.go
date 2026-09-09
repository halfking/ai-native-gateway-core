package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// runBeginTx 仅 BeginTx; caller 负责 GUC 与后续语句.
func runBeginTx(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
}

// resultRows 构造两条 pending 结果 (按 audit-fix 后的列顺序含 tenant_id).
func resultRows() *sqlmock.Rows {
	cols := []string{
		"id", "task_id", "tenant_id", "provider_code", "model_id", "display_name",
		"context_window", "max_tokens", "free_type",
		"monthly_tokens", "daily_tokens", "pool_key",
		"tos_verdict", "tos_notes", "import_status",
	}
	return sqlmock.NewRows(cols).
		AddRow(201, 101, "tenant-a", "groq", "llama-3.1-8b-instant", "Llama 3.1 8B", 131072, 8192, "recurring-uncapped", 0, 14400, "", "caution", "free tier", "pending").
		AddRow(202, 101, "tenant-a", "groq", "compound-beta", "Compound Beta", 128000, 8192, "recurring-uncapped", 0, 14400, "", "caution", "free tier", "pending")
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

func TestImportService_TaskNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := s.Import(context.Background(), ImportRequest{TaskID: 999, TenantID: "tenant-a"})
	if !errors.Is(err, ErrImportTaskNotFound) {
		t.Fatalf("want ErrImportTaskNotFound, got %v", err)
	}
}

func TestImportService_TaskNotSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusRunning)))
	mock.ExpectRollback()

	_, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if !errors.Is(err, ErrImportTaskNotReady) {
		t.Fatalf("want ErrImportTaskNotReady, got %v", err)
	}
}

func TestImportService_EmptyPendingIsNoop(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("empty import: %v", err)
	}
	if summary.Imported != 0 || summary.Conflicted != 0 || summary.Skipped != 0 {
		t.Fatalf("empty summary expected, got %+v", summary)
	}
}

func TestImportService_NewRowsInserted(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	// 第一条 probe → 不存在 → INSERT → mark imported
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 2 || summary.Conflicted != 0 || summary.Skipped != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestImportService_ConflictSkip(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	// 第一条: catalog 已存在 → skip → mark skipped
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(555)))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 第二条: 不存在 → INSERT → mark imported
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{
		TaskID: 101, TenantID: "tenant-a", ConflictPolicy: ConflictSkip,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if summary.Imported != 1 || summary.Skipped != 1 || summary.Conflicted != 0 {
		t.Fatalf("skip policy summary: %+v", summary)
	}
}

func TestImportService_ConflictOverwrite(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(555)))
	mock.ExpectExec("UPDATE free_resource_catalog SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").
		WillReturnResult(sqlmock.NewResult(0, 1))
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

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(555)))
	mock.ExpectExec("UPDATE free_resource_catalog SET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").
		WillReturnResult(sqlmock.NewResult(0, 1))
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

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	// 第一条成功, 第二条炸 → 整体回滚
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnError(errors.New("constraint violation"))
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

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	// 第一条: 探测无冲突, INSERT 撞 unique constraint → 标记 conflict
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnError(errors.New(`pq: duplicate key value violates unique constraint "free_resource_catalog_provider_model_tenant_key"`))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 第二条: 正常导入
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discovery_tasks SET models_imported").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	summary, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("race import: %v", err)
	}
	if summary.Imported != 1 || summary.Conflicted != 1 {
		t.Fatalf("race should count as conflict: %+v", summary)
	}
}

func TestImportService_ResultAlreadyProcessed(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewImportService(db)

	runBeginTx(mock)
	mock.ExpectQuery("FROM discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "status"}).
			AddRow("tenant-a", string(TaskStatusSuccess)))
	mock.ExpectQuery("FROM discovery_results").
		WillReturnRows(resultRows())
	// 第一条: catalog 不存在 → INSERT 成功 → mark imported CAS 失败 (RowsAffected=0)
	// 表示其他事务已处理; 整体事务回滚.
	mock.ExpectQuery("SELECT id FROM free_resource_catalog").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO free_resource_catalog").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE discovery_results SET import_status=\\$").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err := s.Import(context.Background(), ImportRequest{TaskID: 101, TenantID: "tenant-a"})
	if err == nil || !strings.Contains(err.Error(), "already processed") {
		t.Fatalf("CAS failure must propagate, got %v", err)
	}
}
