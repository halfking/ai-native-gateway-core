package provider

import (
	"os"
	"strings"
	"testing"
)

func TestCandidateQueryPricingPlansFallback(t *testing.T) {
	raw, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, needle := range []string{
		"pp_fb.plan_in",
		"pp_fb.plan_out",
		"COALESCE(mo.unit_price_in_per_1m, pp_fb.plan_in)",
		"FROM pricing_plans pp",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("candidate SQL missing pricing fallback fragment: %q", needle)
		}
	}
	if strings.Contains(body, "COALESCE(mo.unit_price_in_per_1m, 0)::float8 AS unit_price_in_per_1m") {
		t.Error("candidate SQL must not COALESCE unit prices to 0 (masks NULL pricing)")
	}
}
