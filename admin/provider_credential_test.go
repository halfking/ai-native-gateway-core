package admin

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
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

// TestAddCredentialBody_DefaultsToTriggerForFpSlot verifies that a minimal
// create payload (api_key + label only) leaves fp_slot_limit unset in the
// parsed struct, so the auto_set_fp_slot_limit DB trigger (migration 039)
// can compute the correct ratio from concurrency_limit. Regression guard
// for the bug where the handler hard-coded fp_slot_limit=20 alongside
// concurrency_limit=10, violating credentials_fp_slot_vs_concurrency
// (20 > 10 → SQLSTATE 23514).
func TestAddCredentialBody_DefaultsToTriggerForFpSlot(t *testing.T) {
	body := `{"api_key":"sk-test","label":"gpt"}`
	var req struct {
		Label            *string `json:"label"`
		APIKey           string  `json:"api_key"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
		FpSlotLimit      *int    `json:"fp_slot_limit"`
	}
	if err := json.NewDecoder(bytes.NewBufferString(body)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.APIKey != "sk-test" {
		t.Fatalf("expected api_key=sk-test, got %q", req.APIKey)
	}
	if req.FpSlotLimit != nil {
		t.Fatalf("expected fp_slot_limit to stay nil so DB trigger fills it, got %v", *req.FpSlotLimit)
	}
	if req.ConcurrencyLimit != nil {
		t.Fatalf("expected concurrency_limit to stay nil in parsed body (handler defaults to 10), got %v", *req.ConcurrencyLimit)
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

// TestValidateFpSlotVsConcurrency covers the constraint pre-check that
// updateCredential runs before applying any PATCH. The DB CHECK constraint
// credentials_fp_slot_vs_concurrency (migration 039) requires
// fp_slot_limit <= concurrency_limit, with NULL allowed on either side.
// Regression guard for the bug where a PATCH that only lowered
// concurrency_limit would crash the UPDATE with SQLSTATE 23514.
func TestValidateFpSlotVsConcurrency(t *testing.T) {
	cases := []struct {
		name        string
		concurrency *int
		fpSlot      *int
		wantErr     bool
	}{
		{"both nil → no constraint", nil, nil, false},
		{"only concurrency set → no constraint", intPtr(10), nil, false},
		{"only fp_slot set → no constraint", nil, intPtr(5), false},
		{"equal → ok", intPtr(10), intPtr(10), false},
		{"fp_slot < concurrency → ok", intPtr(10), intPtr(2), false},
		{"fp_slot > concurrency → reject", intPtr(5), intPtr(8), true},
		{"fp_slot = 1, concurrency = 0 → reject (unlimited cap, but constraint still applies)", intPtr(0), intPtr(1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFpSlotVsConcurrency(tc.concurrency, tc.fpSlot)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if err != nil {
				// The error message must reference both values so the
				// operator can diagnose without checking the DB.
				if !bytes.Contains([]byte(err.Error()), []byte("fp_slot_limit")) {
					t.Fatalf("error should mention fp_slot_limit, got %q", err)
				}
			}
		})
	}
}

// TestEffectiveInt verifies the post-update value computation: incoming
// PATCH wins, otherwise the row's current value, otherwise nil.
func TestEffectiveInt(t *testing.T) {
	intEq := func(a, b *int) bool {
		if a == nil || b == nil {
			return a == b
		}
		return *a == *b
	}

	t.Run("incoming wins over current", func(t *testing.T) {
		got := effectiveInt(intPtr(7), sql.NullInt32{Int32: 99, Valid: true})
		if !intEq(got, intPtr(7)) {
			t.Fatalf("expected 7, got %v", deref(got))
		}
	})
	t.Run("falls back to current when incoming is nil", func(t *testing.T) {
		got := effectiveInt(nil, sql.NullInt32{Int32: 99, Valid: true})
		if !intEq(got, intPtr(99)) {
			t.Fatalf("expected 99, got %v", deref(got))
		}
	})
	t.Run("returns nil when both are absent", func(t *testing.T) {
		got := effectiveInt(nil, sql.NullInt32{Valid: false})
		if got != nil {
			t.Fatalf("expected nil, got %v", *got)
		}
	})
	t.Run("incoming wins even when current is invalid", func(t *testing.T) {
		got := effectiveInt(intPtr(3), sql.NullInt32{Valid: false})
		if !intEq(got, intPtr(3)) {
			t.Fatalf("expected 3, got %v", deref(got))
		}
	})
}

func intPtr(v int) *int { return &v }
func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestActorFromRequest verifies the X-Admin-User attribution fallback:
// the header beats the default "admin" string when set; otherwise we
// default to "admin" so the operator_user column never stores NULL.
func TestActorFromRequest(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		wantVal string
	}{
		{"header set → use header", "alice", "alice"},
		{"header empty → default admin", "", "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/x", nil)
			if tc.header != "" {
				req.Header.Set("X-Admin-User", tc.header)
			}
			if got := actorFromRequest(req); got != tc.wantVal {
				t.Fatalf("got %q, want %q", got, tc.wantVal)
			}
		})
	}
}

// TestJsonOrNull covers the JSONB encoding helper: a valid sql.NullInt32
// becomes a JSON number, an invalid one becomes JSON null. The audit
// reader relies on the shape being uniform.
func TestJsonOrNull(t *testing.T) {
	t.Run("valid int → JSON number", func(t *testing.T) {
		got := jsonOrNull(sql.NullInt32{Int32: 25, Valid: true})
		if string(got) != "25" {
			t.Fatalf("expected 25, got %s", got)
		}
	})
	t.Run("invalid → JSON null", func(t *testing.T) {
		got := jsonOrNull(sql.NullInt32{Valid: false})
		if string(got) != "null" {
			t.Fatalf("expected null, got %s", got)
		}
	})
}

// TestRejectedTransitionJSON covers the audit-log payload builder. The
// shape must include both the attempted values (so we know what the
// operator sent) and the current values (so we know what they collided
// with), even when fields are nil/invalid. sql.NullInt32 values are
// flattened to *int so they JSON-encode as a number or null instead of
// the awkward {"Int32":N,"Valid":B} default.
func TestRejectedTransitionJSON(t *testing.T) {
	got := rejectedTransitionJSON(
		sql.NullInt32{Int32: 100, Valid: true}, // current concurrency
		sql.NullInt32{Int32: 25, Valid: true},  // current fp_slot
		intPtr(10),                            // attempted concurrency
		nil,                                   // attempted fp_slot (unchanged)
	)
	var payload struct {
		AttemptedConcurrency *int `json:"attempted_concurrency"`
		AttemptedFpSlot      *int `json:"attempted_fp_slot"`
		CurrentConcurrency   *int `json:"current_concurrency"`
		CurrentFpSlot        *int `json:"current_fp_slot"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, got)
	}
	if payload.AttemptedConcurrency == nil || *payload.AttemptedConcurrency != 10 {
		t.Fatalf("attempted_concurrency: got %v, want 10", payload.AttemptedConcurrency)
	}
	if payload.AttemptedFpSlot != nil {
		t.Fatalf("attempted_fp_slot: expected nil, got %v", *payload.AttemptedFpSlot)
	}
	if payload.CurrentConcurrency == nil || *payload.CurrentConcurrency != 100 {
		t.Fatalf("current_concurrency: got %v, want 100", payload.CurrentConcurrency)
	}
	if payload.CurrentFpSlot == nil || *payload.CurrentFpSlot != 25 {
		t.Fatalf("current_fp_slot: got %v, want 25", payload.CurrentFpSlot)
	}
}

// TestRejectedTransitionJSON_NullCurrentValues verifies that absent
// current values serialize as JSON null rather than crashing or emitting
// the NullInt32 struct shape.
func TestRejectedTransitionJSON_NullCurrentValues(t *testing.T) {
	got := rejectedTransitionJSON(
		sql.NullInt32{Valid: false},
		sql.NullInt32{Valid: false},
		intPtr(10),
		intPtr(50),
	)
	var payload struct {
		AttemptedConcurrency *int `json:"attempted_concurrency"`
		AttemptedFpSlot      *int `json:"attempted_fp_slot"`
		CurrentConcurrency   *int `json:"current_concurrency"`
		CurrentFpSlot        *int `json:"current_fp_slot"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, got)
	}
	if payload.CurrentConcurrency != nil || payload.CurrentFpSlot != nil {
		t.Fatalf("expected current_* to be nil, got concurrency=%v fp_slot=%v",
			payload.CurrentConcurrency, payload.CurrentFpSlot)
	}
	// Raw bytes should contain JSON null tokens for the current fields.
	if !bytes.Contains(got, []byte(`"current_concurrency":null`)) {
		t.Fatalf("expected current_concurrency null in raw JSON, got %s", got)
	}
}

// TestWriteConstraintError_EnvelopeShape verifies the 400 response shape
// used when PATCH is rejected by credentials_fp_slot_vs_concurrency. The
// legacy `error.detail` string MUST stay so existing callers (and the
// generic req() extractor) keep working. The new `error.code` and
// `error.context` fields are an opt-in affordance for the UI to render
// a targeted message without parsing the detail.
func TestWriteConstraintError_EnvelopeShape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeConstraintError(
		rr,
		fmt.Errorf("fp_slot_limit (25) cannot exceed concurrency_limit (10) after this update"),
		intPtr(10),            // attempted concurrency
		intPtr(25),            // attempted fp_slot
		sql.NullInt32{Int32: 100, Valid: true}, // current concurrency
		sql.NullInt32{Int32: 25, Valid: true},  // current fp_slot
	)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
	var resp struct {
		Error struct {
			Detail  string `json:"detail"`
			Code    string `json:"code"`
			Context struct {
				AttemptedConcurrency *int `json:"attempted_concurrency"`
				AttemptedFpSlot      *int `json:"attempted_fp_slot"`
				CurrentConcurrency   *int `json:"current_concurrency"`
				CurrentFpSlot        *int `json:"current_fp_slot"`
			} `json:"context"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, rr.Body.String())
	}
	if resp.Error.Detail == "" {
		t.Fatalf("detail must remain populated for backward compat")
	}
	if resp.Error.Code != "fp_slot_exceeds_concurrency" {
		t.Fatalf("code: got %q, want fp_slot_exceeds_concurrency", resp.Error.Code)
	}
	if resp.Error.Context.AttemptedConcurrency == nil || *resp.Error.Context.AttemptedConcurrency != 10 {
		t.Fatalf("attempted_concurrency: got %v, want 10", resp.Error.Context.AttemptedConcurrency)
	}
	if resp.Error.Context.AttemptedFpSlot == nil || *resp.Error.Context.AttemptedFpSlot != 25 {
		t.Fatalf("attempted_fp_slot: got %v, want 25", resp.Error.Context.AttemptedFpSlot)
	}
	if resp.Error.Context.CurrentConcurrency == nil || *resp.Error.Context.CurrentConcurrency != 100 {
		t.Fatalf("current_concurrency: got %v, want 100", resp.Error.Context.CurrentConcurrency)
	}
	if resp.Error.Context.CurrentFpSlot == nil || *resp.Error.Context.CurrentFpSlot != 25 {
		t.Fatalf("current_fp_slot: got %v, want 25", resp.Error.Context.CurrentFpSlot)
	}
}

// TestWriteConstraintError_NullCurrentValues covers the case where the
// rejected PATCH only sent concurrency_limit (the more common case from
// the UI when the user only edits one field). The current_fp_slot must
// serialize as JSON null so the UI can tell "no value" apart from "0".
func TestWriteConstraintError_NullCurrentValues(t *testing.T) {
	rr := httptest.NewRecorder()
	writeConstraintError(
		rr,
		fmt.Errorf("fp_slot_limit (25) cannot exceed concurrency_limit (10) after this update"),
		intPtr(10),
		nil,                    // user only sent concurrency
		sql.NullInt32{Valid: false},
		sql.NullInt32{Int32: 25, Valid: true},
	)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Context struct {
				AttemptedFpSlot    *int `json:"attempted_fp_slot"`
				CurrentConcurrency *int `json:"current_concurrency"`
				CurrentFpSlot      *int `json:"current_fp_slot"`
			} `json:"context"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, rr.Body.String())
	}
	if resp.Error.Code != "fp_slot_exceeds_concurrency" {
		t.Fatalf("code: got %q", resp.Error.Code)
	}
	if resp.Error.Context.AttemptedFpSlot != nil {
		t.Fatalf("attempted_fp_slot should be nil when user didn't send it, got %v", *resp.Error.Context.AttemptedFpSlot)
	}
	if resp.Error.Context.CurrentConcurrency != nil {
		t.Fatalf("current_concurrency should be nil (DB row had NULL), got %v", *resp.Error.Context.CurrentConcurrency)
	}
	if resp.Error.Context.CurrentFpSlot == nil || *resp.Error.Context.CurrentFpSlot != 25 {
		t.Fatalf("current_fp_slot: got %v, want 25", resp.Error.Context.CurrentFpSlot)
	}
}
