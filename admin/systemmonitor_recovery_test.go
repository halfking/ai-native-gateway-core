// Package admin — systemmonitor_recovery_test.go
//
// Unit tests for the audit follow-up #5 admin endpoint
// GET /api/admin/system-monitor/recovery. We stub the
// SystemMonitorBackend interface (no real Redis, no real PG) to
// verify the JSON shape, nil-monitor handling, and method-not-allowed
// paths.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
)

// stubSystemMonitorBackend is a minimal SystemMonitorBackend
// implementation for unit tests. We override only the methods the
// recovery handler touches (RecoveryStats + IsFallback) and stub
// the rest as no-ops so the test compiles.
type stubSystemMonitorBackend struct {
	recoveryStats systemmonitor.RecoveryStats
	inFallback    bool
}

func (s *stubSystemMonitorBackend) Submit(_ context.Context, _ *SystemMonitorTask) (int64, error) {
	return 0, nil
}

func (s *stubSystemMonitorBackend) QueueStats(_ context.Context) (SystemMonitorQueueStats, error) {
	return SystemMonitorQueueStats{}, nil
}

func (s *stubSystemMonitorBackend) IsFallback() bool { return s.inFallback }

func (s *stubSystemMonitorBackend) GetMetricsCollector() interface{} { return nil }

func (s *stubSystemMonitorBackend) RecoveryStats() SystemMonitorRecoveryStats {
	return SystemMonitorRecoveryStats{
		LastError:            s.recoveryStats.LastError,
		LastErrorAt:          s.recoveryStats.LastErrorAt,
		LastRecoveryAt:       s.recoveryStats.LastRecoveryAt,
		LastRecoveryKeyCount: s.recoveryStats.LastRecoveryKeyCount,
		ConsecutiveFailures:  s.recoveryStats.ConsecutiveFailures,
		FailThreshold:        s.recoveryStats.FailThreshold,
	}
}

// TestSystemMonitorRecoveryHandler_ReturnsSnapshot verifies the
// happy path: stub returns a populated snapshot, handler emits the
// expected JSON shape.
func TestSystemMonitorRecoveryHandler_ReturnsSnapshot(t *testing.T) {
	now := time.Now().UTC()
	h := NewHandler(nil, "", nil)
	backend := &stubSystemMonitorBackend{
		recoveryStats: systemmonitor.RecoveryStats{
			LastError:            "redis unreachable",
			LastErrorAt:          now.Add(-2 * time.Minute),
			LastRecoveryAt:       now.Add(-1 * time.Hour),
			LastRecoveryKeyCount: 42,
			ConsecutiveFailures:  2,
			FailThreshold:        3,
		},
		inFallback: true,
	}
	h.SetSystemMonitor(backend)

	mux := http.NewServeMux()
	h.RegisterSystemMonitorRoutes(mux, identityWrap, identityWrap)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/system-monitor/recovery", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := resp["last_error"]; got != "redis unreachable" {
		t.Fatalf("last_error = %v, want redis unreachable", got)
	}
	if got := resp["last_recovery_key_count"]; int(got.(float64)) != 42 {
		t.Fatalf("last_recovery_key_count = %v, want 42", got)
	}
	if got := resp["consecutive_failures"]; int(got.(float64)) != 2 {
		t.Fatalf("consecutive_failures = %v, want 2", got)
	}
	if got := resp["fail_threshold"]; int(got.(float64)) != 3 {
		t.Fatalf("fail_threshold = %v, want 3", got)
	}
	if got := resp["in_fallback"]; got != true {
		t.Fatalf("in_fallback = %v, want true", got)
	}
	if _, ok := resp["snapshot_at"]; !ok {
		t.Fatalf("snapshot_at missing from response")
	}
}

// TestSystemMonitorRecoveryHandler_NilMonitor verifies the 503 path:
// when SetSystemMonitor was never called (or called with nil), the
// handler returns 503 instead of panicking.
func TestSystemMonitorRecoveryHandler_NilMonitor(t *testing.T) {
	h := NewHandler(nil, "", nil)
	mux := http.NewServeMux()
	h.RegisterSystemMonitorRoutes(mux, identityWrap, identityWrap)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/system-monitor/recovery", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestSystemMonitorRecoveryHandler_MethodNotAllowed verifies the
// handler rejects non-GET methods with 405.
func TestSystemMonitorRecoveryHandler_MethodNotAllowed(t *testing.T) {
	h := NewHandler(nil, "", nil)
	h.SetSystemMonitor(&stubSystemMonitorBackend{})

	mux := http.NewServeMux()
	h.RegisterSystemMonitorRoutes(mux, identityWrap, identityWrap)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/system-monitor/recovery", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
