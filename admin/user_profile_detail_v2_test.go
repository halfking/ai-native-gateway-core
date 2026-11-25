package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestProjectedUserProfileRoutesRegisterWithUsageMatrix(t *testing.T) {
	mux := http.NewServeMux()
	(&Handler{}).RegisterRoutes(mux)
	for _, path := range []string{"usage-matrix", "alice", "alice/projects"} {
		r := httptest.NewRequest(http.MethodGet, userProfilesV2Path+"/"+path, nil)
		_, pattern := mux.Handler(r)
		if pattern == "" {
			t.Fatalf("profile route not registered for %s", path)
		}
		if path == "usage-matrix" && pattern != "GET "+userProfilesV2Path+"/usage-matrix" {
			t.Fatalf("profile detail intercepted the model matrix: %s", pattern)
		}
	}
}

func TestProjectedUserProfileCalendarWindowUsesLocalMidnights(t *testing.T) {
	settings.SystemTimezoneLocationForTest("America/New_York")
	t.Cleanup(func() { settings.SystemTimezoneLocationForTest("") })
	start, end, err := projectedOwnerUsageWindow("2026-11-01", "2026-11-01", time.Time{})
	if err != nil || start.Format(time.RFC3339) != "2026-11-01T04:00:00Z" || end.Sub(start) != 25*time.Hour {
		t.Fatalf("fall-back natural day: %s to %s, %v", start, end, err)
	}
	start, end, err = projectedOwnerUsageWindow("2026-11-01T00:00:00.123456-04:00", "2026-11-02T00:00:00.123456-05:00", time.Time{})
	if err != nil || start.Format(time.RFC3339Nano) != "2026-11-01T04:00:00.123456Z" ||
		end.Format(time.RFC3339Nano) != "2026-11-02T05:00:00.123456Z" {
		t.Fatalf("exact descent window: %s to %s, %v", start, end, err)
	}
}

func TestProjectedUserProfileBindsOrdinaryUsersToSelf(t *testing.T) {
	handler := &Handler{}
	for _, tc := range []struct {
		owner, query string
		code         int
	}{
		{"bob", "", http.StatusForbidden},
		{"alice", "?tenant_id=tenant-b", http.StatusForbidden},
		{"alice", "", http.StatusServiceUnavailable},
	} {
		r := httptest.NewRequest(http.MethodGet, userProfilesV2Path+"/"+tc.owner+tc.query, nil)
		r.SetPathValue("owner", tc.owner)
		r = SetAuthContext(r, &AuthContext{TenantID: "tenant-a", Username: "alice", Role: "user", IsJWT: true})
		w := httptest.NewRecorder()
		handler.handleUserProfileDetailV2(w, r)
		if w.Code != tc.code {
			t.Fatalf("owner=%s query=%q status=%d, want %d: %s", tc.owner, tc.query, w.Code, tc.code, w.Body)
		}
	}
}
