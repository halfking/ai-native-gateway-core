package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// runBegin 期望一个带 RLS GUC 的事务开始.
func runBegin(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestDiscoveryEngine_Run_TemplateNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	// createTask: 事务内查 provider_templates → 无行
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 999, TenantID: "tenant-a",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("template not found must fail the run, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestDiscoveryEngine_Run_ScannerFailureMarksTaskFailed(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-test-env")
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))
	engine.SetProviderScanner("groq", &stubScanner{err: errors.New("upstream exploded")})

	// 1. createTask
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("groq"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectCommit()

	// 2. mark running
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 3. Get template (Tx + SELECT)
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRows(7))
	mock.ExpectRollback()

	// 4. fail: 标记 failed
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2, error_message=\\$3").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 7, TenantID: "tenant-a",
	})
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("scanner error must propagate, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestDiscoveryEngine_Run_HappyPath(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-test-env")
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": [
			{"id": "llama-3.1-8b-instant", "display_name": "Llama 3.1 8B", "context_window": 131072, "max_output_tokens": 8192}
		]}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.ID = 7
	tpl.BaseURL = srv.URL

	// 1. createTask
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("groq"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectCommit()

	// 2. mark running
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 3. Get template
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").WillReturnRows(templateRowsWith(tpl))
	mock.ExpectRollback()

	// 4. saveResults (单事务批量)
	runBegin(mock)
	mock.ExpectPrepare("INSERT INTO discovery_results")
	mock.ExpectExec("INSERT INTO discovery_results").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	// 5. updateTask success
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 6. GetTask (终态回读)
	runBegin(mock)
	mock.ExpectQuery("FROM discovery_tasks WHERE id=\\$1").
		WillReturnRows(taskRows(101, "success", 1))
	mock.ExpectRollback()

	task, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 7, TenantID: "tenant-a", TriggeredBy: "admin",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if task.Status != TaskStatusSuccess || task.ModelsFound != 1 {
		t.Fatalf("task state: %+v", task)
	}
	if task.ProviderCode != "groq" {
		t.Fatalf("provider code: %q", task.ProviderCode)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestDiscoveryEngine_Run_NoScannerForProvider(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-test-env")
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("exotic-provider"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(102))
	mock.ExpectCommit()

	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRowsWith(&ProviderTemplate{
			ID: 7, ProviderCode: "exotic-provider", APIType: "mystery",
			BaseURL: "https://x.com", Enabled: true, TosVerdict: "unknown",
		}))
	mock.ExpectRollback()

	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2, error_message=\\$3").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := engine.Run(context.Background(), DiscoveryRequest{TemplateID: 7, TenantID: "tenant-a"})
	if err == nil || !strings.Contains(err.Error(), "no scanner") {
		t.Fatalf("missing scanner must fail loudly, got %v", err)
	}
}

// --- helpers ---

func templateRows(id int64) *sqlmock.Rows {
	cols := []string{
		"id", "tenant_id", "provider_code", "display_name", "base_url", "api_type",
		"api_key_env", "api_key_encrypted", "models_endpoint",
		"quota_endpoint", "tos_url", "tos_verdict", "tos_notes",
		"enabled", "created_by", "created_at", "updated_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		id, "tenant-a", "groq", "Groq Free", "https://api.groq.test/openai/v1", "openai-completions",
		"$GROQ_API_KEY", nil, "/models",
		"", "", "caution", "free tier",
		true, "admin", nil, nil,
	)
}

func templateRowsWith(tpl *ProviderTemplate) *sqlmock.Rows {
	cols := []string{
		"id", "tenant_id", "provider_code", "display_name", "base_url", "api_type",
		"api_key_env", "api_key_encrypted", "models_endpoint",
		"quota_endpoint", "tos_url", "tos_verdict", "tos_notes",
		"enabled", "created_by", "created_at", "updated_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		tpl.ID, "tenant-a", tpl.ProviderCode, tpl.DisplayName, tpl.BaseURL, string(tpl.APIType),
		tpl.APIKeyEnv, tpl.APIKeyEncrypted, tpl.ModelsEndpoint,
		"", tpl.TosURL, tpl.TosVerdict, tpl.TosNotes,
		tpl.Enabled, "admin", nil, nil,
	)
}

func taskRows(id int64, status string, found int) *sqlmock.Rows {
	cols := []string{
		"id", "tenant_id", "template_id", "provider_code", "status", "trigger_type",
		"triggered_by", "started_at", "completed_at", "error_message",
		"models_found", "models_imported", "created_at", "updated_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		id, "tenant-a", 7, "groq", status, "manual",
		"admin", nil, nil, "",
		found, 0, nil, nil,
	)
}

// stubScanner 固定输出/错误的扫描器.
type stubScanner struct {
	models []DiscoveredModel
	err    error
}

func (s *stubScanner) ScanModels(_ context.Context, _ *ProviderTemplate, _ string) ([]DiscoveredModel, error) {
	return s.models, s.err
}
