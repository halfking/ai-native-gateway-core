package admin

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompareStatsSummaries(t *testing.T) {
	base := statsSummaryValues{Requests: 100, Success: 90, Failures: 10, TotalTokens: 1000, CostUSD: 1}
	tests := []struct {
		name string
		to   statsSummaryValues
		want string
	}{
		{"exact", base, "exact"},
		{"tolerated", statsSummaryValues{Requests: 101, Success: 90, Failures: 11, TotalTokens: 1005, CostUSD: 1}, "tolerated_drift"},
		{"material", statsSummaryValues{Requests: 120, Success: 108, Failures: 12, TotalTokens: 1200, CostUSD: 1.2}, "material_drift"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := compareStatsSummaries(base, test.to); got != test.want {
				t.Fatalf("compareStatsSummaries() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStatsShadowSampleBounds(t *testing.T) {
	if statsShadowSamplePercent(0, 1) {
		t.Fatal("zero percent must never sample")
	}
	if !statsShadowSamplePercent(100, 1) {
		t.Fatal("100 percent must always sample")
	}
}

func TestStatsShadowPartialWindow(t *testing.T) {
	if completeUTCWindow(time.Now(), time.Now().Add(3*time.Hour)) {
		t.Fatal("non-day-aligned window must not be treated as complete")
	}
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	if !completeUTCWindow(start, start.AddDate(0, 0, 7)) {
		t.Fatal("UTC day-aligned week window must be complete")
	}
}

func TestCompareStatsSummariesOutcomeFlags(t *testing.T) {
	legacy := statsSummaryValues{Requests: 100, PromptTokens: 10, TotalTokens: 10, HasOutcomes: false}
	canonical := statsSummaryValues{Requests: 100, Success: 90, Failures: 10, PromptTokens: 10, TotalTokens: 10, HasOutcomes: true}
	if got := compareStatsSummaries(legacy, canonical); got != "exact" {
		t.Fatalf("compareStatsSummaries() = %q, want exact (outcomes ignored when legacy lacks them)", got)
	}
	if _, ok := statsSummaryFromMap(map[string]any{
		"total_requests": int64(10), "success_count": int64(8), "failure_count": int64(2),
	}); !ok {
		t.Fatal("expected HasOutcomes summary to be comparable")
	}
}

func TestStatsSummaryFromMap(t *testing.T) {
	summary, ok := statsSummaryFromMap(map[string]any{
		"total_requests":          int64(10),
		"total_prompt_tokens":     int64(30),
		"total_completion_tokens": int64(20),
		"total_tokens":            int64(50),
		"total_credits_charged":   int64(50),
		"total_cost_usd":          1.5,
		"avg_latency_ms":          42.0,
		"success_rate":            0.8,
	})
	if !ok {
		t.Fatal("expected board summary to be comparable")
	}
	if summary.Success != 0 || summary.Failures != 0 || summary.TotalTokens != 50 {
		t.Fatalf("unexpected shadow summary: %+v", summary)
	}
	if _, ok := statsSummaryFromMap(map[string]any{
		"total_requests": int64(10), "success_count": int64(8), "failure_count": int64(2),
	}); !ok {
		t.Fatal("expected HasOutcomes summary to be comparable")
	}
	if _, ok := statsSummaryFromMap(map[string]any{
		"total_requests": int64(10), "success_rate": 0.8,
	}); !ok {
		t.Fatal("expected success_rate-only summary to be comparable without outcomes")
	}
}
func TestStatsTenantScope(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/admin/stats/summary?tenant_id=other", nil)
	request = SetAuthContext(request, &AuthContext{TenantID: "own", Role: "tenant_admin"})
	if got := statsTenantScope(request); got != "own" {
		t.Fatalf("tenant scope = %q, want own tenant", got)
	}
	request = SetAuthContext(request, &AuthContext{TenantID: "own", Role: "user"})
	if got := statsTenantScope(request); got != "own" {
		t.Fatalf("regular user scope = %q, want own tenant", got)
	}
	request = SetAuthContext(request, &AuthContext{Role: "super_admin"})
	if got := statsTenantScope(request); got != "other" {
		t.Fatalf("super admin scope = %q, want requested tenant", got)
	}
}
