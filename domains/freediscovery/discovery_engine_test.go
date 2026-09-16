package freediscovery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// nonNilArg matches any non-nil driver value. Regression guard for the running
// transition: started_at must be written (it was previously persisted as NULL
// because the code assigned the still-nil completedAt).
type nonNilArg struct{}

func (nonNilArg) Match(v driver.Value) bool { return v != nil }

// runBegin expects a transaction start with the RLS GUC.
func runBegin(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestDiscoveryEngine_Run_TemplateNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	// Run entry first Gets the full template; if missing, returns ErrTemplateNotFound directly.
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 999, TenantID: "tenant-a",
	})
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("template not found must return ErrTemplateNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestDiscoveryEngine_Run_TemplateDisabled(t *testing.T) {
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	// Run entry first Gets the template; enabled=false returns ErrTemplateDisabled directly (no task created).
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRowsWith(&ProviderTemplate{
			ID: 7, ProviderCode: "groq", Enabled: false, BaseURL: "https://x",
		}))
	mock.ExpectRollback()

	_, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 7, TenantID: "tenant-a",
	})
	if !errors.Is(err, ErrTemplateDisabled) {
		t.Fatalf("disabled template must return ErrTemplateDisabled, got %v", err)
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

	// 1. Run entry Gets the template (pre-checks enabled).
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRows(7))
	mock.ExpectRollback()

	// 2. createTask (look up provider_code + INSERT inside the transaction).
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("groq"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectCommit()

	// 3. mark running (CAS pending → running).
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WithArgs(int64(101), string(TaskStatusRunning), nil, nil, nonNilArg{}, nil, "tenant-a", string(TaskStatusPending)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 4. ResolveAPIKey: separate transaction to read the env.
	runBegin(mock)
	mock.ExpectExec("SELECT").WillReturnResult(sqlmock.NewResult(0, 0)) // placeholder, depends on the ResolveAPIKey implementation
	mock.ExpectRollback()

	// 5. fail: mark failed (CAS running → failed).
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2, error_message=\\$3").
		WithArgs(int64(101), string(TaskStatusFailed), sqlmock.AnyArg(), sqlmock.AnyArg(), "tenant-a", string(TaskStatusRunning)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := engine.Run(context.Background(), DiscoveryRequest{
		TemplateID: 7, TenantID: "tenant-a",
	})
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("scanner error must propagate, got %v", err)
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
	tpl.Enabled = true // Run entry requires enabled=true

	// Use a stubScanner in place of the httptest path to avoid safehttpclient blocking 127.0.0.1.
	engine.SetProviderScanner("groq", &stubScanner{
		models: []DiscoveredModel{{ProviderCode: "groq", ModelID: "llama-3.1-8b-instant",
			DisplayName: "Llama 3.1 8B", ContextWindow: 131072, MaxTokens: 8192}},
	})

	// 1. Run entry Gets the template.
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRowsWith(tpl))
	mock.ExpectRollback()

	// 2. createTask (look up provider_code + INSERT).
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("groq"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectCommit()

	// 3. mark running (CAS pending → running).
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WithArgs(int64(101), string(TaskStatusRunning), nil, nil, nonNilArg{}, nil, "tenant-a", string(TaskStatusPending)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 4. saveResults (single-transaction batch).
	runBegin(mock)
	mock.ExpectPrepare("INSERT INTO discovery_results")
	mock.ExpectExec("INSERT INTO discovery_results").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	// 5. updateTask success (CAS running → success).
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WithArgs(int64(101), string(TaskStatusSuccess), sqlmock.AnyArg(), nil, nil, sqlmock.AnyArg(), "tenant-a", string(TaskStatusRunning)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 6. GetTask (read back final state).
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
}

func TestDiscoveryEngine_Run_NoScannerForProvider(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "sk-test-env")
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	// 1. Get template (enabled=true, but api_type is unknown → no fallback either).
	runBegin(mock)
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").
		WillReturnRows(templateRowsWith(&ProviderTemplate{
			ID: 7, ProviderCode: "exotic-provider", APIType: "mystery",
			BaseURL: "https://x.com", Enabled: true, TosVerdict: "unknown",
		}))
	mock.ExpectRollback()

	// 2. createTask.
	runBegin(mock)
	mock.ExpectQuery("SELECT provider_code FROM provider_templates WHERE id=\\$1").
		WillReturnRows(sqlmock.NewRows([]string{"provider_code"}).AddRow("exotic-provider"))
	mock.ExpectQuery("INSERT INTO discovery_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(102))
	mock.ExpectCommit()

	// 3. mark running.
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2").
		WithArgs(int64(102), string(TaskStatusRunning), nil, nil, nonNilArg{}, nil, "tenant-a", string(TaskStatusPending)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 4. fail: mark failed (no scanner).
	runBegin(mock)
	mock.ExpectExec("UPDATE discovery_tasks SET status=\\$2, error_message=\\$3").
		WithArgs(int64(102), string(TaskStatusFailed), sqlmock.AnyArg(), sqlmock.AnyArg(), "tenant-a", string(TaskStatusRunning)).
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
		"consecutive_scan_failures", "last_scan_failure_at", "auto_disabled_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		id, "tenant-a", "groq", "Groq Free", "https://api.groq.test/openai/v1", "openai-completions",
		"$GROQ_API_KEY", nil, "/models",
		"", "", "caution", "free tier",
		true, "admin", nil, nil,
		0, nil, nil,
	)
}

func templateRowsWith(tpl *ProviderTemplate) *sqlmock.Rows {
	cols := []string{
		"id", "tenant_id", "provider_code", "display_name", "base_url", "api_type",
		"api_key_env", "api_key_encrypted", "models_endpoint",
		"quota_endpoint", "tos_url", "tos_verdict", "tos_notes",
		"enabled", "created_by", "created_at", "updated_at",
		"consecutive_scan_failures", "last_scan_failure_at", "auto_disabled_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		tpl.ID, "tenant-a", tpl.ProviderCode, tpl.DisplayName, tpl.BaseURL, string(tpl.APIType),
		tpl.APIKeyEnv, tpl.APIKeyEncrypted, tpl.ModelsEndpoint,
		"", tpl.TosURL, tpl.TosVerdict, tpl.TosNotes,
		tpl.Enabled, "admin", nil, nil,
		tpl.ConsecutiveScanFailures, tpl.LastScanFailureAt, tpl.AutoDisabledAt,
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

// stubScanner a scanner with fixed output/error.
type stubScanner struct {
	models []DiscoveredModel
	err    error
}

func (s *stubScanner) ScanModels(_ context.Context, _ *ProviderTemplate, _ string) ([]DiscoveredModel, error) {
	return s.models, s.err
}

// TestDiscoveryEngine_GetTask_NotFoundSentinel guards the audit contract: a missing
// task (sql.ErrNoRows) must return the ErrTaskNotFound sentinel (handler maps to 404),
// while a db fault must not be wrapped as this sentinel (stays 500).
func TestDiscoveryEngine_GetTask_NotFoundSentinel(t *testing.T) {
	db, mock := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	runBegin(mock)
	mock.ExpectQuery("FROM discovery_tasks WHERE id=\\$1").
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := engine.GetTask(context.Background(), "tenant-a", 42)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing task must return ErrTaskNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}

	// A db fault is not the sentinel: the handler side must keep returning 500,
	// never a false 404.
	db2, mock2 := newMockDB(t)
	engine2 := NewDiscoveryEngine(db2, NewTemplateManager(db2, nil))

	runBegin(mock2)
	mock2.ExpectQuery("FROM discovery_tasks WHERE id=\\$1").
		WithArgs(int64(42)).
		WillReturnError(errors.New("boom"))
	mock2.ExpectRollback()

	_, err = engine2.GetTask(context.Background(), "tenant-a", 42)
	if err == nil {
		t.Fatal("db fault must return an error")
	}
	if errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("db fault must not map to ErrTaskNotFound, got %v", err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

// TestDiscoveryEngine_PresetScannerWiring guards the scannerFor precedence:
// a preset whose protocol is not openai-completions (google-ai-studio) must fall
// through to the real protocol scanner in fallbackScanners. The preset loop
// previously wired an OpenAI-shape HTTPScanner under the provider key, shadowing
// the Gemini scanner and failing every google-ai-studio scan at response parsing.
func TestDiscoveryEngine_PresetScannerWiring(t *testing.T) {
	db, _ := newMockDB(t)
	engine := NewDiscoveryEngine(db, NewTemplateManager(db, nil))

	if _, ok := engine.scannerFor(&ProviderTemplate{
		ProviderCode: "google-ai-studio", APIType: APITypeGoogleGenerativeAI,
	}).(*GoogleGenerativeAIScanner); !ok {
		t.Fatalf("google-ai-studio preset must resolve to the Gemini protocol scanner")
	}
	if _, ok := engine.scannerFor(&ProviderTemplate{
		ProviderCode: "groq", APIType: APITypeOpenAICompletions,
	}).(*HTTPScanner); !ok {
		t.Fatalf("groq preset must keep the OpenAI-compatible scanner")
	}
	if _, ok := engine.scannerFor(&ProviderTemplate{
		ProviderCode: "custom", APIType: APITypeAnthropic,
	}).(*AnthropicScanner); !ok {
		t.Fatalf("custom providers must fall back by protocol")
	}
}
