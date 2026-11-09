package maas

import (
	"sync"
	"testing"
)

func TestCommerceAuditReferencePriceFailure_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	request := ModelRateUpsert{ManualIn: true, CreditsPer1MIn: 100}
	if err := svc.UpsertModelPriceVersionForMode(ctx, 1, BillingModeTokenPlan, request, "audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `ALTER TABLE model_price_versions ADD CONSTRAINT reject_audit_write CHECK (credits_per_1m_in < 150)`); err != nil {
		t.Fatal(err)
	}
	request.CreditsPer1MIn = 200
	if err := svc.UpsertModelPriceVersionForMode(ctx, 1, BillingModeTokenPlan, request, "audit"); err == nil {
		t.Fatal("reference price write reported success after the database rejected it")
	}
	var current int64
	if err := svc.pool.QueryRow(ctx, `SELECT credits_per_1m_in FROM model_price_versions WHERE effective_to IS NULL`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 100 {
		t.Fatalf("failed price write changed the open period: %d", current)
	}
}

func TestCommerceAuditPriceReadFailurePreservesPeriod_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	if _, err := svc.pool.Exec(ctx, `INSERT INTO model_price_versions
		(canonical_id, billing_mode, credits_per_1m_in) VALUES (1, 'pay_as_you_go', 100)`); err != nil {
		t.Fatal(err)
	}
	// model_credit_rates deliberately is not in the fixture: a failed read
	// must not be confused with a successful read proving that a rate was deleted.
	svc.SyncPayAsYouGoPriceVersion(ctx, 1, RateSourceManual, "read failure")
	var current int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM model_price_versions WHERE effective_to IS NULL`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 1 {
		t.Fatal("failed rate read closed the current price period")
	}
}

func TestCommerceAuditPricePeriodsAdjacent_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	for _, price := range []int64{100, 200, 200, 300} {
		if err := svc.UpsertModelPriceVersionForMode(ctx, 1, BillingModeTokenPlan,
			ModelRateUpsert{ManualIn: true, CreditsPer1MIn: price}, "audit"); err != nil {
			t.Fatal(err)
		}
	}
	var periods, gaps int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE effective_to IS DISTINCT FROM next_start)
		FROM (SELECT effective_to, lead(effective_from) OVER (ORDER BY effective_from) next_start
		      FROM model_price_versions WHERE canonical_id=1 AND billing_mode='token_plan') periods`).Scan(&periods, &gaps); err != nil {
		t.Fatal(err)
	}
	if periods != 3 || gaps != 0 {
		t.Fatalf("price periods=%d gaps=%d, want three adjacent periods with unchanged-price deduplication", periods, gaps)
	}
}

func TestCommerceAuditConcurrentPriceVersions_RealDB(t *testing.T) {
	svc, ctx := commerceAuditDB(t)
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- svc.UpsertModelPriceVersionForMode(ctx, 1, BillingModeTokenPlan,
				ModelRateUpsert{ManualIn: true, CreditsPer1MIn: int64(100 + i)}, "concurrent")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("concurrent price write: %v", err)
		}
	}
	var periods, current, gaps int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE effective_to IS NULL),
		count(*) FILTER (WHERE effective_to IS DISTINCT FROM next_start)
		FROM (SELECT effective_to, lead(effective_from) OVER (ORDER BY effective_from) next_start
		      FROM model_price_versions WHERE canonical_id=1 AND billing_mode='token_plan') periods`).Scan(&periods, &current, &gaps); err != nil {
		t.Fatal(err)
	}
	if periods != writers || current != 1 || gaps != 0 {
		t.Fatalf("concurrent periods=%d current=%d gaps=%d, want %d/1/0", periods, current, gaps, writers)
	}
}
