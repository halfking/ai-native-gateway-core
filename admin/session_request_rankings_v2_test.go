package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionRequestRankingV2RejectsInvalidScopeAndSort(t *testing.T) {
	tenant := &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", IsJWT: true}
	base := "/api/admin/session-analytics/v2/agent-ranking?start_date=2026-11-23&end_date=2026-11-23"
	for _, tc := range []struct {
		name, method, query string
		auth                *AuthContext
		status              int
	}{
		{"rejects writes", http.MethodPost, "", tenant, http.StatusMethodNotAllowed},
		{"requires authentication", http.MethodGet, "", nil, http.StatusUnauthorized},
		{"requires an administrator", http.MethodGet, "", &AuthContext{TenantID: tenant.TenantID, Role: "user"}, http.StatusForbidden},
		{"rejects another tenant", http.MethodGet, "&tenant_id=tenant-b", tenant, http.StatusForbidden},
		{"rejects model sort on agent route", http.MethodGet, "&sort_by=cache_rate", tenant, http.StatusBadRequest},
		{"accepts tenant and session sort", http.MethodGet, "&sort_by=sessions", tenant, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, base+tc.query, nil)
			if tc.auth != nil {
				r = SetAuthContext(r, tc.auth)
			}
			w := httptest.NewRecorder()
			(&Handler{}).handleSessionRequestRankingV2(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
}
