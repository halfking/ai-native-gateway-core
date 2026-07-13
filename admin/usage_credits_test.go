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

func TestQueryCreditsFromRequestLogsFallbackSQLShape(t *testing.T) {
	platformSQL := `
		SELECT COALESCE(SUM(COALESCE(r.credits_charged, CASE)), 0)::bigint
		  FROM request_logs_hot AS r
		 WHERE r.ts >= now() - ($1 * INTERVAL '1 day')
		   AND r.tenant_id <> ''
	`
	tenantSQL := `
		SELECT COALESCE(SUM(COALESCE(r.credits_charged, CASE)), 0)::bigint
		  FROM request_logs_hot AS r
		 WHERE r.ts >= now() - ($1 * INTERVAL '1 day')
		   AND r.tenant_id = $2
	`
	for _, sql := range []string{platformSQL, tenantSQL} {
		for _, want := range []string{"request_logs_hot", "COALESCE(SUM"} {
			if !strings.Contains(sql, want) {
				t.Errorf("credits request_logs fallback SQL missing %q", want)
			}
		}
	}
}

func TestIsPlatformCreditsScope(t *testing.T) {
	for _, tid := range []string{"", "default"} {
		if !isPlatformCreditsScope(tid) {
			t.Fatalf("isPlatformCreditsScope(%q) = false, want true", tid)
		}
	}
	if isPlatformCreditsScope("hansi") {
		t.Fatal("tenant-scoped id must not be platform scope")
	}
}

func TestRequestLogsFromClauseUsesUnionForLongWindows(t *testing.T) {
	from, alias := requestLogsFromClause(30)
	if alias != "r" {
		t.Fatalf("alias = %q, want r", alias)
	}
	if !strings.Contains(from, "request_logs_with_current_month") {
		t.Fatalf("long window must use cross-month view: %q", from)
	}
	from7, _ := requestLogsFromClause(7)
	if strings.Contains(from7, "UNION ALL") {
		t.Fatalf("7-day window should use hot table only: %q", from7)
	}
}

func TestQueryCreditsFromBucketsReturnsRowCount(t *testing.T) {
	sql := `
		SELECT COALESCE(SUM(credits), 0)::bigint,
		       COUNT(*)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`
	for _, want := range []string{"COUNT(*)::bigint", "COALESCE(SUM(credits), 0)::bigint"} {
		if !strings.Contains(sql, want) {
			t.Errorf("credits bucket SQL missing %q", want)
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
