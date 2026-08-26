// work_types_routes_test.go — exercises the validation branches of
// PUT /api/admin/work-types/:key/routes without spinning up a real DB.
//
// The pre-transaction checks (duplicate canonical_name, invalid tier) are
// pure-Go and run before any DB call. These tests cover them end-to-end via
// the httptest recorder and verify the wire-level error codes so the
// frontend can rely on stable strings.
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// putRoutesPUT builds a valid PUT request to putRoutes for the given body.
// h.db is nil — the validation branches must short-circuit before any
// DB call, so a nil pool is safe here.
func putRoutesPUT(t *testing.T, h *WorkTypeHandlers, key string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/admin/work-types/"+key+"/routes", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.putRoutes(res, req, key)
	return res
}

// errorBody decodes the {"error": {"code": "...", "message": "..."}}
// envelope written by writeJSONErrCtx so tests can assert against the
// stable machine-readable `code` field.
func errorBody(t *testing.T, res *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v (raw=%q)", err, res.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

// TestPutRoutes_DuplicateCanonicalNameRejected verifies that two entries
// sharing canonical_name in a single PUT short-circuit with 400 +
// admin_duplicate_route *before* the DB transaction begins, instead of
// failing mid-INSERT with a generic unique_violation 500.
func TestPutRoutes_DuplicateCanonicalNameRejected(t *testing.T) {
	h := NewWorkTypeHandlers(nil)
	body := []map[string]interface{}{
		{"canonical_name": "claude-opus", "tier": "primary", "weight": 1, "enabled": true},
		{"canonical_name": "claude-opus", "tier": "secondary", "weight": 0.9, "enabled": true},
	}

	res := putRoutesPUT(t, h, "reasoning", body)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	code, msg := errorBody(t, res)
	if code != "admin_duplicate_route" {
		t.Fatalf("error code = %q, want admin_duplicate_route (msg=%q)", code, msg)
	}
}

// TestPutRoutes_InvalidTierRejected verifies that an unknown tier string
// is rejected with a clean 400 rather than failing the SQL CHECK and
// surfacing as a 500-shaped error.
func TestPutRoutes_InvalidTierRejected(t *testing.T) {
	h := NewWorkTypeHandlers(nil)
	body := []map[string]interface{}{
		{"canonical_name": "claude-opus", "tier": "urgent", "weight": 1, "enabled": true},
	}

	res := putRoutesPUT(t, h, "reasoning", body)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	code, _ := errorBody(t, res)
	if code != "admin_invalid_tier" {
		t.Fatalf("error code = %q, want admin_invalid_tier", code)
	}
}

// TestPutRoutes_TierWhitelistAcceptsAllKnownValues checks the validation
// table itself — all three documented tiers must pass through to the
// (DB-calling) bulk path. We assert the negative path here: an unknown
// value gets caught, a valid value reaches the DB call (which we
// satisfy by giving h.db = nil — anything past validation would NPE,
// which the test catches as a panic, proving validation is the only gate).
func TestPutRoutes_TierWhitelistAcceptsAllKnownValues(t *testing.T) {
	for _, tier := range []string{"primary", "secondary", "fallback"} {
		t.Run("tier="+tier, func(t *testing.T) {
			h := NewWorkTypeHandlers(nil) // nil db — anything past validation panics
			body := []map[string]interface{}{
				{"canonical_name": "claude-opus", "tier": tier, "weight": 1, "enabled": true},
			}
			defer func() {
				if r := recover(); r != nil {
					// Reaching the DB call is the success signal for
					// validation — the panic here means validation
					// passed (otherwise we'd have written 400 and
					// returned cleanly).
					if !strings.Contains(toString(r), "nil") &&
						!strings.Contains(toString(r), "invalid memory") {
						t.Fatalf("unexpected panic on tier=%s: %v", tier, r)
					}
				} else {
					t.Fatalf("tier=%s should have reached DB and panicked (db is nil)", tier)
				}
			}()
			res := putRoutesPUT(t, h, "reasoning", body)
			if res.Code == http.StatusBadRequest {
				t.Fatalf("tier=%s was unexpectedly rejected", tier)
			}
		})
	}
}

// TestPutRoutes_EmptyCanonicalNameSkipped ensures blank rows are dropped
// from the dedupe loop. Three rows with two blank canonical_names should
// not trip the duplicate check (because blanks are skipped before the
// seen-map runs) and should reach the DB. With h.db = nil that means a
// panic, which is the success signal.
func TestPutRoutes_EmptyCanonicalNameSkipped(t *testing.T) {
	h := NewWorkTypeHandlers(nil)
	body := []map[string]interface{}{
		{"canonical_name": "", "tier": "primary", "weight": 1, "enabled": true},
		{"canonical_name": "claude-opus", "tier": "primary", "weight": 1, "enabled": true},
		{"canonical_name": "  ", "tier": "secondary", "weight": 1, "enabled": true},
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic (db nil) but validation passed and we returned cleanly with status=%d",
				0)
		}
	}()
	res := putRoutesPUT(t, h, "reasoning", body)
	if res.Code == http.StatusBadRequest {
		t.Fatalf("blank rows should not trip duplicate or invalid-tier check, got 400")
	}
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return ""
}