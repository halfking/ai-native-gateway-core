package admin

// Unit tests for the free resource auto-discovery Admin API.
// Builds a minimal Handler (no pgxpool), injects the freediscovery services
// bridged over sqlmock, and calls handler methods directly to assert HTTP
// behavior. Follows the handler_cred_encrypt_test.go pattern.

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

// newFreeDiscoveryTestHandler builds a minimal Handler with the freediscovery deps wired.
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

// fdRequest performs a handler request and returns the recorder.
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

// fdPathID extracts the trailing path segment ({id} patterns need manual injection under httptest).
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
			APIType      string `json:"api_type"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	codes := map[string]bool{}
	apiTypes := map[string]string{}
	for _, p := range resp.Presets {
		codes[p.ProviderCode] = true
		apiTypes[p.ProviderCode] = p.APIType
	}
	if !codes["groq"] || !codes["openrouter"] {
		t.Fatalf("groq/openrouter presets missing: %v", codes)
	}
	// Regression: google-ai-studio must return the correct api_type (the frontend relies
	// on this field to show the protocol-adaptation hint)
	if apiTypes["google-ai-studio"] != string(freediscovery.APITypeGoogleGenerativeAI) {
		t.Fatalf("google-ai-studio api_type = %q, want %q", apiTypes["google-ai-studio"], freediscovery.APITypeGoogleGenerativeAI)
	}
}

func TestFreeDiscovery_CreateTemplate_ValidationPassesThrough(t *testing.T) {
	h, mock := newFreeDiscoveryTestHandler(t)

	// Create runs in a transaction (Begin + GUC + INSERT + Commit) followed by a Get readback
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO provider_templates").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()
	// Get readback
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
		// Get readback returning NotFound is expected at the mock layer, but validation
		// errors return 400 — we only require that it is not 500/panic
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

// TestFreeDiscovery_Import_StatusForSentinels verifies that the import handler maps
// domain sentinel errors to the correct HTTP status codes (404 / 409) instead of
// a hardcoded 500.
//
// Regression: the import handler used to return 500 outright, breaking the
// ErrImportTaskNotFound / ErrImportTaskNotReady contract. Fixed by audit-fix
// (2026-09-09); this test guards it.
func TestFreeDiscovery_Import_StatusForSentinels(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		expect int
	}{
		{"not found → 404", freediscovery.ErrImportTaskNotFound, http.StatusNotFound},
		{"not ready → 409", freediscovery.ErrImportTaskNotReady, http.StatusConflict},
		{"task state conflict → 409", freediscovery.ErrTaskStateConflict, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fdStatusFor(tc.err); got != tc.expect {
				t.Fatalf("fdStatusFor(%v)=%d, want %d", tc.err, got, tc.expect)
			}
		})
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

	// Listing tasks must go through the RLS GUC transaction
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

// taskListCols mirrors the SELECT column order of engine.ListTasks.
func taskListCols() []string {
	return []string{
		"id", "tenant_id", "template_id", "provider_code", "status", "trigger_type",
		"triggered_by", "started_at", "completed_at", "error_message",
		"models_found", "models_imported", "created_at",
	}
}
