package licensing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// helper: register health routes on a fresh echo instance and return
// the engine + test recorder wired up for a single exchange.
func setupHealthTest(t *testing.T, dataDir string) (*echo.Echo, *LicenseHealthHandler) {
	t.Helper()
	e := echo.New()
	g := e.Group("/api/system/license")
	h := NewLicenseHealthHandler(dataDir, DefaultGracePeriod)
	h.RegisterHealthRoutes(g)
	return e, h
}

func doHealthRequest(e *echo.Echo, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// ──────────────────── AC-LH-API-1: /health always responds 200 ────────────────────

func TestHealthHandler_AlwaysResponds200(t *testing.T) {
	resetSingleton()

	e, _ := setupHealthTest(t, t.TempDir())
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/health")

	if rec.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("body is empty")
	}

	var resp LicenseHealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Healthy {
		t.Error("with no daemon cycles and no marker, healthy should be true")
	}
	if resp.TotalCycles != 0 {
		t.Errorf("TotalCycles should be 0, got %d", resp.TotalCycles)
	}
}

func TestHealthHandler_ReflectsFailures(t *testing.T) {
	resetSingleton()

	health := ensureHealthInit()
	health.RecordFailure(os.ErrInvalid)
	health.RecordFailure(os.ErrPermission)
	health.RecordFailure(os.ErrNotExist)

	e, _ := setupHealthTest(t, t.TempDir())
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/health")

	var resp LicenseHealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.ConsecutiveFails != 3 {
		t.Errorf("ConsecutiveFails=%d, want 3", resp.ConsecutiveFails)
	}
	if resp.TotalFailures != 3 {
		t.Errorf("TotalFailures=%d, want 3", resp.TotalFailures)
	}
	if resp.Healthy {
		t.Error("Healthy should be false with 3 consecutive failures")
	}
	if resp.LastError == "" {
		t.Error("LastError should not be empty")
	}
}

func TestHealthHandler_StalenessDetection(t *testing.T) {
	resetSingleton()

	health := ensureHealthInit()
	health.RecordSuccess()

	// 1-second refresh cadence → 5s ago is "stale" (>2×).
	t.Setenv("REFRESH_INTERVAL_SECONDS", "1")
	health.LastCycleAt = time.Now().Add(-5 * time.Second)

	e, _ := setupHealthTest(t, t.TempDir())
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/health")

	var resp LicenseHealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Stale {
		t.Error("Stale should be true when last cycle > 2× interval")
	}
}

// ──────────────────── AC-LH-API-2: /status grace arithmetic ────────────────────

func TestStatusHandler_NormalWhenNoMarker(t *testing.T) {
	tmpDir := t.TempDir()
	e, _ := setupHealthTest(t, tmpDir)
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/status")

	if rec.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rec.Code)
	}

	var resp LicenseStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Mode != "normal" {
		t.Errorf("Mode=%q, want 'normal' when no marker", resp.Mode)
	}
	if resp.InGrace {
		t.Error("InGrace should be false with no marker")
	}
	if resp.Marker != nil {
		t.Error("Marker should be nil with no failure on disk")
	}
}

func TestStatusHandler_InGraceWithinWindow(t *testing.T) {
	tmpDir := t.TempDir()

	marker := FailureMarker{
		FailedAt: time.Now().Add(-1 * time.Hour),
		Reason:   "test failure",
		Attempts: 1,
	}
	out, _ := marker.Marshal()
	os.WriteFile(filepath.Join(tmpDir, failureMarkerName), out, 0o600)

	e, _ := setupHealthTest(t, tmpDir)
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/status")

	var resp LicenseStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Mode != "in_grace" {
		t.Errorf("Mode=%q, want 'in_grace' for marker within window", resp.Mode)
	}
	if !resp.InGrace {
		t.Error("InGrace should be true")
	}
	// Should leave roughly 23 hours of grace (default 24h, 1h consumed).
	if resp.GraceRemainingSeconds < 80000 || resp.GraceRemainingSeconds > 90000 {
		t.Errorf("GraceRemainingSeconds=%d, want ~82800 (23h)", resp.GraceRemainingSeconds)
	}
	if resp.Marker == nil || resp.Marker.Reason != "test failure" {
		t.Errorf("Marker not echoed correctly: %+v", resp.Marker)
	}
}

func TestStatusHandler_RestrictedAfterGraceExpires(t *testing.T) {
	tmpDir := t.TempDir()

	marker := FailureMarker{
		FailedAt: time.Now().Add(-48 * time.Hour),
		Reason:   "long broken",
		Attempts: 99,
	}
	out, _ := marker.Marshal()
	os.WriteFile(filepath.Join(tmpDir, failureMarkerName), out, 0o600)

	e, _ := setupHealthTest(t, tmpDir)
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/status")

	var resp LicenseStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Mode != "restricted" {
		t.Errorf("Mode=%q, want 'restricted' for expired grace", resp.Mode)
	}
	if resp.InGrace {
		t.Error("InGrace should be false after grace expired")
	}
	if resp.GraceRemainingSeconds != 0 {
		t.Errorf("GraceRemainingSeconds=%d, want 0 in restricted", resp.GraceRemainingSeconds)
	}
}

func TestStatusHandler_NoGraceEnvSurfaced(t *testing.T) {
	tmpDir := t.TempDir()
	marker := FailureMarker{FailedAt: time.Now(), Reason: "test"}
	out, _ := marker.Marshal()
	os.WriteFile(filepath.Join(tmpDir, failureMarkerName), out, 0o600)

	t.Setenv("LICENSE_NO_GRACE", "1")

	e, _ := setupHealthTest(t, tmpDir)
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/status")

	var resp LicenseStatusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.NoGraceHonored {
		t.Error("NoGraceHonored should be true when LICENSE_NO_GRACE=1")
	}
}

// ──────────────────── AC-LH-API-3: in restricted mode the endpoints respond ────────────────────

func TestHealthEndpoints_WorkInRestrictedMode(t *testing.T) {
	resetSingleton()
	health := ensureHealthInit()
	health.RecordFailure(os.ErrInvalid)

	tmpDir := t.TempDir()

	// Mount through the restricted-mode middleware to verify the
	// /health and /status paths are allow-listed.
	restricted := RestrictedModeMiddleware()
	mounted := echo.New()
	apiGroup := mounted.Group("/api/system/license")
	mounted.Use(restricted)
	NewLicenseHealthHandler(tmpDir, DefaultGracePeriod).RegisterHealthRoutes(apiGroup)

	for _, path := range []string{"/api/system/license/health", "/api/system/license/status"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mounted.ServeHTTP(rec, req)

		if rec.Code == http.StatusServiceUnavailable {
			t.Errorf("path=%q blocked under restricted mode — should be allowed (license path)", path)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("path=%q status=%d, want 200", path, rec.Code)
		}
	}
}

// ──────────────────── AC-LH-API-4: response shape contract ────────────────────

func TestHealthHandler_ResponseIsJSON(t *testing.T) {
	resetSingleton()
	ensureHealthInit().RecordSuccess()

	e, _ := setupHealthTest(t, t.TempDir())
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/health")

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q, want application/json…", ct)
	}

	body, _ := io.ReadAll(rec.Body)
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Errorf("body not valid JSON object: %v\nbody: %s", err, body)
	}
	if _, ok := probe["healthy"]; !ok {
		t.Error("response missing 'healthy' field")
	}
}

func TestStatusHandler_ResponseIsJSON(t *testing.T) {
	e, _ := setupHealthTest(t, t.TempDir())
	rec := doHealthRequest(e, http.MethodGet, "/api/system/license/status")

	body, _ := io.ReadAll(rec.Body)
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Errorf("body not valid JSON: %v\nbody: %s", err, body)
	}
	for _, key := range []string{"mode", "grace_configured_seconds", "in_grace", "grace_remaining_seconds"} {
		if _, ok := probe[key]; !ok {
			t.Errorf("response missing '%s' field", key)
		}
	}
}
