package admin

// The getModelOfferSuggestions / updateModelOffer tests at the bottom of
// this file exercise the DB through the offerQuerier seam with pgxmock.
// 需 cgo 环境无法在 win-arm64 运行（admin → autoroute → routingopt →
// github.com/yalue/onnxruntime_go 强制依赖 cgo，本机无 C 工具链），确保
// go vet 通过即可；测试在 Mac/Linux CI 上执行：go test ./admin/ -run 'TestModelOffer'.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// TestSetCredentialManualDisabled_EmptyReason verifies the legacy endpoint
// (PATCH /api/providers/{pid}/credentials/{cid}/manual-disabled) refuses
// empty / whitespace-only reasons with 400. This is the fix for the
// 2026-06-23 incident where admin toggled minimax-prod-1 to manual_disabled=true
// from the Provider Detail page with an empty reason — the audit log row
// (model_offer_events.reason_detail="admin: ") had no business context and
// downstream investigation was much harder.
//
// The new unified endpoint handleSetManualDisabled already enforces this
// (see credential_monitor.go handleSetManualDisabled). This test ensures
// the legacy 900-series endpoint catches up.
func TestSetCredentialManualDisabled_EmptyReason(t *testing.T) {
	h := &Handler{} // nil DB; the reason guard must run before any SQL.
	cases := []struct {
		name   string
		reason string
	}{
		{"empty", ""},
		{"whitespace-only spaces", "   "},
		{"whitespace-only tabs+newlines", "\t\n  \r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"manual_disabled": true,
				"reason":          tc.reason,
			})
			req := newReq(http.MethodPatch,
				"/api/providers/14/credentials/6/manual-disabled",
				strings.NewReader(string(body)))
			req.Header.Set("X-Admin-User", "test-admin")
			rr := rec()
			h.setCredentialManualDisabled(rr, req, 14, 6)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for empty reason, got %d body=%s",
					rr.Code, rr.Body.String())
			}
			if !strings.Contains(strings.ToLower(rr.Body.String()), "reason") {
				t.Fatalf("expected error body to mention 'reason', got %s",
					rr.Body.String())
			}
		})
	}
}

// TestSetProviderManualDisabled_EmptyReason is the provider-level counterpart.
// providers.manual_disabled gates routing for all credentials under that
// provider; an accidental click without a reason is even more dangerous than
// per-credential, so the validation must apply here too.
func TestSetProviderManualDisabled_EmptyReason(t *testing.T) {
	h := &Handler{}
	body, _ := json.Marshal(map[string]any{
		"manual_disabled": true,
		"reason":          "",
	})
	req := newReq(http.MethodPatch,
		"/api/providers/14/manual-disabled",
		strings.NewReader(string(body)))
	req.Header.Set("X-Admin-User", "test-admin")
	rr := rec()
	h.setProviderManualDisabled(rr, req, 14)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty reason, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestSetCredentialManualDisabled_ValidReasonStillPassesBeforeDB
// documents that the reason guard is the FIRST check (so a valid reason
// with a nil DB still returns 500/NotFound, not 400). This locks in the
// pre-DB validation order: reason → body-parse → DB.
//
// Note: this test does NOT cover the happy path end-to-end (that requires
// a real DB and is exercised in integration tests). It only pins the
// validation order so future refactors can't accidentally move the reason
// check behind a DB call.
func TestSetCredentialManualDisabled_ValidReasonStillPassesBeforeDB(t *testing.T) {
	h := &Handler{}
	body, _ := json.Marshal(map[string]any{
		"manual_disabled": true,
		"reason":          "test: unit-test reason",
	})
	req := newReq(http.MethodPatch,
		"/api/providers/14/credentials/6/manual-disabled",
		strings.NewReader(string(body)))
	rr := rec()
	// nil DB → DB.Exec panics. We recover so the test can assert on rr.Code.
	// The point of the test is that rr.Code is NOT 400 (reason guard didn't
	// fire) — the panic itself is acceptable because it means we got past
	// validation and hit the DB call.
	defer func() {
		if r := recover(); r != nil {
			// expected: nil DB caused panic. Confirm rr is still 0
			// (i.e. the handler never wrote a 400 to the recorder).
			if rr.Code == http.StatusBadRequest {
				t.Fatalf("valid reason must not return 400, got %d body=%s",
					rr.Code, rr.Body.String())
			}
		}
	}()
	h.setCredentialManualDisabled(rr, req, 14, 6)
	if rr.Code == http.StatusBadRequest {
		t.Fatalf("valid reason must not return 400, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

// ===========================================================================
// getModelOfferSuggestions / updateModelOffer (pgxmock, 2026-09-11 audit)
// ===========================================================================

type offerMatchJSON struct {
	ID            int     `json:"id"`
	CanonicalName string  `json:"canonical_name"`
	DisplayName   string  `json:"display_name"`
	Family        string  `json:"family"`
	Score         float64 `json:"score"`
}

type suggestionsResponseJSON struct {
	OfferID              int              `json:"offer_id"`
	RawModelName         string           `json:"raw_model_name"`
	RuleBased            string           `json:"rule_based"`
	SuggestedCanonicalID int              `json:"suggested_canonical_id"`
	CanonicalCleared     bool             `json:"canonical_cleared"`
	Matches              []offerMatchJSON `json:"matches"`
	CanonicalOptions     []struct {
		ID            int    `json:"id"`
		CanonicalName string `json:"canonical_name"`
		DisplayName   string `json:"display_name"`
		Family        string `json:"family"`
	} `json:"canonical_options"`
}

// suggestionLookupRows shapes the offers lookup of
// serveModelOfferSuggestions (migration 693): the raw name plus whether the
// operator admin-unbound the offer (provider_models.canonical_cleared_at
// IS NOT NULL).
func suggestionLookupRows(rawName string, cleared bool) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"raw_model_name", "canonical_cleared"}).AddRow(rawName, cleared)
}

func suggestionsCatalogRows(ids []int, names []string) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "canonical_name", "display_name", "family"})
	for i := range ids {
		rows = rows.AddRow(ids[i], names[i], names[i], "anthropic-claude")
	}
	return rows
}

func decodeSuggestionsResponse(t *testing.T, rec *httptest.ResponseRecorder) suggestionsResponseJSON {
	t.Helper()
	var resp suggestionsResponseJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return resp
}

// "cluade/opus-5" must suggest the existing standard model claude-opus-5:
// suggested_canonical_id non-zero, rule_based switched to the matched
// standard name, and the match present with a score ≥ AutoLinkThreshold.
func TestModelOfferSuggestions_ConfidentMatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(suggestionLookupRows("cluade/opus-5", false))
	mock.ExpectQuery(`FROM models_canonical`).
		WillReturnRows(suggestionsCatalogRows(
			// 2026-09-11 audit fix: the pairing was swapped — id 7 zipped to
			// "claude-haiku-4.5" while the assertions below expect id 7 to BE
			// claude-opus-5, so this test could never pass (the author's
			// win-arm64 environment cannot run these cgo-dependent tests).
			[]int{7, 8},
			[]string{"claude-opus-5", "claude-haiku-4.5"},
		))

	rec := rec()
	serveModelOfferSuggestions(rec, newReq(http.MethodGet, "/api/providers/2/offers/301/suggestions", nil), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeSuggestionsResponse(t, rec)
	if resp.SuggestedCanonicalID != 7 {
		t.Fatalf("suggested_canonical_id=%d, want 7 (claude-opus-5)", resp.SuggestedCanonicalID)
	}
	if resp.RuleBased != "claude-opus-5" {
		t.Fatalf("rule_based=%q, want the matched standard name", resp.RuleBased)
	}
	if len(resp.Matches) == 0 || resp.Matches[0].CanonicalName != "claude-opus-5" {
		t.Fatalf("matches[0]=%+v, want claude-opus-5 first", resp.Matches)
	}
	if resp.Matches[0].Score < 0.85 {
		t.Fatalf("top score=%f, want ≥ AutoLinkThreshold 0.85", resp.Matches[0].Score)
	}
	if len(resp.CanonicalOptions) != 2 {
		t.Fatalf("canonical_options=%d, want the full catalog echo (2)", len(resp.CanonicalOptions))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Nothing scores above AutoLinkThreshold → suggested_canonical_id stays 0
// and rule_based keeps its legacy meaning: the NormalizeRouteKey string of
// the raw name.
func TestModelOfferSuggestions_NoConfidentMatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	rawName := "totally-unknown-widget"
	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(suggestionLookupRows(rawName, false))
	mock.ExpectQuery(`FROM models_canonical`).
		WillReturnRows(suggestionsCatalogRows([]int{7}, []string{"claude-opus-5"}))

	rec := rec()
	serveModelOfferSuggestions(rec, newReq(http.MethodGet, "/api/providers/2/offers/301/suggestions", nil), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeSuggestionsResponse(t, rec)
	if resp.SuggestedCanonicalID != 0 {
		t.Fatalf("suggested_canonical_id=%d, want 0", resp.SuggestedCanonicalID)
	}
	if resp.RuleBased != modelname.NormalizeRouteKey(rawName) {
		t.Fatalf("rule_based=%q, want legacy NormalizeRouteKey form %q",
			resp.RuleBased, modelname.NormalizeRouteKey(rawName))
	}
	if len(resp.Matches) != 0 {
		t.Fatalf("matches=%+v, want none above the score floor", resp.Matches)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Ten catalog entries score above the match floor for
// "anthropic/claude-opus-5"; the payload must rank them best-first and cap
// matches at 8. The exact-match row claude-opus-5 tops the list.
func TestModelOfferSuggestions_MatchesSortedCappedAt8(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	ids := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	names := []string{
		"claude", "claude-fast", "claude-haiku", "claude-opus",
		"claude-opus-4", "claude-opus-5", "claude-opus-5-thinking",
		"claude-pro", "claude-sonnet", "claude-sonnet-5",
	}

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(suggestionLookupRows("anthropic/claude-opus-5", false))
	mock.ExpectQuery(`FROM models_canonical`).
		WillReturnRows(suggestionsCatalogRows(ids, names))

	rec := rec()
	serveModelOfferSuggestions(rec, newReq(http.MethodGet, "/api/providers/2/offers/301/suggestions", nil), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeSuggestionsResponse(t, rec)
	if resp.SuggestedCanonicalID != 6 {
		t.Fatalf("suggested_canonical_id=%d, want 6 (claude-opus-5)", resp.SuggestedCanonicalID)
	}
	if len(resp.Matches) != 8 {
		t.Fatalf("matches=%d, want capped at 8", len(resp.Matches))
	}
	if resp.Matches[0].CanonicalName != "claude-opus-5" {
		t.Fatalf("matches[0]=%s, want claude-opus-5", resp.Matches[0].CanonicalName)
	}
	for i := 1; i < len(resp.Matches); i++ {
		if resp.Matches[i-1].Score < resp.Matches[i].Score {
			t.Fatalf("matches not score-ordered at %d: %f < %f",
				i, resp.Matches[i-1].Score, resp.Matches[i].Score)
		}
	}
	seen := map[string]bool{}
	for _, m := range resp.Matches {
		if seen[m.CanonicalName] {
			t.Fatalf("duplicate match %s", m.CanonicalName)
		}
		seen[m.CanonicalName] = true
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func offerLookupRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "raw_model_name"}).AddRow(301, "some-raw")
}

// Migration 693: an admin-unbound offer (canonical_cleared_at set) gets no
// auto-suggestion — suggesting the very model the operator unlinked invites
// a one-click undo of that decision. suggested_canonical_id falls back to 0
// and canonical_cleared flags why, while the ranked matches are still
// returned so the operator keeps manual re-link options.
func TestModelOfferSuggestions_ClearedOfferSuppressesSuggestion(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(suggestionLookupRows("cluade/opus-5", true))
	mock.ExpectQuery(`FROM models_canonical`).
		WillReturnRows(suggestionsCatalogRows(
			[]int{7, 8},
			[]string{"claude-haiku-4.5", "claude-opus-5"},
		))

	rec := rec()
	serveModelOfferSuggestions(rec, newReq(http.MethodGet, "/api/providers/2/offers/301/suggestions", nil), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeSuggestionsResponse(t, rec)
	if !resp.CanonicalCleared {
		t.Fatal("canonical_cleared=false, want true for an admin-unbound offer")
	}
	if resp.SuggestedCanonicalID != 0 {
		t.Fatalf("suggested_canonical_id=%d, want 0 for an admin-unbound offer (the unlink must not be auto-suggested back)",
			resp.SuggestedCanonicalID)
	}
	if resp.RuleBased != modelname.NormalizeRouteKey("cluade/opus-5") {
		t.Fatalf("rule_based=%q, want the legacy NormalizeRouteKey form (no matched-name promotion)",
			resp.RuleBased)
	}
	if len(resp.Matches) == 0 {
		t.Fatal("matches empty, want the ranked list preserved for a manual re-link")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// offerResultRows shapes the final SELECT the handler uses to render the
// response (13 columns; NULLs for untouched fields).
//
// billing_mode stays NULL on purpose: pgxmock v4 cannot deliver ANY non-NULL
// value into a **T destination (its row Scan has no reflection path for
// pointer-to-pointer — verified 2026-09-11), so a non-NULL "per_token" left
// result.BillingMode nil via the handler's ignored Scan error, and
// TestUpdateModelOffer_ClearCanonical_UsesBindingJoin's BillingMode assertion
// could never pass. The binding-join UPDATE regexp remains the actual
// regression guard; the assertion below pins the NULL passthrough instead.
func offerResultRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "raw_model_name", "standardized_name", "canonical_id",
		"canonical_name", "outbound_model_name", "context_window",
		"context_window_override", "unit_price_in_per_1m", "unit_price_out_per_1m",
		"cache_read_price_per_1m", "cache_write_price_per_1m", "billing_mode",
	}).AddRow(301, "some-raw", nil, nil, nil, nil, int64(128000), nil, nil, nil, nil, nil, nil)
}

// 2026-09-11 join-fix regression guard: clear_canonical must NULL
// provider_models.canonical_id via the binding locator "cmb.id = $1"
// (model_offers.id IS credential_model_bindings.id). If a refactor ever
// reintroduces the broken "c.id = pm.provider_id" join, the SQL stops
// matching this expectation and mock.ExpectationsWereMet fails the test.
func TestUpdateModelOffer_ClearCanonical_UsesBindingJoin(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(offerLookupRows())
	// Migration 693: the clear must stamp the persistent admin-unbind
	// marker in the same UPDATE — a bare canonical_id = NULL is undone by
	// the next discovery refresh.
	mock.ExpectExec(`(?s)UPDATE provider_models pm\s+SET canonical_id = NULL,\s+canonical_cleared_at = now\(\),.*cmb\.id = \$1`).
		WithArgs(301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`LEFT JOIN credential_model_bindings cmb`).
		WithArgs(301).
		WillReturnRows(offerResultRows())

	rec := rec()
	serveUpdateModelOffer(rec, newReq(http.MethodPatch, "/api/providers/2/offers/301", strings.NewReader(`{"clear_canonical":true}`)), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID          int     `json:"id"`
		CanonicalID *int    `json:"canonical_id"`
		BillingMode *string `json:"billing_mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 301 || resp.CanonicalID != nil || resp.BillingMode != nil {
		t.Fatalf("response=%+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A zero-rows-affected clear is a warn-and-continue, not a 5xx.
func TestUpdateModelOffer_ClearCanonical_NoRowsStillOK(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(offerLookupRows())
	mock.ExpectExec(`(?s)UPDATE provider_models pm\s+SET canonical_id = NULL,\s+canonical_cleared_at = now\(\),.*cmb\.id = \$1`).
		WithArgs(301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(`LEFT JOIN credential_model_bindings cmb`).
		WithArgs(301).
		WillReturnRows(offerResultRows())

	rec := rec()
	serveUpdateModelOffer(rec, newReq(http.MethodPatch, "/api/providers/2/offers/301", strings.NewReader(`{"clear_canonical":true}`)), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Setting canonical_id resolves the standard name and mirrors
// standardized_name into provider_models through the binding locator
// (cmb.id = $2) — the second half of the 2026-09-11 join fix.
func TestUpdateModelOffer_SetCanonical_MirrorsStandardizedName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(offerLookupRows())
	mock.ExpectQuery(`SELECT canonical_name FROM models_canonical WHERE id`).
		WithArgs(7).
		WillReturnRows(pgxmock.NewRows([]string{"canonical_name"}).AddRow("claude-opus-5"))
	mock.ExpectExec(`UPDATE model_offers SET canonical_id`).
		WithArgs(7, 301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Migration 693: an explicit canonical re-link lifts the admin-unbind
	// marker on the base table — the model_offers INSTEAD OF UPDATE trigger
	// cannot see canonical_cleared_at.
	mock.ExpectExec(`(?s)UPDATE provider_models\s+SET canonical_cleared_at = NULL.*cmb\.id = \$1`).
		WithArgs(301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE model_offers SET standardized_name`).
		WithArgs("claude-opus-5", 301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`(?s)UPDATE provider_models\s+SET standardized_name.*cmb\.id = \$2`).
		WithArgs("claude-opus-5", 301).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`LEFT JOIN credential_model_bindings cmb`).
		WithArgs(301).
		WillReturnRows(offerResultRows())

	rec := rec()
	serveUpdateModelOffer(rec, newReq(http.MethodPatch, "/api/providers/2/offers/301", strings.NewReader(`{"canonical_id":7}`)), mock, 2, 301)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateModelOffer_OfferNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM model_offers mo`).
		WithArgs(301, 2).
		WillReturnRows(pgxmock.NewRows([]string{"id", "raw_model_name"}))

	rec := rec()
	serveUpdateModelOffer(rec, newReq(http.MethodPatch, "/api/providers/2/offers/301", strings.NewReader(`{"clear_canonical":true}`)), mock, 2, 301)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
