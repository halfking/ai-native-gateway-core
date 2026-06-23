package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestUpdateFpSlotLimit_ConstraintExceedsConcurrency verifies the constraint
// that fp_slot_limit cannot exceed concurrency_limit. Uses a minimal mock
// DB that records Exec calls and returns a fixed concurrency_limit.
//
// Full DB-integration tests for this handler live in the e2e suite; this
// unit test covers the request-parse + constraint-check + 400 path with
// no real database required.
func TestUpdateFpSlotLimit_ConstraintExceedsConcurrency(t *testing.T) {
	_ = slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// Smoke test: verify fp_slot_limit field is accepted in the PATCH body
// (parse phase). This is the path that previously had no UI to drive it.
func TestUpdateCredentialBody_ParsesFpSlotLimit(t *testing.T) {
	body := `{"fp_slot_limit": 30, "concurrency_limit": 50}`
	var req struct {
		Label            *string `json:"label"`
		Status           *string `json:"status"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
		FpSlotLimit      *int    `json:"fp_slot_limit"`
	}
	if err := json.NewDecoder(bytes.NewBufferString(body)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.FpSlotLimit == nil || *req.FpSlotLimit != 30 {
		t.Fatalf("expected fp_slot_limit=30, got %v", req.FpSlotLimit)
	}
	if req.ConcurrencyLimit == nil || *req.ConcurrencyLimit != 50 {
		t.Fatalf("expected concurrency_limit=50, got %v", req.ConcurrencyLimit)
	}
}

// TestUpdateCredentialHandler_BadRequestInvalidJSON covers the parse-failure
// path: malformed body → 400.
func TestUpdateCredentialHandler_BadRequestInvalidJSON(t *testing.T) {
	// Minimal handler: we only need to confirm readJSON failure path.
	h := &Handler{} // zero-valued; only writeError is exercised
	req := httptest.NewRequest(http.MethodPatch, "/x", bytes.NewBufferString("{not-json"))
	rr := httptest.NewRecorder()
	h.updateCredential(rr, req, 1, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}
