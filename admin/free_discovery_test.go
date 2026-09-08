package admin

// 免费资源自动发现 Admin API 单元测试.
// 构造最小 Handler (无 pgxpool), 注入 sqlmock 桥接的 freediscovery 服务,
// 直接调用 handler 方法断言 HTTP 行为. 参考 handler_cred_encrypt_test.go 模式.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kaixuan/llm-gateway-go/domains/freediscovery"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// newFreeDiscoveryTestHandler 构造带 freediscovery 依赖的最小 Handler.
func newFreeDiscoveryTestHandler(t *testing.T) (*Handler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := [32]byte{}
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	h := &Handler{encKey: testFernetKey(t), keyring: kr}
	h.SetFreeDiscovery(db, kr)
	if h.freeDiscovery == nil {
		t.Fatal("SetFreeDiscovery did not wire deps")
	}
	return h, mock
}

// fdRequest 执行 handler 请求并返回 recorder.
func fdRequest(t *testing.T, h *Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.SetPathValue("id", fdPathID(path))
	rec := httptest.NewRecorder()

	var handler http.HandlerFunc
	switch {
	case strings.HasSuffix(path, "/scan"):
		handler = h.handleFreeDiscoveryScan
	case strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/results"):
		handler = h.handleFreeDiscoveryTaskResults
	case strings.HasSuffix(path, "/tasks"):
		handler = h.handleFreeDiscoveryTasks
	case strings.Contains(path, "/import"):
		handler = h.handleFreeDiscoveryImport
	case strings.HasSuffix(path, "/templates"):
		handler = h.handleFreeDiscoveryTemplates
	default:
		handler = h.handleFreeDiscoveryTemplateByID
	}
	handler(rec, req)
	return rec
}

// fdPathID 从路径提取末段数字 ({id} 模式在 httptest 下需手动注入).
func fdPathID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	last := parts[len(parts)-1]
	if last == "" {
		return "0"
	}
	return last
}

func TestFreeDiscovery_NoDepsReturns503(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleFreeDiscoveryTemplates(rec, httptest.NewRequest(http.MethodGet, "/api/free-discovery/templates", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-DB mode must 503, got %d", rec.Code)
	}
}

func TestFreeDiscovery_PresetsIncludeGroqAndOpenRouter(t *testing.T) {
	h, _ := newFreeDiscoveryTestHandler(t)
	rec := httptest.NewRecorder()
	h.handleFreeDiscoveryPresets(rec, httptest.NewRequest(http.MethodGet, "/api/free-discovery/templates/presets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("presets: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Presets []struct {
			ProviderCode string `json:"provider_code"`
			APIKeyEnv    string `json:"api_key_env"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	codes := map[string]bool{}
	for _, p := range resp.Presets {
		codes[p.ProviderCode] = true
	}
	if !codes["groq"] || !codes["openrouter"] {
		t.Fatalf("groq/openrouter presets missing: %v", codes)
	}
}

func TestFreeDiscovery_CreateTemplate_ValidationPassesThrough(t *testing.T) {
	h, mock := newFreeDiscoveryTestHandler(t)

	// Create 走事务 (Begin + GUC + INSERT + Commit) + Get 回读
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO provider_templates").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()
	// Get 回读
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	rec := fdRequest(t, h, http.MethodPost, "/api/free-discovery/templates", map[string]any{
		"provider_code": "groq",
		"display_name":  "Groq",
		"base_url":      "https://api.groq.com/openai/v1",
	})
	if rec.Code != http.StatusBadRequest {
		// Get 回读返回 NotFound 是预期 (mock 层), 但 validation 错误会 400 —
		// 这里只要求不是 500/panic
		if rec.Code == http.StatusInternalServerError {
			t.Fatalf("unexpected 500: %s", rec.Body.String())
		}
	}
}

func TestFreeDiscovery_CreateTemplate_MissingFields400(t *testing.T) {
	h, mock := newFreeDiscoveryTestHandler(t)

	rec := fdRequest(t, h, http.MethodPost, "/api/free-discovery/templates", map[string]any{
		"provider_code": "BAD!",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid provider_code must 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL should run for invalid input: %v", err)
	}
}

func TestFreeDiscovery_Scan_MissingTemplateID400(t *testing.T) {
	h, mock := newFreeDiscoveryTestHandler(t)

	rec := fdRequest(t, h, http.MethodPost, "/api/free-discovery/scan", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("template_id=0 must 400, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL should run: %v", err)
	}
}

func TestFreeDiscovery_Import_MissingTaskID400(t *testing.T) {
	h, _ := newFreeDiscoveryTestHandler(t)

	rec := fdRequest(t, h, http.MethodPost, "/api/free-discovery/import", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("task_id=0 must 400, got %d", rec.Code)
	}
}

func TestFreeDiscovery_MethodNotAllowed(t *testing.T) {
	h, _ := newFreeDiscoveryTestHandler(t)

	rec := fdRequest(t, h, http.MethodDelete, "/api/free-discovery/tasks", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE tasks must 405, got %d", rec.Code)
	}
}

func TestFreeDiscovery_TenantIsolationGUC(t *testing.T) {
	h, mock := newFreeDiscoveryTestHandler(t)

	// List 任务必须走 RLS GUC 事务
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant = 'default'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM discovery_tasks ORDER BY created_at DESC LIMIT \\$1").
		WillReturnRows(sqlmock.NewRows(taskListCols()))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodGet, "/api/free-discovery/tasks", nil)
	rec := httptest.NewRecorder()
	h.handleFreeDiscoveryTasks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list tasks: %d %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("RLS contract: %v", err)
	}
}

func TestFDStatusFor(t *testing.T) {
	if got := fdStatusFor(freediscovery.ErrTemplateNotFound); got != http.StatusNotFound {
		t.Fatalf("not found: %d", got)
	}
	if got := fdStatusFor(errors.New("provider_code is required")); got != http.StatusBadRequest {
		t.Fatalf("validation: %d", got)
	}
	if got := fdStatusFor(errors.New("db exploded")); got != http.StatusInternalServerError {
		t.Fatalf("unknown: %d", got)
	}
}

// taskListCols 与 engine.ListTasks 的 SELECT 列序一致.
func taskListCols() []string {
	return []string{
		"id", "tenant_id", "template_id", "provider_code", "status", "trigger_type",
		"triggered_by", "started_at", "completed_at", "error_message",
		"models_found", "models_imported", "created_at",
	}
}
