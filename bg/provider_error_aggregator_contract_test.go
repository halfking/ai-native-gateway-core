package bg

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestProviderErrorAggregatorSQLIsTenantScopedAndBucketIdempotent(t *testing.T) {
	data, err := os.ReadFile("provider_error_aggregator.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"set_config('app.current_role', 'super_admin', true)",
		"set_config('app.bypass_rls', 'true', true)",
		"pg_try_advisory_xact_lock($1)",
		"new_source_rows AS",
		"affected_buckets AS",
		"all_source_rows AS NOT MATERIALIZED",
		"bucket_rows AS",
		"FROM bucket_rows",
		"PARTITION BY tenant_id, provider_id, credential_id",
		// 2026-09-01 (P0-1 24h-audit round2): credential_id joined the
		// aggregation grain (migration 639). Every DISTINCT ON / PARTITION BY /
		// ON CONFLICT key list must carry it or per-credential rows collapse.
		"tenant_id, provider_id, credential_id, model_name",
		"COALESCE(c.credential_id::text, '')",
		"COALESCE(credential_id, '')",
		"AS aggregation_bucket",
		"occurrences = EXCLUDED.occurrences",
		"aggregation_bucket",
		"COALESCE(tenant_id, '')",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provider error aggregator missing %q", want)
		}
	}

	// Watermark rows only identify affected aggregate keys. Re-reading the complete
	// matching buckets makes N existing rows plus one new row write N+1. A replay
	// follows the same path and therefore remains N+1 rather than multiplying.
	newRows := strings.Index(s, "new_source_rows AS")
	affected := strings.Index(s, "affected_buckets AS")
	buckets := strings.Index(s, "bucket_rows AS")
	aggregated := strings.Index(s, "aggregated AS")
	if newRows < 0 || affected < newRows || buckets < affected || aggregated < buckets {
		t.Fatal("aggregator must identify watermark rows, reread affected buckets, then aggregate them")
	}
	if strings.Contains(s, "occurrences = provider_error_details.occurrences + EXCLUDED.occurrences") {
		t.Fatal("aggregator must replace complete bucket counts, not add overlapping windows")
	}
}

func TestProviderErrorAggregatorMigrationsFailClosedForLegacyDuplicates(t *testing.T) {
	for _, path := range []string{
		"../sql/migrations/startup/620_provider_error_details_tenant_scope.sql",
		"../sql/migrations/startup/620_provider_error_details_tenant_scope.down.sql",
		"../deploy/sql/migrations/V364__provider_error_details_tenant_scope.sql",
		"../deploy/sql/migrations/V364__provider_error_details_tenant_scope.down.sql",
		// 2026-09-01 (P0-1 24h-audit round2): 639/V368 rebuild the unique
		// index with credential_id in the identity; same fail-closed contract.
		"../sql/migrations/startup/639_provider_error_details_credential.sql",
		"../sql/migrations/startup/639_provider_error_details_credential.down.sql",
		"../deploy/sql/migrations/V368__provider_error_details_credential.sql",
		"../deploy/sql/migrations/V368__provider_error_details_credential.down.sql",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		migration := string(data)
		for _, want := range []string{"HAVING count(*) > 1", "RAISE EXCEPTION"} {
			if !strings.Contains(migration, want) {
				t.Errorf("%s must fail closed for unsafe legacy duplicates; missing %q", path, want)
			}
		}
		if strings.Contains(migration, "duplicates are merged") {
			t.Errorf("%s must not claim it merges production legacy duplicates", path)
		}
	}
}

func TestProviderErrorAggregatorDeployMigrationMirrorsWatermarkState(t *testing.T) {
	for _, path := range []string{
		"../deploy/sql/migrations/V365__provider_error_aggregator_state.sql",
		"../deploy/sql/migrations/V365__provider_error_aggregator_state.down.sql",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		migration := string(data)
		for _, want := range []string{
			"candidate_failure_logs_hot",
			"provider_error_aggregator_state",
			"aggregation_id",
		} {
			if !strings.Contains(migration, want) {
				t.Errorf("%s missing provider error aggregator mirror %q", path, want)
			}
		}
	}
}

func TestProviderErrorAggregatorStopIsSafeBeforeAndAfterStart(t *testing.T) {
	agg := NewProviderErrorAggregator(nil, 0)
	agg.Stop()
	agg.Stop()
	agg.Start(context.Background())
	agg.Stop()
}
