package admin

// Regression pin for the 2026-09-11 verification-round audit: the provider
// model list silently truncated at the first never-matched offer.
//
// Root cause: the canonical-name column was
// COALESCE(NULLIF(mc.canonical_name,''), mo.standardized_name) — NULL for an
// offer whose canonical_id AND standardized_name are both NULL (the natural
// state of a freshly discovered, unmatched raw model, i.e. exactly the rows
// the matching drawer exists to fix). Scanned into a non-pointer string, that
// NULL errored the Scan and pgx v5 marks the result set dead, so every row
// after the first bad one vanished from the admin UI with only a WARN in the
// logs.
//
// The fix appends a final '' fallback to the COALESCE. The ExpectQuery
// regexp below pins that fallback: reverting the SQL fails the expectation
// (pgxmock reports an unmatched query → the handler 500s → the test fails).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// canonicalNameColumnRegexp is the fixed COALESCE expression; the trailing
// ” fallback is the regression under test.
const canonicalNameColumnRegexp = `COALESCE\(NULLIF\(mc\.canonical_name,''\), mo\.standardized_name, ''\)`

func providerModelsListRows() *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{
		"id", "credential_id", "credential_label",
		"raw_model_name", "standardized_name",
		"canonical_id", "display_name",
		"available", "unavailable_reason", "unavailable_at",
		"p95_latency_ms", "success_rate",
		"input_price", "output_price",
		"last_seen_at", "routing_tier",
		"standard_iq",
		"overall_score", "avg_score",
		"sample_count", "tested_at",
		"canonical_name",
		"context_window", "context_window_override",
	})
	// Matched offer: canonical link present (canonical_id is scanned through a
	// *int; pgxmock only supports NULL through that double pointer, and the
	// assertions below do not depend on row 1's id value).
	rows.AddRow(int64(3176891), int64(77), "apinext-1",
		"claude/opus-5", "opus-5",
		nil, "",
		true, nil, nil,
		nil, nil,
		nil, nil,
		nil, "2",
		nil,
		nil, nil,
		0, nil,
		"opus-5",
		nil, nil)
	// Never-matched offer: canonical_id NULL AND standardized_name NULL.
	// The fixed SQL coalesces the display column to '' — the exact shape the
	// old query NULLed and the old scan choked on.
	rows.AddRow(int64(3201426), int64(77), "apinext-1",
		"cluade/opus-5", nil,
		nil, "",
		true, nil, nil,
		nil, nil,
		nil, nil,
		nil, "2",
		nil,
		nil, nil,
		0, nil,
		"",
		nil, nil)
	return rows
}

type modelOfferListEntry struct {
	ID               int    `json:"id"`
	RawModelName     string `json:"raw_model_name"`
	StandardizedName string `json:"standardized_name"`
	CanonicalID      *int   `json:"canonical_id"`
	CanonicalName    string `json:"canonical_name"`
}

func TestGetProviderModels_NeverMatchedOfferNoTruncation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(canonicalNameColumnRegexp).
		WithArgs(36994).
		WillReturnRows(providerModelsListRows())

	rr := rec()
	serveGetProviderModels(rr, newReq(http.MethodGet, "/api/providers/36994/models", nil), mock, 36994)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var offers []modelOfferListEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &offers); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(offers) != 2 {
		t.Fatalf("offers=%d, want 2 (the never-matched row must not truncate the list)", len(offers))
	}
	last := offers[1]
	if last.RawModelName != "cluade/opus-5" {
		t.Fatalf("offers[1].raw_model_name=%q, want cluade/opus-5", last.RawModelName)
	}
	if last.CanonicalID != nil {
		t.Fatalf("offers[1].canonical_id=%v, want nil", *last.CanonicalID)
	}
	if last.CanonicalName != "" {
		t.Fatalf("offers[1].canonical_name=%q, want empty string (never matched)", last.CanonicalName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The credential-scoped list shares the offerListSQLColumns DTO; pin the same
// fallback there so the two lists cannot drift apart again.
func TestOfferListSQL_HasCanonicalNameEmptyFallback(t *testing.T) {
	fixed := "COALESCE(NULLIF(mc.canonical_name,''), mo.standardized_name, '')"
	if !strings.Contains(offerListSQLColumns, fixed) {
		t.Fatalf("offerListSQLColumns lost the canonical-name '' fallback:\n%s", offerListSQLColumns)
	}
}
