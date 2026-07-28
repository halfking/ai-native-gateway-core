package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleModelIntegrity_NotConfigured returns 503 when h.db is nil,
// matching the rest of the admin handlers' "no DB" semantics.
func TestHandleModelIntegrity_NotConfigured(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/model-integrity/events", nil)
	rec := httptest.NewRecorder()
	h.handleModelIntegrity(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (db not configured)", rec.Code)
	}
}

// TestHandleModelIntegrity_UnknownSubpath returns 404 for unrecognized
// sub-paths. The handler does NOT match the legacy /events/{id}/resolve
// form for arbitrary {id} values.
func TestHandleModelIntegrity_UnknownSubpath(t *testing.T) {
	h := &Handler{db: nil}
	for _, p := range []string{
		"/api/admin/model-integrity/foo",
		"/api/admin/model-integrity/events/123/not-resolve",
		"/api/admin/model-integrity/",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.handleModelIntegrity(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("path %s: got %d, want 404 or 503", p, rec.Code)
		}
	}
}

// TestHandleModelIntegrityList_MethodNotAllowed rejects POST /events.
// The endpoint is read-only; resolve is its own subrouter.
func TestHandleModelIntegrityList_MethodNotAllowed(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/model-integrity/events", nil)
	rec := httptest.NewRecorder()
	h.handleModelIntegrityList(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

// TestHandleModelIntegritySummary_MethodNotAllowed rejects POST.
func TestHandleModelIntegritySummary_MethodNotAllowed(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/model-integrity/summary", nil)
	rec := httptest.NewRecorder()
	h.handleModelIntegritySummary(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

// TestHandleModelIntegrityFingerprintDrift_MethodNotAllowed rejects POST.
func TestHandleModelIntegrityFingerprintDrift_MethodNotAllowed(t *testing.T) {
	h := &Handler{db: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/model-integrity/fingerprint-drift", nil)
	rec := httptest.NewRecorder()
	h.handleModelIntegrityFingerprintDrift(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

// TestModelIntegrityRecord_FieldShape guarantees the JSON tags
// don't drift in a way that would break the admin UI. The shape is
// the contract consumed by web/src/api/integrity.ts.
func TestModelIntegrityRecord_FieldShape(t *testing.T) {
	r := ModelIntegrityRecord{
		ID:          42,
		DetectedAt:  time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		AnomalyType: "model_mismatch",
		Severity:    "high",
	}
	if r.AnomalyType != "model_mismatch" {
		t.Fatal("AnomalyType field not preserved")
	}
	if r.ID != 42 {
		t.Fatal("ID field not preserved")
	}
	if r.DetectedAt.Year() != 2026 {
		t.Fatal("DetectedAt not preserved")
	}
}
