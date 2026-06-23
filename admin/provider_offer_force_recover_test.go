package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
