package admin

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

func TestRequestReconciliationRoleScope(t *testing.T) {
	for _, tt := range []struct {
		role, tenant, filter, section string
		want                          int
	}{
		{"user", "tenant-a", "", "requests", http.StatusForbidden},
		{"tenant_admin", "", "", "requests", http.StatusForbidden},
		{"tenant_admin", "tenant-a", "tenant-b", "requests", http.StatusForbidden},
		{"tenant_admin", "tenant-a", "", "attempts", http.StatusForbidden},
		{"tenant_admin", "tenant-a", "tenant-a", "postings", http.StatusServiceUnavailable},
		{"super_admin", "tenant-a", "tenant-b", "attempts", http.StatusServiceUnavailable},
	} {
		t.Run(strings.Join([]string{tt.role, tt.tenant, tt.filter, tt.section}, "/"), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/request-reconciliation?start=2026-11-22T16:00:00Z&end=2026-11-23T16:00:00Z&timezone=Asia/Shanghai&section="+tt.section+"&tenant_id="+tt.filter, nil)
			r = SetAuthContext(r, &AuthContext{TenantID: tt.tenant, Role: tt.role})
			result := httptest.NewRecorder()
			(&Handler{}).handleRequestReconciliation(result, r)
			if result.Code != tt.want {
				t.Fatalf("status %d, want %d", result.Code, tt.want)
			}
		})
	}
}

func TestRequestReconciliationLegacyUsageRoleGate(t *testing.T) {
	for _, section := range []string{"cost-trend", "period-compare", "cache-economics"} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/"+section, nil)
		for _, auth := range []*AuthContext{{Role: "user", TenantID: "a"}, {Role: "tenant_admin"}} {
			response := httptest.NewRecorder()
			(&Handler{}).HandleUsageAdmin(response, SetAuthContext(r, auth))
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s with %s returned %d", section, auth.Role, response.Code)
			}
		}
	}
}

func TestRequestReconciliationWindowAndExport(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?start=2026-11-23T00:00:00%2B08:00&end=2026-11-24T00:00:00%2B08:00&timezone=Asia/Shanghai&section=attempts&limit=1&offset=2", nil)
	q, err := parseRequestReconciliationQuery(r)
	if err != nil || q.Start.UTC().Format(time.RFC3339) != "2026-11-22T16:00:00Z" || q.Limit != 1 || q.Offset != 2 {
		t.Fatalf("query = %+v, error = %v", q, err)
	}
	for _, bad := range []string{"/?timezone=Local", "/?timezone=Bad/Zone", "/?timezone=UTC&start=2026-11-23T00:00:00Z&end=2026-11-23T00:00:00Z"} {
		if _, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, bad, nil)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	data, err := requestReconciliationCSV(reconciliation.Report{Section: q.Section, Rows: []json.RawMessage{
		json.RawMessage(`{"attempt_id":"attempt-1","provider_id":9007199254740993,"model":"=LABEL","estimated_amount":"-0.0120000010000000001","reported_amount":null}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("export %q: %v", data, err)
	}
	cells := make(map[string]string)
	for i, col := range rows[0] {
		cells[col] = rows[1][i]
	}
	if cells["attempt_id"] != "attempt-1" || cells["provider_id"] != "9007199254740993" || cells["model"] != "'=LABEL" ||
		cells["estimated_amount"] != "-0.0120000010000000001" || cells["reported_amount"] != "" {
		t.Fatalf("unexpected export values %v", cells)
	}
}
