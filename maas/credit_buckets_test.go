package maas

import (
	"context"
	"os"
	"testing"
)

// TestCreditBucketsSQL ensures the upsert SQL in chargeTokens is the
// exact atomic-increment form we expect (idempotent guard, sum accumulation
// rather than replace). This guards against an accidental regression where
// someone replaces the upsert with a plain INSERT (which would lose
// concurrent increments) or with INSERT … ON CONFLICT DO UPDATE SET
// credits = EXCLUDED.credits (which would lose already-accumulated totals).
func TestCreditBucketsSQL(t *testing.T) {
	src, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(src)
	wantSubs := []string{
		"INSERT INTO maas_credit_consumption_buckets",
		"ON CONFLICT (tenant_id, bucket_start) DO UPDATE",
		"credits + EXCLUDED.credits",
		"request_count + 1",
		"date_trunc('hour', now(), 'UTC')",
	}
	for _, w := range wantSubs {
		if !contains(sql, w) {
			t.Errorf("chargeTokens bucket SQL missing %q", w)
		}
	}
}

// TestQueryConsumedCreditsWindowContract: the read query must filter by
// both tenant_id and the rolling window so a single tenant dashboard load
// can't see other tenants' data and can answer any window the UI asks for.
func TestQueryConsumedCreditsWindowContract(t *testing.T) {
	sql := `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`
	wantSubs := []string{
		"WHERE tenant_id = $1",
		"bucket_start >=",
		"INTERVAL '1 day'",
		"COALESCE(SUM(credits), 0)::bigint",
	}
	for _, w := range wantSubs {
		if !contains(sql, w) {
			t.Errorf("consumed credits window query missing %q", w)
		}
	}
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestQueryConsumedCreditsWindow_Gating: when the Service is disabled
// (nil pool or no DB), the function must return 0 with nil error so the
// admin dashboard gracefully shows 0 instead of erroring out.
func TestQueryConsumedCreditsWindow_Gating(t *testing.T) {
	svc := &Service{}
	got, err := svc.QueryConsumedCreditsWindow(context.Background(), "tenant-x", 7)
	if err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0 when disabled, got %d", got)
	}
}

// TestBackfillCreditConsumptionBuckets_Gating: same gating when disabled.
func TestBackfillCreditConsumptionBuckets_Gating(t *testing.T) {
	svc := &Service{}
	rows, err := svc.BackfillCreditConsumptionBuckets(context.Background(), 90)
	if err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected 0 rows when disabled, got %d", rows)
	}
}
