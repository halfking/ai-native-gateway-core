package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// W1.3 surfaces that must refuse with recording_unavailable when consent=off.
// Source-level gate: a new body-returning handler without the call fails here
// before a silent-empty regression ships.
func TestRecordingConsentSurfacesCallRequire(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	adminDir := filepath.Dir(thisFile)

	mustContain := []string{
		"session_summary_v2.go",
		"session_detail_v2.go",
		"session_turns_v2.go",
	}
	for _, name := range mustContain {
		raw, err := os.ReadFile(filepath.Join(adminDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		if !strings.Contains(src, "requireRecordingConsent(w, tenantID)") {
			t.Errorf("%s missing requireRecordingConsent — body/replay/summary must gate consent", name)
		}
	}
	turnsRaw, err := os.ReadFile(filepath.Join(adminDir, "session_turns_v2.go"))
	if err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(string(turnsRaw), "requireRecordingConsent(w, tenantID)"); c < 3 {
		t.Errorf("session_turns_v2.go: want ≥3 requireRecordingConsent sites (detail/bodies/instant-summary), got %d", c)
	}
}

func TestRequireRecordingConsent_OffReturnsStructuredConflict(t *testing.T) {
	prev := settings.TestingConsentOverride
	off := false
	settings.TestingConsentOverride = &off
	t.Cleanup(func() { settings.TestingConsentOverride = prev })

	rec := httptest.NewRecorder()
	if requireRecordingConsent(rec, "tenant-x") {
		t.Fatal("expected false when consent off")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("missing error object: %s", rec.Body.String())
	}
	if got, _ := errObj["code"].(string); got != RecordingUnavailableCode {
		t.Fatalf("error.code = %q, want %q", got, RecordingUnavailableCode)
	}
}

func TestSessionDetailV2_ConsentOffUnavailable(t *testing.T) {
	prev := settings.TestingConsentOverride
	off := false
	settings.TestingConsentOverride = &off
	t.Cleanup(func() { settings.TestingConsentOverride = prev })

	api := &SessionDetailV2API{pool: consentGateDB{}}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=s1&tenant=t1", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "t1"})
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), RecordingUnavailableCode) {
		t.Fatalf("body missing %s: %s", RecordingUnavailableCode, rec.Body.String())
	}
}

// consentGateDB panics if touched — consent must return before any query.
type consentGateDB struct{}

func (consentGateDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("consent gate should return before Query")
}
func (consentGateDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("consent gate should return before QueryRow")
}
func (consentGateDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("consent gate should return before Exec")
}
