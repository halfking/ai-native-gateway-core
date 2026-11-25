package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestMaaSUsageV2ScopesAndCalendarWindows(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/maas/v2/usage/requests?day=2026-11-01&timezone=America/New_York&owner_user=somebody", nil)
	if _, _, ok := maasUsageScopeV2(r, false); ok {
		t.Fatal("unauthenticated request accepted")
	}
	r = SetAuthContext(r, &AuthContext{IsJWT: true, Role: "user", Username: "same-owner", TenantID: "tenant-a"})
	tenant, owner, ok := maasUsageScopeV2(r, false)
	if !ok || tenant != "tenant-a" || owner != "same-owner" {
		t.Fatalf("unexpected ordinary user scope: %q %q %v", tenant, owner, ok)
	}
	q, days, _, err := parseMaaSUsageV2(r, tenant, owner, "requests", false)
	if err != nil || days != 1 || q.End.Sub(q.Start) != 25*time.Hour || q.IncludeUpstreamCost {
		t.Fatalf("expected one local fall-back day, got %+v, days=%d, error=%v", q, days, err)
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/admin/maas/v2/tenants/tenant-b/usage/detail", nil)
	denied.SetPathValue("code", "tenant-b")
	denied.SetPathValue("section", "detail")
	denied = SetAuthContext(denied, &AuthContext{IsJWT: true, Role: "tenant_admin", TenantID: "tenant-a"})
	w := httptest.NewRecorder()
	(&Handler{}).handleMaaSUsageV2(w, denied)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant detail returned %d", w.Code)
	}
}

func TestMaaSCreditEstimatePreservesUnknownMarginAndPostedRevenue(t *testing.T) {
	observedUSD, missing := 0.002, int64(1)
	row := projectedMaaSDetailRow{Upstream: &observedUSD, CostRowsWithoutUSDEvidence: &missing}
	row.CreditsCharged = 4
	setMaaSCreditEstimate(&row, maasCreditReference{CentsPerCredit: 50, Currency: "CNY"})
	if row.EstimatedRevenueUSD == nil || *row.EstimatedRevenueUSD != 2/settings.CNYToUSDFX || row.EstimatedMarginUSD != nil || row.EstimatedMarginRate != nil {
		t.Fatalf("unexpected estimate: %+v", row)
	}
	encoded, err := json.Marshal(row)
	if err != nil || !strings.Contains(string(encoded), `"tenant_revenue_usd":null`) || !strings.Contains(string(encoded), `"upstream_cost_usd":0.002`) {
		t.Fatalf("unexpected exported amounts %s: %v", encoded, err)
	}
}
