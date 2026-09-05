//go:build integration

package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const providerErrorIntegrationIsolationEnv = "TEST_PG_CONTRACTS_ISOLATED"

func openProviderErrorIntegrationPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	if os.Getenv(providerErrorIntegrationIsolationEnv) != "1" {
		t.Skipf("%s=1 is required; this test mutates an isolated PostgreSQL database", providerErrorIntegrationIsolationEnv)
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s is not set; skipping PostgreSQL contract test", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(%s): %v", env, err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("%s is unreachable", env)
	}
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database identity: %v", err)
	}
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing mutating PostgreSQL contract test against database %q; use a dedicated *_test database", database)
	}
	return pool
}

func requireProviderErrorSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checks := []struct {
		name  string
		query string
	}{
		{"candidate_failure_logs_hot", `SELECT to_regclass('public.candidate_failure_logs_hot') IS NOT NULL`},
		{"candidate_failure_logs_hot.aggregation_id", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='candidate_failure_logs_hot' AND column_name='aggregation_id')`},
		{"provider_error_details", `SELECT to_regclass('public.provider_error_details') IS NOT NULL`},
		{"provider_error_details.aggregation_bucket", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='provider_error_details' AND column_name='aggregation_bucket')`},
		{"provider_error_aggregator_state", `SELECT to_regclass('public.provider_error_aggregator_state') IS NOT NULL`},
		{"provider_error_aggregator_state.singleton", `SELECT EXISTS (SELECT 1 FROM public.provider_error_aggregator_state WHERE id=1)`},
		{"tenant fingerprint index", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='idx_provider_error_details_tenant_fingerprint')`},
		// Migration 663 rebuilt the credential fingerprint without
		// error_message; the aggregator's ON CONFLICT target must match it or
		// every tick fails with SQLSTATE 42P10.
		{"credential fingerprint index", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='idx_provider_error_details_tenant_cred_fingerprint')`},
		{"cleanup index", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='idx_ped_resolved_updated_at')`},
	}
	for _, check := range checks {
		var ok bool
		if err := pool.QueryRow(ctx, check.query).Scan(&ok); err != nil {
			t.Fatalf("%s schema probe: %v", check.name, err)
		}
		if !ok {
			t.Fatalf("%s is missing; apply migrations 617, 620, 621, 622, 623 and 663 to the isolated database", check.name)
		}
	}
	var enabled, forced bool
	if err := pool.QueryRow(ctx, `
		SELECT c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relname='provider_error_details'`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("provider_error_details RLS probe: %v", err)
	}
	if !enabled || !forced {
		t.Fatal("provider_error_details must use enabled FORCE ROW LEVEL SECURITY")
	}
}

func TestProviderErrorAggregatorRealPG(t *testing.T) {
	pool := openProviderErrorIntegrationPool(t, "TEST_DATABASE_URL")
	requireProviderErrorSchema(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	alphaTenant := "pg-it-alpha-" + suffix
	betaTenant := "pg-it-beta-" + suffix
	model := "pg-it-model-" + suffix
	providerID := int(time.Now().UnixNano()%1000000000 + 1000000000)
	credentialID := providerID + 1
	alphaRequestPrefix := "pg-it-alpha-request-" + suffix
	betaRequestPrefix := "pg-it-beta-request-" + suffix
	fallbackRequest := "pg-it-fallback-request-" + suffix

	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			t.Errorf("cleanup begin: %v", err)
			return
		}
		defer tx.Rollback(context.Background()) //nolint:errcheck
		if _, err := tx.Exec(cleanupCtx, `
			SELECT set_config('app.current_role', 'super_admin', true),
			       set_config('app.bypass_rls', 'true', true)`); err != nil {
			t.Errorf("cleanup visibility: %v", err)
			return
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM provider_error_details WHERE tenant_id IN ($1,$2)`, alphaTenant, betaTenant); err != nil {
			t.Errorf("cleanup provider error details: %v", err)
			return
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM candidate_failure_logs_hot WHERE tenant_id IN ($1,$2)`, alphaTenant, betaTenant); err != nil {
			t.Errorf("cleanup candidate failures: %v", err)
			return
		}
		if err := tx.Commit(cleanupCtx); err != nil {
			t.Errorf("cleanup commit: %v", err)
		}
	}
	t.Cleanup(cleanup)

	// Advance the isolated database watermark past any pre-existing rows. This
	// keeps the assertion scoped to the fixture without resetting the sequence.
	if _, err := pool.Exec(ctx, `
		UPDATE provider_error_aggregator_state
		SET last_source_id = COALESCE((SELECT MAX(aggregation_id) FROM candidate_failure_logs_hot), 0), updated_at=now()
		WHERE id=1`); err != nil {
		t.Fatalf("advance initial watermark: %v", err)
	}
	firstBucket := time.Now().UTC().Truncate(10 * time.Minute)
	bucket := firstBucket.Add(time.Minute)
	insertSource := func(requestID, tenant, errorKind string, ts time.Time, endpoint bool) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin source seed: %v", err)
		}
		defer tx.Rollback(context.Background()) //nolint:errcheck
		if _, err := tx.Exec(ctx, `
			SELECT set_config('app.current_role', 'super_admin', true),
			       set_config('app.bypass_rls', 'true', true)`); err != nil {
			t.Fatalf("set source seed visibility: %v", err)
		}
		ctxJSON := `{}`
		if endpoint {
			ctxJSON = `{"endpoint":"chat"}`
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO candidate_failure_logs_hot
				(request_id, tenant_id, credential_id, provider_id, raw_model_name,
				 attempt_index, error_kind, error_message, upstream_status_code, context, ts)
			VALUES ($1,$2,$3,$4,$5,0,$6,'fixture upstream failure',502,$7::jsonb,$8)`,
			requestID, tenant, credentialID, providerID, model, errorKind, ctxJSON, ts); err != nil {
			t.Fatalf("insert source %s: %v", requestID, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit source seed: %v", err)
		}
	}
	insertSource(alphaRequestPrefix+"-1", alphaTenant, "network", bucket, true)
	insertSource(alphaRequestPrefix+"-2", alphaTenant, "network", bucket.Add(20*time.Second), true)
	insertSource(betaRequestPrefix+"-1", betaTenant, "network", bucket, true)
	insertSource(betaRequestPrefix+"-2", betaTenant, "network", bucket.Add(20*time.Second), true)
	insertSource(fallbackRequest, alphaTenant, "limiter", bucket.Add(40*time.Second), false)
	agg := NewProviderErrorAggregator(pool, time.Minute)
	agg.aggregateErrors(ctx)

	var alphaOccurrences, betaOccurrences, fallbackOccurrences, aggregateRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3`, alphaTenant, providerID, model).Scan(&aggregateRows); err != nil {
		t.Fatalf("count aggregates: %v", err)
	}
	if aggregateRows != 2 {
		t.Fatalf("aggregate rows = %d, want 2 (network + limiter)", aggregateRows)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network'`, alphaTenant, providerID, model).Scan(&alphaOccurrences); err != nil {
		t.Fatalf("alpha aggregate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='limiter'`, alphaTenant, providerID, model).Scan(&fallbackOccurrences); err != nil {
		t.Fatalf("fallback aggregate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network'`, betaTenant, providerID, model).Scan(&betaOccurrences); err != nil {
		t.Fatalf("beta aggregate: %v", err)
	}
	if alphaOccurrences != 2 || betaOccurrences != 2 || fallbackOccurrences != 1 {
		t.Fatalf("occurrences alpha=%d beta=%d fallback=%d, want 2/2/1", alphaOccurrences, betaOccurrences, fallbackOccurrences)
	}
	var endpoint, fallbackEndpoint string
	if err := pool.QueryRow(ctx, `SELECT endpoint FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network'`, alphaTenant, providerID, model).Scan(&endpoint); err != nil {
		t.Fatalf("network endpoint: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT endpoint FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='limiter'`, alphaTenant, providerID, model).Scan(&fallbackEndpoint); err != nil {
		t.Fatalf("fallback endpoint: %v", err)
	}
	if endpoint != "chat" || fallbackEndpoint != "unknown" {
		t.Fatalf("endpoints network=%q fallback=%q, want chat/unknown", endpoint, fallbackEndpoint)
	}

	// F-4 baseline for the cross-tick assertions below: the bucket's original
	// onset and its latest sighting right after the first tick.
	var firstSeen, lastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT first_seen_at, last_seen_at FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket).Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatalf("alpha bucket first/last seen: %v", err)
	}
	if !firstSeen.Equal(bucket) || !lastSeen.Equal(bucket.Add(20*time.Second)) {
		t.Fatalf("first/last seen = %s/%s, want %s/%s", firstSeen, lastSeen, bucket, bucket.Add(20*time.Second))
	}

	var watermark int64
	if err := pool.QueryRow(ctx, `SELECT last_source_id FROM provider_error_aggregator_state WHERE id=1`).Scan(&watermark); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if watermark <= 0 {
		t.Fatal("aggregator watermark did not advance")
	}
	var rowsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE tenant_id IN ($1,$2)`, alphaTenant, betaTenant).Scan(&rowsBefore); err != nil {
		t.Fatalf("count before replay: %v", err)
	}
	agg.aggregateErrors(ctx)
	var rowsAfter, alphaAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE tenant_id IN ($1,$2)`, alphaTenant, betaTenant).Scan(&rowsAfter); err != nil {
		t.Fatalf("count after replay: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network'`, alphaTenant, providerID, model).Scan(&alphaAfter); err != nil {
		t.Fatalf("alpha replay aggregate: %v", err)
	}
	if rowsAfter != rowsBefore || alphaAfter != alphaOccurrences {
		t.Fatalf("replay changed rows/occurrences: rows %d->%d occurrences %d->%d", rowsBefore, rowsAfter, alphaOccurrences, alphaAfter)
	}
	var watermarkAfter int64
	if err := pool.QueryRow(ctx, `SELECT last_source_id FROM provider_error_aggregator_state WHERE id=1`).Scan(&watermarkAfter); err != nil {
		t.Fatalf("read replay watermark: %v", err)
	}
	if watermarkAfter != watermark {
		t.Fatalf("replay watermark %d, want unchanged %d", watermarkAfter, watermark)
	}

	insertSource(alphaRequestPrefix+"-same-bucket", alphaTenant, "network", bucket.Add(45*time.Second), true)
	agg.aggregateErrors(ctx)
	var sameBucketOccurrences int
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket).Scan(&sameBucketOccurrences); err != nil {
		t.Fatalf("same bucket aggregate: %v", err)
	}
	if sameBucketOccurrences != 3 {
		t.Fatalf("same bucket occurrences=%d, want 3 after incremental source row", sameBucketOccurrences)
	}
	// F-4 cross-tick accumulation: the second tick must re-read the COMPLETE
	// bucket and replace it exactly — first_seen keeps the original onset,
	// last_seen advances to the newest row, occurrences accumulate (2+1).
	// With commit 3344f3f17's watermark-bounded staging the replace saw only
	// the new increment: occurrences collapsed to 1 and first_seen drifted to
	// the new row's ts.
	var crossFirstSeen, crossLastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT first_seen_at, last_seen_at FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket).Scan(&crossFirstSeen, &crossLastSeen); err != nil {
		t.Fatalf("cross-tick bucket first/last seen: %v", err)
	}
	if !crossFirstSeen.Equal(firstSeen) {
		t.Fatalf("cross-tick first_seen drifted %s -> %s, want unchanged (audit F-4)", firstSeen, crossFirstSeen)
	}
	if !crossLastSeen.Equal(bucket.Add(45*time.Second)) {
		t.Fatalf("cross-tick last_seen = %s, want %s", crossLastSeen, bucket.Add(45*time.Second))
	}

	insertSource(alphaRequestPrefix+"-next", alphaTenant, "network", bucket.Add(10*time.Minute), true)
	agg.aggregateErrors(ctx)
	var nextRows, nextOccurrences int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3`, alphaTenant, providerID, model).Scan(&nextRows); err != nil {
		t.Fatalf("count next bucket: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket.Add(10*time.Minute)).Scan(&nextOccurrences); err != nil {
		t.Fatalf("next bucket aggregate: %v", err)
	}
	if nextRows != 3 || nextOccurrences != 1 {
		t.Fatalf("next bucket rows=%d occurrences=%d, want 3/1", nextRows, nextOccurrences)
	}

	// F-4 same-bucket idempotency under a full re-process: roll the watermark
	// back so every fixture row stages again as "new" and both affected
	// buckets are re-read and replace-upserted from scratch. Replacement over
	// complete buckets must land on the identical rows — occurrences stay 3
	// and 1 (not 6 and 2), first/last_seen keep their values, row count
	// unchanged. A cumulative DO UPDATE (audit fix option 2) would
	// double-count on exactly this reprocess; complete-bucket replacement is
	// what makes it safe.
	var watermarkBeforeReset int64
	if err := pool.QueryRow(ctx, `SELECT last_source_id FROM provider_error_aggregator_state WHERE id=1`).Scan(&watermarkBeforeReset); err != nil {
		t.Fatalf("read watermark before reset: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_error_aggregator_state SET last_source_id = 0, updated_at = now() WHERE id = 1`); err != nil {
		t.Fatalf("reset watermark for reprocess: %v", err)
	}
	agg.aggregateErrors(ctx)
	var replayRows, replayFirstOccurrences, replayNextOccurrences int
	var replayFirstSeen, replayLastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3`, alphaTenant, providerID, model).Scan(&replayRows); err != nil {
		t.Fatalf("count after reprocess: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences, first_seen_at, last_seen_at FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket).Scan(&replayFirstOccurrences, &replayFirstSeen, &replayLastSeen); err != nil {
		t.Fatalf("reprocessed first bucket: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT occurrences FROM provider_error_details WHERE tenant_id=$1 AND provider_id=$2 AND model_name=$3 AND error_type='network' AND aggregation_bucket=$4`, alphaTenant, providerID, model, firstBucket.Add(10*time.Minute)).Scan(&replayNextOccurrences); err != nil {
		t.Fatalf("reprocessed next bucket: %v", err)
	}
	if replayRows != nextRows || replayFirstOccurrences != 3 || replayNextOccurrences != nextOccurrences ||
		!replayFirstSeen.Equal(firstSeen) || !replayLastSeen.Equal(bucket.Add(45*time.Second)) {
		t.Fatalf("full re-process changed buckets: rows=%d occurrences=%d/%d first=%s last=%s, want rows=%d 3/%d %s/%s",
			replayRows, replayFirstOccurrences, replayNextOccurrences, replayFirstSeen, replayLastSeen,
			nextRows, nextOccurrences, firstSeen, bucket.Add(45*time.Second))
	}
	var watermarkAfterReset int64
	if err := pool.QueryRow(ctx, `SELECT last_source_id FROM provider_error_aggregator_state WHERE id=1`).Scan(&watermarkAfterReset); err != nil {
		t.Fatalf("read watermark after reprocess: %v", err)
	}
	if watermarkAfterReset != watermarkBeforeReset {
		t.Fatalf("reprocess watermark %d, want restored %d", watermarkAfterReset, watermarkBeforeReset)
	}

	verifyProviderErrorTargetRLS(t, alphaTenant, betaTenant, providerID, model)
}

func verifyProviderErrorTargetRLS(t *testing.T, alphaTenant, betaTenant string, providerID int, model string) {
	t.Helper()
	pool := openProviderErrorIntegrationPool(t, "TEST_TENANT_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var superuser, bypassRLS bool
	if err := pool.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS); err != nil {
		t.Fatalf("read tenant role flags: %v", err)
	}
	if superuser || bypassRLS {
		t.Fatalf("TEST_TENANT_DATABASE_URL role must be NOSUPERUSER and NOBYPASSRLS, got superuser=%t bypassrls=%t", superuser, bypassRLS)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tenant RLS probe: %v", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, alphaTenant); err != nil {
		t.Fatalf("set alpha tenant: %v", err)
	}
	var alphaCount, betaCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE tenant_id=$1), count(*) FILTER (WHERE tenant_id=$2)
		FROM provider_error_details WHERE provider_id=$3 AND model_name=$4`, alphaTenant, betaTenant, providerID, model).Scan(&alphaCount, &betaCount); err != nil {
		t.Fatalf("tenant RLS query: %v", err)
	}
	if alphaCount != 3 || betaCount != 0 {
		t.Fatalf("tenant RLS saw alpha=%d beta=%d, want 3/0", alphaCount, betaCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tenant RLS probe: %v", err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unset tenant probe: %v", err)
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM provider_error_details WHERE provider_id=$1 AND model_name=$2`, providerID, model).Scan(&total); err != nil {
		t.Fatalf("unset tenant query: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit unset tenant probe: %v", err)
	}
	if total != 0 {
		t.Fatalf("unset tenant saw %d provider error rows, want 0", total)
	}
}
