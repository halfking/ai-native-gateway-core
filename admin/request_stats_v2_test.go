package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

func TestRequestStatsV2NaturalDayAndScopedDimensions(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, requestStatsV2Path+"/breakdown?day=2026-11-23&timezone=Asia%2FShanghai&dimension=provider&credential_id=12", nil)
	query, err := parseRequestStatsV2(r, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if query.TenantID != "tenant-a" || query.Group != "provider" || query.Credential == nil || *query.Credential != 12 ||
		query.StartTime.Format(time.RFC3339) != "2026-11-22T16:00:00Z" ||
		query.EndTime.Format(time.RFC3339) != "2026-11-23T16:00:00Z" {
		t.Fatalf("unexpected query %+v", query)
	}
}

func TestRequestStatsV2UsagePickerDatesRemainInclusive(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, usageV2Path+"/providers/7/trend?start=2026-11-23&end=2026-11-23&period=hour", nil)
	r.SetPathValue("provider_id", "7")
	query, err := parseRequestStatsV2(r, "")
	if err != nil || query.ProviderID == nil || *query.ProviderID != 7 || query.Group != "hour" ||
		query.EndTime.Sub(query.StartTime) != 24*time.Hour {
		t.Fatalf("unexpected usage query %+v, %v", query, err)
	}
}

func TestRequestStatsV2RejectsUnscopedTenant(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, requestStatsV2Path+"/summary?tenant_id=tenant-b", nil)
	r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "tenant-a"})
	w := httptest.NewRecorder()
	(&Handler{}).handleRequestStatsV2(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRequestStatsV2CSVUsesSameMetricsAndEscapesLabels(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, usageV2Path+"/providers/export", nil)
	w := httptest.NewRecorder()
	result := metrics.RequestStats{Ready: true, Group: "provider", Items: []json.RawMessage{
		json.RawMessage(`{"provider_id":3,"provider_name":"=test","provider_code":"provider","total_requests":2,"total_prompt_tokens":4,"total_completion_tokens":6,"total_tokens":10,"last_request_at":null,"total_cost_usd":0.004001,"success_rate":1,"cost_rows_outside_usd":0}`),
	}}
	writeUsageMetricsV2(w, r, result)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "3,'=test,provider,2,4,6,10,0.004001,1,0") {
		t.Fatalf("response %d %q", w.Code, w.Body.String())
	}
}

func TestRequestStatsV2RegularUserIsScopedToOwnTraffic(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, usageV2Path+"/summary?tenant_id=tenant-a", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "tenant-a", Username: "owner-a", IsJWT: true})
	tenant, owner, ok := requestStatsV2Scope(r)
	if !ok || tenant != "tenant-a" || owner != "owner-a" {
		t.Fatalf("scope tenant=%q owner=%q allowed=%t", tenant, owner, ok)
	}
	q := r.URL.Query()
	q.Set("tenant_id", "tenant-b")
	r.URL.RawQuery = q.Encode()
	if _, _, ok := requestStatsV2Scope(r); ok {
		t.Fatal("cross-tenant access granted")
	}
}
