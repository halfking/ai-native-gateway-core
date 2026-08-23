package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration565CostUsdPricingBackfill(t *testing.T) {
	up, err := os.ReadFile("565_cost_usd_pricing_backfill.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)
	for _, needle := range []string{
		"pricing_plans",
		"catalog_estimate",
		"gpt-5.6-terra",
		"request_logs_hot h",
		"cost_usd IS NULL",
		"/ 7.2",
		"Idempotent: YES",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("565 up missing %q", needle)
		}
	}
	forbidden := []string{
		"CREATE OR REPLACE FUNCTION update_session_summary",
		"563_session_summary",
		"request_count = EXCLUDED.request_count",
	}
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("565 must not touch 563 trigger/backfill: %q", needle)
		}
	}
}
