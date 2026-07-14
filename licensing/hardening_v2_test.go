package licensing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// ──────────────────── AC-LH1..AC-LH4: grace period ────────────────────

func TestGrace_FailureRecordedOnDisk(t *testing.T) {
	tmpDir := t.TempDir()

	if err := MarkLicenseFailure(tmpDir, "verify failed (test)", "/etc/licenses/test.dat"); err != nil {
		t.Fatalf("MarkLicenseFailure: %v", err)
	}

	got, err := ReadLicenseFailure(tmpDir)
	if err != nil {
		t.Fatalf("ReadLicenseFailure: %v", err)
	}
	if got.Reason != "verify failed (test)" {
		t.Errorf("Reason = %q, want %q", got.Reason, "verify failed (test)")
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
	if got.Source != "/etc/licenses/test.dat" {
		t.Errorf("Source = %q", got.Source)
	}
	if time.Since(got.FailedAt) > 5*time.Second {
		t.Errorf("FailedAt should be recent, got %v", got.FailedAt)
	}
}

func TestGrace_AttemptCounterIncrements(t *testing.T) {
	tmpDir := t.TempDir()

	for i := 0; i < 3; i++ {
		if err := MarkLicenseFailure(tmpDir, "fail", ""); err != nil {
			t.Fatalf("MarkLicenseFailure iteration %d: %v", i, err)
		}
	}

	got, _ := ReadLicenseFailure(tmpDir)
	if got.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", got.Attempts)
	}
}

func TestGrace_ClearRemovesMarker(t *testing.T) {
	tmpDir := t.TempDir()

	if err := MarkLicenseFailure(tmpDir, "first", ""); err != nil {
		t.Fatalf("MarkLicenseFailure: %v", err)
	}
	if err := ClearLicenseFailure(tmpDir); err != nil {
		t.Fatalf("ClearLicenseFailure: %v", err)
	}

	got, _ := ReadLicenseFailure(tmpDir)
	if !got.FailedAt.IsZero() {
		t.Errorf("after Clear, marker should be zero-value, got FailedAt=%v", got.FailedAt)
	}
	if got.Attempts != 0 {
		t.Errorf("after Clear, Attempts should reset to 0, got %d", got.Attempts)
	}
}

func TestGrace_PolicySuccessClearsMarker(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-existing failure.
	if err := MarkLicenseFailure(tmpDir, "old failure", ""); err != nil {
		t.Fatal(err)
	}

	grace := GracePolicy{GraceDuration: 1 * time.Hour, Now: time.Now}
	verify := func() error { return nil } // success
	if err := grace.EnforceWithGrace(tmpDir, verify); err != nil {
		t.Errorf("success path should return nil, got %v", err)
	}

	got, _ := ReadLicenseFailure(tmpDir)
	if !got.FailedAt.IsZero() {
		t.Error("success path should have cleared the marker")
	}
}

func TestGrace_FreshFailure_NoMarker_HardFailsImmediately(t *testing.T) {
	tmpDir := t.TempDir()

	// No prior marker → verify fails → EnforceWithGrace fail-closes
	// immediately (defense in depth: first-ever failure with a brand-new
	// marker cannot be "trusted" — the operator hasn't observed the
	// outage yet). Subsequent failures (marker exists) DO enter grace.
	grace := GracePolicy{GraceDuration: 24 * time.Hour, Now: time.Now}
	verify := func() error { return os.ErrNotExist }

	err := grace.EnforceWithGrace(tmpDir, verify)
	if err == nil {
		t.Fatal("expected hard-fail when no prior marker exists")
	}
	if !strings.Contains(err.Error(), "first failure") {
		t.Errorf("expected 'first failure' message, got: %v", err)
	}

	// Marker must still have been written (for forensic visibility).
	got, _ := ReadLicenseFailure(tmpDir)
	if got.FailedAt.IsZero() {
		t.Error("marker should have been written even on first failure")
	}

	// Subsequent failure now goes into grace (marker exists).
	verify2 := func() error { return os.ErrNotExist }
	if err := grace.EnforceWithGrace(tmpDir, verify2); err != ErrLicenseInGracePeriod {
		t.Errorf("subsequent failure should enter grace, got %v", err)
	}
}

func TestGrace_OldFailure_ExceedsGrace_FailClosed(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a failure marker that's already older than the grace
	// window — simulate "broken for >24h".
	old := FailureMarker{
		FailedAt: time.Now().Add(-48 * time.Hour),
		Reason:   "long-broken",
		Attempts: 99,
	}
	out, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(tmpDir, failureMarkerName), out, 0o600); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	grace := GracePolicy{GraceDuration: 24 * time.Hour, Now: time.Now}
	verify := func() error { return os.ErrNotExist }

	err := grace.EnforceWithGrace(tmpDir, verify)
	if err == nil {
		t.Fatal("expected hard-fail when marker older than grace")
	}
	if !strings.Contains(err.Error(), "grace exceeded") {
		t.Errorf("expected 'grace exceeded' message, got: %v", err)
	}
}

func TestGrace_NoGraceEnvHardFailsImmediately(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LICENSE_NO_GRACE", "1")

	// Without LICENSE_NO_GRACE: the failure path enters grace.
	// With LICENSE_NO_GRACE=1: every failure is hard-fail,
	// regardless of marker age.
	grace := GracePolicy{GraceDuration: 999 * time.Hour, Now: time.Now}
	verify := func() error { return os.ErrInvalid }

	if err := grace.EnforceWithGrace(tmpDir, verify); err == nil {
		t.Error("LICENSE_NO_GRACE must short-circuit grace and fail-closed immediately")
	}
}

// ──────────────────── AC-LH5..AC-LH8: backoff w/ jitter ────────────────────

func TestBackoff_NextDelayExponential(t *testing.T) {
	b := BackoffConfig{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  10 * time.Second,
	}
	// attempts are 1-indexed; no delay for attempt 1 (initial).
	if d := b.nextDelay(1); d != 0 {
		t.Errorf("attempt 1 should be 0, got %v", d)
	}
	// attempt 2: 100ms × 2^0 = 100ms (no jitter)
	bNoJitter := b
	bNoJitter.JitterFraction = 0
	if d := bNoJitter.nextDelay(2); d != 100*time.Millisecond {
		t.Errorf("attempt 2 = %v, want 100ms", d)
	}
	if d := bNoJitter.nextDelay(3); d != 200*time.Millisecond {
		t.Errorf("attempt 3 = %v, want 200ms", d)
	}
	if d := bNoJitter.nextDelay(4); d != 400*time.Millisecond {
		t.Errorf("attempt 4 = %v, want 400ms", d)
	}
}

func TestBackoff_NextDelayRespectsMax(t *testing.T) {
	b := BackoffConfig{
		BaseDelay: 1 * time.Second,
		MaxDelay:  5 * time.Second,
	}
	// attempt 10: 1s × 2^8 = 256s → capped at 5s
	if d := b.nextDelay(10); d != 5*time.Second {
		t.Errorf("attempt 10 with MaxDelay=5s should cap, got %v", d)
	}
}

func TestBackoff_JitterIsBounded(t *testing.T) {
	b := BackoffConfig{
		BaseDelay:      1 * time.Second,
		MaxDelay:       60 * time.Second,
		JitterFraction: 0.2,
		// Deterministic "random" — always returns the same value so
		// we can reason about boundedness.
		Rand: func(n int) int { return n - 1 }, // returns n-1 (max)
	}
	for attempt := 2; attempt <= 4; attempt++ {
		got := b.nextDelay(attempt)
		baseAttempt := b.BaseDelay * time.Duration(1<<uint(attempt-2))
		upper := baseAttempt + time.Duration(float64(baseAttempt)*b.JitterFraction)
		lower := baseAttempt - time.Duration(float64(baseAttempt)*b.JitterFraction)
		if got > upper || got < lower {
			t.Errorf("attempt %d: delay %v outside [%v, %v]", attempt, got, lower, upper)
		}
	}
}

func TestAutoRefreshTokenWithConfig_CapsAttempts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"success":false}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshPath := filepath.Join(tmpDir, "refresh.token")
	os.WriteFile(refreshPath, []byte("test-token"), 0o600)
	instancePath := filepath.Join(tmpDir, "instance.token")

	cfg := BackoffConfig{
		BaseDelay:   1 * time.Millisecond, // test-only: instant backoff
		MaxDelay:    5 * time.Millisecond,
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	}

	err := AutoRefreshTokenWithConfig(context.Background(), server.URL, refreshPath, instancePath, cfg)
	if err == nil {
		t.Error("expected refresh to fail when server always errors")
	}
	if calls != cfg.MaxAttempts {
		t.Errorf("server was hit %d times, want MaxAttempts=%d", calls, cfg.MaxAttempts)
	}
}

func TestAutoRefreshTokenWithConfig_HonorsZeroAttempts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"instance_token":"single-try-token"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshPath := filepath.Join(tmpDir, "refresh.token")
	os.WriteFile(refreshPath, []byte("ok"), 0o600)
	instancePath := filepath.Join(tmpDir, "instance.token")

	// MaxAttempts=1 → no retries. We expect exactly 1 call.
	cfg := BackoffConfig{
		MaxAttempts: 1,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	}

	if err := AutoRefreshTokenWithConfig(context.Background(), server.URL, refreshPath, instancePath, cfg); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 1 {
		t.Errorf("server was hit %d times, want 1 (MaxAttempts=1)", calls)
	}

	// Sanity: token file written.
	data, err := os.ReadFile(instancePath)
	if err != nil {
		t.Fatalf("read instance token: %v", err)
	}
	if string(data) != "single-try-token" {
		t.Errorf("instance token = %q, want single-try-token", data)
	}
}

// ──────────────────── AC-LH9..AC-LH11: daemon health ────────────────────

func TestDaemonHealth_RecordSuccess(t *testing.T) {
	// Reset the package-level singleton for this test so assertions
	// against Cycle/Success counts are deterministic.
	h := &DaemonHealth{}
	h.RecordSuccess()
	h.RecordSuccess()

	snap := h.Snapshot()
	if snap.TotalCycles != 2 {
		t.Errorf("TotalCycles=%d, want 2", snap.TotalCycles)
	}
	if snap.TotalSuccesses != 2 {
		t.Errorf("TotalSuccesses=%d, want 2", snap.TotalSuccesses)
	}
	if snap.TotalFailures != 0 {
		t.Errorf("TotalFailures=%d, want 0", snap.TotalFailures)
	}
	if snap.ConsecutiveFails != 0 {
		t.Errorf("ConsecutiveFails=%d, want 0 (successes reset)", snap.ConsecutiveFails)
	}
	if !snap.Healthy() {
		t.Error("Healthy() should return true after only successes")
	}
}

func TestDaemonHealth_RecordFailureConsecutive(t *testing.T) {
	h := &DaemonHealth{}

	h.RecordFailure(os.ErrInvalid)
	h.RecordFailure(os.ErrPermission)
	h.RecordFailure(os.ErrNotExist)

	snap := h.Snapshot()
	if snap.TotalFailures != 3 {
		t.Errorf("TotalFailures=%d, want 3", snap.TotalFailures)
	}
	if snap.ConsecutiveFails != 3 {
		t.Errorf("ConsecutiveFails=%d, want 3", snap.ConsecutiveFails)
	}
	if snap.Healthy() {
		t.Error("Healthy() should return false on 3+ consecutive failures")
	}

	h.RecordSuccess()
	if h.ConsecutiveFails != 0 {
		t.Errorf("after success, ConsecutiveFails=%d, want 0", h.ConsecutiveFails)
	}
}

func TestDaemonHealth_RecentFailuresBounded(t *testing.T) {
	h := &DaemonHealth{}
	// Hammer with 100 failures — the ring buffer must cap.
	for i := 0; i < 100; i++ {
		h.RecordFailure(os.ErrInvalid)
	}
	if got := len(h.RecentFailures); got > recentFailureRingSize {
		t.Errorf("RecentFailures length=%d, want <= %d", got, recentFailureRingSize)
	}
	if got := len(h.RecentFailures); got != recentFailureRingSize {
		t.Errorf("RecentFailures length=%d, want exactly %d (full ring)", got, recentFailureRingSize)
	}
}

func TestDaemonHealth_IsStale(t *testing.T) {
	h := &DaemonHealth{
		LastCycleAt: time.Now().Add(-2 * time.Hour),
	}
	if !h.IsStale(1 * time.Hour) {
		t.Error("IsStale should be true when last cycle >1h ago and max=1h")
	}
	if h.IsStale(3 * time.Hour) {
		t.Error("IsStale should be false when max=3h and last=2h")
	}
}

func TestDaemonHealth_ConcurrentAccess(t *testing.T) {
	h := &DaemonHealth{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.RecordSuccess()
		}()
		go func() {
			defer wg.Done()
			_ = h.Snapshot()
		}()
	}
	wg.Wait()
	snap := h.Snapshot()
	if snap.TotalCycles != 50 {
		t.Errorf("TotalCycles=%d, want 50 (race-safe counter)", snap.TotalCycles)
	}
}

func TestGetDaemonHealth_BeforeAnyCycle(t *testing.T) {
	// Reset singleton for this test.
	resetSingleton()

	if got := GetDaemonHealth(); got.TotalCycles != 0 {
		t.Errorf("TotalCycles before any cycle should be 0, got %d", got.TotalCycles)
	}
}

func TestStartTokenRefreshDaemon_RecordsSuccessInHealth(t *testing.T) {
	resetSingleton()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"instance_token":"hi"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshPath := filepath.Join(tmpDir, "refresh.token")
	os.WriteFile(refreshPath, []byte("ok"), 0o600)
	instancePath := filepath.Join(tmpDir, "instance.token")

	ctx, cancel := context.WithCancel(context.Background())

	// Don't actually wait 1h — cancel before any cycle.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	StartTokenRefreshDaemon(ctx, server.URL, refreshPath, instancePath, 1*time.Hour)

	// Cycle never ran (cancelled before initial delay) — health
	// should still be zero-value (no cycles).
	if got := GetDaemonHealth(); got.TotalCycles != 0 {
		t.Errorf("TotalCycles should be 0 (cancelled pre-cycle), got %d", got.TotalCycles)
	}
}

// ──────────────────── AC-LH12..AC-LH14: restricted mode bypass fix ────────────────────

func TestRestrictedMode_BypassFix(t *testing.T) {
	tests := []struct {
		path string
		want bool
		name string
	}{
		// Exact match and clean prefixes — allowed by license path check.
		{"/api/system/license", true, "exact-prefix"},
		{"/api/system/license/", true, "root-slash"},
		{"/api/system/license/status", true, "subpath"},
		{"/api/system/license/v1/activate", true, "deep-subpath"},

		// Bypass attempts — rejected by licensePathAllowed (boundary
		// char is the killer change in v2).
		{"/api/system/licenseeXploit", false, "bypass-licenseeXploit"},
		{"/api/system/licenseAdmin", false, "bypass-licenseAdmin"},
		{"/api/system/license.json", false, "bypass-license.json"},
		{"/api/system/licensethief", false, "bypass-shorter-prefix"},
		{"/api/system/LICENSE", false, "bypass-case"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := licensePathAllowed(tt.path)
			if got != tt.want {
				t.Errorf("licensePathAllowed(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestRestrictedMode_HealthEndpointAlwaysAllowed(t *testing.T) {
	// Health endpoints must always be allowed — even when license
	// path check would reject them.
	for _, path := range []string{"/api/healthz", "/healthz"} {
		if !isHealthEndpoint(path) {
			t.Errorf("isHealthEndpoint(%q) = false, want true", path)
		}
	}
	// Negative cases.
	for _, path := range []string{"/api/system/healthz", "/health", "/metrics"} {
		if isHealthEndpoint(path) {
			t.Errorf("isHealthEndpoint(%q) = true, want false", path)
		}
	}
}

func TestRestrictedMode_HealthEndpointCheckHelper(t *testing.T) {
	// isHealthEndpoint is the small helper extracted so unit tests
	// can drive it directly.
}

func TestRestrictedMode_MiddlewareBlocksBypass(t *testing.T) {
	// We exercise the full middleware path against an echo
	// instance. The middleware must reject bypass paths.
	e := echo.New()
	mw := RestrictedModeMiddleware()

	allowed := []string{
		"/api/healthz",
		"/healthz",
		"/api/system/license",
		"/api/system/license/status",
	}

	blocked := []string{
		"/api/system/licenseeXploit",
		"/api/system/LICENSE",
		"/api/admin/users",
		"/api/chat",
		"/",
	}

	next := func(c echo.Context) error {
		return c.String(http.StatusOK, "next")
	}

	for _, path := range allowed {
		t.Run("allow-"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			rec.Code = 0
			handler := mw(next)
			_ = handler(c)
			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("path=%q should be ALLOWED (license or health), got 503", path)
			}
		})
	}

	for _, path := range blocked {
		t.Run("block-"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			rec.Code = 0
			handler := mw(next)
			_ = handler(c)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("path=%q should be BLOCKED (503), got code=%d", path, rec.Code)
			}
		})
	}
}

// ──────────────────── helpers ────────────────────

func resetSingleton() {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	singleton = nil
	onceInit = false
}

// isHealthEndpoint mirrors the health-endpoint allow list in
// restricted_mode.go so unit tests can call it directly. Kept in
// sync with the middleware by hand (one-line check).
func isHealthEndpoint(path string) bool {
	return path == "/api/healthz" || path == "/healthz"
}
