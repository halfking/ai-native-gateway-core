package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubScanSchedulerStatus is a test double for the ScanSchedulerStatusProvider
// interface. bg.ScanScheduler is exercised in bg/scan_scheduler_test.go.
type stubScanSchedulerStatus struct {
	snap any
}

func (s stubScanSchedulerStatus) Status() any { return s.snap }

func TestScanSchedulerStatus_503WhenNotWired(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleFreeDiscoveryScanSchedulerStatus(rec, httptest.NewRequest(http.MethodGet, "/api/free-discovery/scan-scheduler/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no probe wired: want 503, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestScanSchedulerStatus_OK(t *testing.T) {
	snap := map[string]any{
		"enabled":       true,
		"interval":       "6h0m0s",
		"last_sweep_at":  "2026-09-14T00:00:00Z",
		"sweeps_total":   3,
		"scans_total":    42,
		"scans_failed":   1,
		"scans_skipped":  0,
	}
	h := &Handler{}
	h.SetScanSchedulerStatus(stubScanSchedulerStatus{snap: snap})

	rec := httptest.NewRecorder()
	h.handleFreeDiscoveryScanSchedulerStatus(rec, httptest.NewRequest(http.MethodGet, "/api/free-discovery/scan-scheduler/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != true {
		t.Errorf("enabled = %v, want true", got["enabled"])
	}
	if got["interval"] != "6h0m0s" {
		t.Errorf("interval = %v, want 6h0m0s", got["interval"])
	}
	if got["sweeps_total"] != float64(3) {
		t.Errorf("sweeps_total = %v, want 3", got["sweeps_total"])
	}
}
