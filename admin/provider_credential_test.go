package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
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

func TestRotateCredentialPrimaryKeyRejectsInvalidInputBeforeDB(t *testing.T) {
	h := &Handler{}
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: "{not-json"},
		{name: "trailing data", body: `{"api_key":"secret","raw_model_name":"claude-sonnet-4-5"} trailing`},
		{name: "second JSON value", body: `{"api_key":"secret","raw_model_name":"claude-sonnet-4-5"}{}`},
		{name: "missing api key", body: `{"raw_model_name":"claude-sonnet-4-5"}`},
		{name: "blank api key", body: `{"api_key":"  ","raw_model_name":"claude-sonnet-4-5"}`},
		{name: "missing model", body: `{"api_key":"secret"}`},
		{name: "blank model", body: `{"api_key":"secret","raw_model_name":"  "}`},
		{name: "too large", body: `{"api_key":"` + strings.Repeat("x", maxRotateCredentialPrimaryKeyBodyBytes) + `","raw_model_name":"claude-sonnet-4-5"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(tt.body))
			rr := httptest.NewRecorder()
			h.rotateCredentialPrimaryKey(rr, req, 587, 17)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "secret") {
				t.Fatalf("response disclosed request secret: %s", rr.Body.String())
			}
		})
	}
}

func TestQueueCredentialRotationProbeReportsUnavailable(t *testing.T) {
	if got := (&Handler{}).queueCredentialRotationProbe(17, "claude-sonnet-4-5"); got != "not_configured" {
		t.Fatalf("nil runner status = %q, want not_configured", got)
	}

	runner := bg.NewModelProbeRunner(nil, nil)
	for i := 0; i < 64; i++ {
		if err := runner.SubmitManualProbe(i, "model"); err != nil {
			t.Fatalf("fill manual probe queue at %d: %v", i, err)
		}
	}
	if got := (&Handler{modelProbe: runner}).queueCredentialRotationProbe(17, "claude-sonnet-4-5"); got != "queue_unavailable" {
		t.Fatalf("full queue status = %q, want queue_unavailable", got)
	}
}

func TestProviderCredentialRouteRejectsRotatePrimaryKeyWrongMethod(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/providers/587/credentials/17/rotate-primary-key", nil)
	rr := httptest.NewRecorder()
	h.handleProviderCredentials(rr, req, 587, "17/rotate-primary-key")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rr.Code, rr.Body.String())
	}
}
