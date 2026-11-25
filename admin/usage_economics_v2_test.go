package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

func TestEconomicsV2UsesOneCalendarWindow(t *testing.T) {
	const route = "/api/admin/usage/v2/cost-trend"
	r := httptest.NewRequest(http.MethodGet, route+"?date_from=2026-11-01&date_to=2026-11-01&timezone=America%2FNew_York&group_by=key", nil)
	query, group, err := parseEconomicsQuery(r, "acme", false)
	if err != nil || query.Group != "cost-api_key" || group != "key" || query.TenantID != "acme" || query.EndTime.Sub(query.StartTime) != 25*time.Hour {
		t.Fatalf("calendar range/alias mismatch: %+v, %s, %v", query, group, err)
	}
	if query.StartTime.Format(time.RFC3339) != "2026-11-01T04:00:00Z" || query.EndTime.Format(time.RFC3339) != "2026-11-02T05:00:00Z" {
		t.Fatalf("incorrect local day boundaries: %s - %s", query.StartTime, query.EndTime)
	}
	r = httptest.NewRequest(http.MethodGet, route+"?date_from=2026-11-01&date_to=2026-11-01&days=7", nil)
	if _, _, err := parseEconomicsQuery(r, "acme", false); err == nil {
		t.Fatal("ambiguous window was accepted")
	}
}

func TestEconomicsV2SeparatesMeasuredAndUnavailableValues(t *testing.T) {
	start := time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC)
	result := metrics.RequestStats{
		Ready: true, Source: "request_metrics", Timezone: "Asia/Shanghai", Start: start, End: start.Add(24 * time.Hour),
		Items: []json.RawMessage{json.RawMessage(`{"total_requests":7,"cache_input_tokens":325,"eligible_cache_read_tokens":95,
			"cache_scope_requests":4,"compression_scope_requests":4,"compressed_requests":0,"total_cost_usd":0.006,
			"cost_rows_outside_usd":1,"total_credits_charged":8}`)},
	}
	payload, err := projectedEconomicsPayload(result, "")
	if err != nil {
		t.Fatal(err)
	}
	cache, ok := payload.(projectedCacheEconomics)
	if !ok || cache.DateFrom != "2026-11-24" || cache.DateTo != "2026-11-24" || cache.CacheScope != 4 ||
		cache.CacheHitRatio == nil || *cache.CacheHitRatio != 95.0/325 || cache.Compressed == nil || *cache.Compressed != 0 ||
		cache.DollarsSpent != 0.006 || cache.NonUSDRows != 1 || cache.Credits != 8 || cache.DollarsSaved != nil || cache.TotalSaved != nil {
		t.Fatalf("unexpected cache evidence: %+v", payload)
	}
	result.Items = []json.RawMessage{json.RawMessage(`{"key":"test-model","total_requests":7,"total_cost_usd":0.006}`)}
	payload, err = projectedEconomicsPayload(result, "model")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var costs struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(encoded, &costs); err != nil || len(costs.Entries) != 1 ||
		string(costs.Entries[0]["input_cost_usd"]) != "null" || string(costs.Entries[0]["output_cost_usd"]) != "null" ||
		string(costs.Entries[0]["total_cost_usd"]) != "0.006" {
		t.Fatalf("cost evidence payload %s (%v)", encoded, err)
	}
}
