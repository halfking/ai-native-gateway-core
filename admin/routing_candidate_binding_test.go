package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutingCandidateBindingUpdate_InputValidation covers the input-side
// guards added in 2026-07-24 for the candidate-binding PATCH endpoint:
// - wrong method
// - bad credential_id
// - missing raw_model
// - empty body (no fields to update)
// - out-of-range field values
//
// Database-touching paths require a live pgxpool; those are covered by
// integration tests, not by this handler-level guard test.
func TestRoutingCandidateBindingUpdate_InputValidation(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "wrong method",
			method:     http.MethodGet,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
			wantSubstr: "method not allowed",
		},
		{
			name:       "bad credential id",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/not-a-number?raw_model=gpt-4",
			body:       `{"manual_priority": 1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid credential_id",
		},
		{
			name:       "missing raw_model",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123",
			body:       `{"manual_priority": 1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "raw_model query parameter required",
		},
		{
			name:       "empty body",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "at least one of",
		},
		{
			name:       "routing_tier out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"routing_tier": 12}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "routing_tier must be in",
		},
		{
			name:       "weight out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"weight": -1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "weight must be in",
		},
		{
			name:       "manual_priority out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"manual_priority": 150}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "manual_priority must be in",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.handleRoutingCandidateBindingUpdate(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
		})
	}
}
