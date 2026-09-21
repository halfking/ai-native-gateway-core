package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
)

func TestCreateProviderOffer_RequiresCredentialID(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/providers/1/models/", bytes.NewReader([]byte(`{}`)))
	h.createProviderOffer(rr, req, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestCreateProviderOffer_RejectsMissingCredentialIDEvenWithRaw(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/providers/1/models/",
		bytes.NewReader([]byte(`{"raw_model_name":"gpt-4o"}`)))
	h.createProviderOffer(rr, req, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
}

// TestPostProviderModels_NoTrailingSlash_ReturnsMethodNotAllowed locks down
// the 2026-08-23 fix for /api/providers/{id}/models (no trailing slash).
//
// Background: the dispatcher used to alias POST→GET, which routed the request
// into getProviderModels — the same SELECT that 500s on environments missing
// migration 361 (`provider_models.source`). With the front-end getOrPost
// helper also falling back from GET to POST on the first failure, the user
// observed the failure as repeated `POST /api/providers/14/models 500` even
// though they never intended to send a POST.
//
// We bypass super_admin by calling handleProviders directly. We hand a
// zero-value *pgxpool.Pool (matching the convention in routing_reset_test.go
// for body-validation paths) so the dispatcher's db-nil guard passes through
// without firing an unrelated 503.
func TestPostProviderModels_NoTrailingSlash_ReturnsMethodNotAllowed(t *testing.T) {
	h := &Handler{db: &pgxpool.Pool{}}

	// POST on the no-trailing-slash path must be rejected at the dispatcher.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/providers/14/models",
		bytes.NewReader([]byte(`{"raw_model_name":"x"}`)))
	h.handleProviders(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/providers/14/models: got %d, want 405. body=%s",
			rr.Code, rr.Body.String())
	}

	// Trailing-slash variant still routes into createProviderOffer and still
	// rejects missing credential_id with 400 (not 405).
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost,
		"/api/providers/14/models/",
		bytes.NewReader([]byte(`{"raw_model_name":"x"}`)))
	h.handleProviders(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/providers/14/models/: got %d, want 400. body=%s",
			rr2.Code, rr2.Body.String())
	}
}

// credentialModelsListRows mirrors the full 30-column offerListSQLColumns
// shape — the shared DTO the credential-scoped list scans through
// scanModelOfferDTO (provider_models_test.go's 24-column helper only covers
// the older provider-wide inline query).
func credentialModelsListRows() *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{
		"id", "credential_id", "credential_label",
		"raw_model_name", "standardized_name",
		"canonical_id", "outbound_model_name",
		"available", "unavailable_reason", "unavailable_at",
		"p95_latency_ms", "success_rate",
		"input_price", "output_price", "last_seen_at",
		"routing_tier", "standard_iq", "overall_score",
		"avg_score", "sample_count", "tested_at",
		"canonical_name", "context_window", "context_window_override",
		"modality", "multimodal_caps", "reasoning_caps",
		"canonical_status", "admin_protected", "source",
	})
	rows.AddRow(int64(3176891), int64(77), "apinext-1",
		"claude/opus-5", "opus-5",
		nil, "claude/opus-5",
		true, nil, nil,
		nil, nil, nil, nil, nil,
		"2", nil, nil,
		nil, 0, nil,
		"opus-5", nil, nil,
		"text", nil, nil,
		"active", false, "")
	return rows
}

// TestServeListCredentialModels_StaleViewWithoutProviderModality_Returns200
// reproduces the 2026-09-11 live failure on the local upgraded install:
// model_offers there was last rebuilt by migration 678, whose view body has
// no `provider_modality` alias, so the credential-scoped list answered
// 500 "column mo.provider_modality does not exist" (SQLSTATE 42703) and the
// admin Credentials→模型 panel rendered empty. The fix probes the column at
// startup and falls back to the canonical-modality-only SQL; on this mock the
// probe has no expectation, errors, and the compat SQL is exactly what
// reaches ExpectQuery — the production stale-view shape.
func TestServeListCredentialModels_StaleViewWithoutProviderModality_Returns200(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(canonicalNameColumnRegexp).
		WithArgs(77).
		WillReturnRows(credentialModelsListRows())

	rr := rec()
	serveListCredentialModels(rr, context.Background(), mock, 77)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var offers []modelOfferDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &offers); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(offers) != 1 {
		t.Fatalf("offers=%d, want 1", len(offers))
	}
	if offers[0].RawModelName != "claude/opus-5" {
		t.Fatalf("raw_model_name=%q, want claude/opus-5", offers[0].RawModelName)
	}
	if offers[0].Modality != "text" {
		t.Fatalf("modality=%q, want text (compat fallback)", offers[0].Modality)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
