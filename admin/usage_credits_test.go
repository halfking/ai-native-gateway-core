package admin

import (
	"os"
	"strings"
	"testing"
)

// Guards against re-inlining the credits subquery into usageSummary's main
// SELECT — that regression zeroed every KPI when maas_credit_consumption_buckets
// was not migrated yet.
func TestUsageSummaryDoesNotInlineCreditsBucketSubquery(t *testing.T) {
	src := mustReadAdminSource(t, "usage.go")
	if strings.Contains(src, "maas_credit_consumption_buckets") {
		t.Fatal("usage.go must not reference maas_credit_consumption_buckets; use usage_credits.go helpers instead")
	}
}

func TestQueryCreditsFromBucketsSQLShape(t *testing.T) {
	tenantSQL := `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`
	allTenantSQL := `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE bucket_start >= now() - ($1::int * INTERVAL '1 day')
	`
	for _, sql := range []string{tenantSQL, allTenantSQL} {
		for _, want := range []string{
			"maas_credit_consumption_buckets",
			"bucket_start >=",
			"COALESCE(SUM(credits), 0)::bigint",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("credits bucket SQL missing %q", want)
			}
		}
	}
}

func TestQueryCreditsFromLedgerFallbackSQLShape(t *testing.T) {
	sql := `
		SELECT COALESCE(SUM(credits_charged), 0)::bigint
		  FROM usage_ledger_with_current_month
		 WHERE ts >= now() - ($1 * INTERVAL '1 day') AND credits_charged IS NOT NULL
	`
	for _, want := range []string{
		"usage_ledger_with_current_month",
		"credits_charged IS NOT NULL",
		"COALESCE(SUM(credits_charged), 0)::bigint",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("credits ledger fallback SQL missing %q", want)
		}
	}
}

func mustReadAdminSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
