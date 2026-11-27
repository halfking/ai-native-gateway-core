package admin

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/settings"
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

func TestRequestReconciliationCoverageRequiresPlatformAndCalendarPeriod(t *testing.T) {
	for _, tc := range []struct {
		role, query string
		status      int
	}{
		{"tenant_admin", "month=2026-11", http.StatusForbidden},
		{"tenant_admin", "day=2026-11-23", http.StatusForbidden},
		{"super_admin", "month=2026-11&format=csv", http.StatusServiceUnavailable},
		{"super_admin", "day=2026-11-23&format=csv", http.StatusServiceUnavailable},
		{"super_admin", "timezone=UTC&start=2026-11-01T00:00:00Z&end=2026-12-01T00:00:00Z", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/request-reconciliation?section=coverage&"+tc.query, nil)
		response := httptest.NewRecorder()
		(&Handler{}).handleRequestReconciliation(response, SetAuthContext(r, &AuthContext{Role: tc.role, TenantID: "a"}))
		if response.Code != tc.status {
			t.Errorf("%s %s status %d, want %d", tc.role, tc.query, response.Code, tc.status)
		}
	}
}

func TestRequestReconciliationNaturalDay(t *testing.T) {
	settings.SystemTimezoneLocationForTest("Asia/Shanghai")
	t.Cleanup(func() { settings.SystemTimezoneLocationForTest("") })
	for _, tc := range []struct{ query, day, start, end string }{
		{"day=2026-11-23", "2026-11-23", "2026-11-22T16:00:00Z", "2026-11-23T16:00:00Z"},
		{"day=2026-03-08&timezone=America/New_York", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"day=2026-11-01&timezone=America/New_York", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
		{"day=2011-12-29&timezone=Pacific/Apia", "2011-12-29", "2011-12-29T10:00:00Z", "2011-12-30T10:00:00Z"},
	} {
		query, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, "/?section=coverage&"+tc.query, nil))
		if err != nil || query.Day != tc.day || query.Month != "" || query.Start.UTC().Format(time.RFC3339) != tc.start || query.End.UTC().Format(time.RFC3339) != tc.end {
			t.Fatalf("%s yielded %+v, error=%v", tc.query, query, err)
		}
	}
	for _, bad := range []string{"day=", "day=2026-02-29", "day=2026-1-10", "day=2011-12-30&timezone=Pacific/Apia", "day=2026-11-23&month=2026-11", "day=2026-11-23&start=", "day=2026-11-23&end="} {
		if _, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, "/?"+bad, nil)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	settings.SystemTimezoneLocationForTest("America/New_York")
	query, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, "/?section=coverage&day=2026-11-01", nil))
	if err != nil || query.Timezone != "America/New_York" || query.End.Sub(query.Start) != 25*time.Hour {
		t.Fatalf("configured daily timezone yielded %+v, err=%v", query, err)
	}
}

func TestRequestReconciliationInternalRequiresTenant(t *testing.T) {
	for _, tc := range []struct {
		role, filter string
		status       int
	}{
		{"super_admin", "", http.StatusBadRequest},
		{"super_admin", "&tenant_id=b", http.StatusServiceUnavailable},
		{"tenant_admin", "", http.StatusServiceUnavailable},
		{"tenant_admin", "&tenant_id=b", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/usage/request-reconciliation?section=internal&month=2026-11"+tc.filter, nil)
		response := httptest.NewRecorder()
		(&Handler{}).handleRequestReconciliation(response, SetAuthContext(r, &AuthContext{Role: tc.role, TenantID: "a"}))
		if response.Code != tc.status {
			t.Errorf("%s %s status %d, want %d", tc.role, tc.filter, response.Code, tc.status)
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
	summary, err := requestReconciliationSummaryCSV(json.RawMessage(`{"net_credits":9007199254740993,"internal_currency":null,"postings":2}`))
	if err != nil || string(summary) != "metric,value\ninternal_currency,\nnet_credits,9007199254740993\npostings,2\n" {
		t.Fatalf("summary export %q: %v", summary, err)
	}
}

func TestRequestReconciliationNaturalMonth(t *testing.T) {
	for _, tt := range []struct{ query, month, zone, startUTC, endUTC string }{
		{"month=2026-11", "2026-11", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-11-30T16:00:00Z"},
		{"month=2024-02&timezone=UTC", "2024-02", "UTC", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"month=2026-03&timezone=America/New_York", "2026-03", "America/New_York", "2026-03-01T05:00:00Z", "2026-04-01T04:00:00Z"},
		{"month=2023-10&timezone=America/Asuncion", "2023-10", "America/Asuncion", "2023-10-01T04:00:00Z", "2023-11-01T03:00:00Z"},
		{"month=2023-09&timezone=America/Asuncion", "2023-09", "America/Asuncion", "2023-09-01T04:00:00Z", "2023-10-01T04:00:00Z"},
		{"month=2020-11&timezone=America/Havana", "2020-11", "America/Havana", "2020-11-01T04:00:00Z", "2020-12-01T05:00:00Z"},
	} {
		q, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil))
		if err != nil || q.Month != tt.month || q.Timezone != tt.zone || q.Start.UTC().Format(time.RFC3339) != tt.startUTC || q.End.UTC().Format(time.RFC3339) != tt.endUTC {
			t.Fatalf("%s yielded %+v, error = %v", tt.query, q, err)
		}
	}
	for _, query := range []string{"month=2026-13", "month=2026-2", "month=2026-11&start=2026-11-01T00:00:00Z"} {
		if _, err := parseRequestReconciliationQuery(httptest.NewRequest(http.MethodGet, "/?"+query, nil)); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestProjectedReconciliationRequiresCompletedCalendarDay(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?section=coverage&day=2026-11-01&timezone=America%2FNew_York", nil)
	q, err := parseRequestReconciliationQuery(r)
	if err != nil || q.End.Sub(q.Start) != 25*time.Hour {
		t.Fatalf("invalid fall-back day: %+v, %v", q, err)
	}
	if requireCompletedReconciliationDay(q, q.End.Add(-time.Nanosecond)) == nil || requireCompletedReconciliationDay(q, q.End) != nil {
		t.Fatal("the report must wait for local midnight")
	}
	q.Day, q.Month = "", "2026-11"
	if requireCompletedReconciliationDay(q, q.End.Add(time.Hour)) == nil {
		t.Fatal("V2 accepted a monthly interval")
	}
}

func TestProjectedReconciliationCSVAddsTerminalEvidence(t *testing.T) {
	report := reconciliation.Report{Version: reconciliation.ProjectedReportVersion, Section: "requests", Rows: []json.RawMessage{
		json.RawMessage(`{"request_id":"r","total_tokens":149,"input_tokens":100,"output_tokens":30,"projected_status":"success"}`),
		json.RawMessage(`{"request_id":"wal-only","status":"success","projected_status":null}`),
	}}
	data, err := requestReconciliationCSV(report)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil || len(rows) != 3 || !slices.Equal(rows[0][len(rows[0])-2:], []string{"total_tokens", "projected_status"}) ||
		!slices.Equal(rows[1][len(rows[1])-2:], []string{"149", "success"}) || rows[2][len(rows[2])-1] != "" {
		t.Fatalf("V2 export lost terminal evidence: %q, %v", data, err)
	}
	pending := time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)
	summary, err := requestReconciliationSummaryCSVWithProjection(json.RawMessage(`{"total_tokens":149}`),
		&reconciliation.ReportProjection{Source: "request_metrics", PendingSince: &pending})
	if err != nil || !strings.Contains(string(summary), "projection_pending_since,") || !strings.Contains(string(summary), "request_metrics") {
		t.Fatalf("V2 summary export lost projection state: %q, %v", summary, err)
	}
}
