package maas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClampUsageDays(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 1},
		{-5, 1},
		{7, 7},
		{90, 90},
		{120, 90},
	}
	for _, tc := range tests {
		if got := ClampUsageDays(tc.in); got != tc.want {
			t.Fatalf("ClampUsageDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRequestLogsSource(t *testing.T) {
	source, alias := requestLogsSource(7)
	if source != "request_logs_hot AS r" || alias != "r" {
		t.Fatalf("requestLogsSource(7) = %q/%q", source, alias)
	}
	source, alias = requestLogsSource(30)
	want := "(SELECT * FROM request_logs_hot UNION ALL SELECT * FROM request_logs) AS r"
	if source != want || alias != "r" {
		t.Fatalf("requestLogsSource(30) = %q/%q", source, alias)
	}
}

func TestClampUsageLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 10},
		{-1, 10},
		{10, 10},
		{25, 25},
		{100, 50},
	}
	for _, tc := range tests {
		if got := ClampUsageLimit(tc.in); got != tc.want {
			t.Fatalf("ClampUsageLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestQueryUsageSummary_requiresTenant(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.QueryUsageSummary(t.Context(), "", 7, 10)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}

func TestQueryConsumptionDetail_requiresTenant(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.QueryConsumptionDetail(t.Context(), "", "", 7)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}

func TestConsumptionDetailRowResponseShape(t *testing.T) {
	// Sanity-check that the response marshals the documented fields so
	// frontend types stay in sync with the backend contract.
	out := ConsumptionDetail{
		TenantID:       "default",
		Days:           7,
		CentsPerCredit: 0.1,
		Rows: []ConsumptionDetailRow{
			{
				TenantID:                "default",
				OwnerUser:               "alice",
				ProviderName:            "OpenAI",
				CredentialLabel:         "cred-1",
				Model:                   "gpt-4o",
				Requests:                10,
				PromptTokens:            100,
				CompletionTokens:        50,
				CacheReadTokens:         20,
				CacheWriteTokens:        5,
				CreditsCharged:          300,
				UpstreamCostUSD:         0.12,
				TenantRevenueUSD:        0.30,
				GrossMarginUSD:          0.18,
				GrossMarginRate:         0.6,
				CancelledBilledRequests: 1,
			},
		},
	}
	// Round-trip via JSON to surface contract drift early.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, field := range []string{
		"provider_name", "credential_label", "cache_read_tokens", "cache_write_tokens",
		"upstream_cost_usd", "tenant_revenue_usd", "gross_margin_usd", "gross_margin_rate",
		"cancelled_billed_requests",
	} {
		if !strings.Contains(body, field) {
			t.Errorf("missing %s in marshalled output", field)
		}
	}
}
