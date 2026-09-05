package bg

import (
	"context"
	"os"
	"regexp"
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
		// 2026-09-05 (PG log audit): source rows must be staged via the
		// temp-table CTAS — the direct multi-CTE pipeline over
		// candidate_failure_logs_unified aborts on citus-columnar partitions
		// (SQLSTATE XX000, "cache lookup failed for attribute source").
		"CREATE TEMP TABLE provider_error_agg_src",
		"FROM candidate_failure_logs_unified c",
		"WHERE c.aggregation_id > $1",
		"all_source_rows AS (",
		"SELECT * FROM provider_error_agg_src",
		"new_source_rows AS",
		"affected_buckets AS",
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

// 2026-09-05 (PG log audit P3 regression guard, doc
// docs/2026-09-05-pg-error-audit-and-environment.md §1 row 1 / §3.1):
// candidate_failure_logs_unified synthesizes a constant `source` column over
// citus-columnar month partitions, and citus 13.3 aborts any plan that reads it
// with SQLSTATE XX000 "cache lookup failed for attribute source of relation".
// The aggregator's fix stages source rows via a plain CTAS into the temp table
// provider_error_agg_src, with `NULL::text AS source` as a shape-preserving
// placeholder alias. This test pins that contract: the placeholder alias is the
// ONLY legal spelling of the token `source`; any other bare or qualified
// reference (c.source) in executable SQL is a regression.
func TestProviderErrorAggregatorSQLNeverReferencesUnifiedViewSourceColumn(t *testing.T) {
	data, err := os.ReadFile("provider_error_aggregator.go")
	if err != nil {
		t.Fatal(err)
	}
	// The contract is about executable SQL tokens, so strip Go (`//`) and SQL
	// (`--`) line comments first, then keep only string literal contents (the
	// backtick raw strings that hold the SQL, plus double-quoted strings that
	// carry SQL keywords). Scanning the whole source would false-positive on
	// log messages like "source staging failed".
	code := aggregatorExtractSQLText(aggregatorStripLineComments(string(data)))

	// The placeholder alias must survive any refactor of the staging CTAS —
	// it keeps the temp-table column shape identical to the old in-pipeline
	// SELECT list.
	if !strings.Contains(code, "NULL::text AS source") {
		t.Errorf("staging CTAS must keep the `NULL::text AS source` placeholder so provider_error_agg_src keeps the unified view's column shape")
	}

	// The unified view must be read exactly once: by the staging CTAS. Every
	// additional reference re-opens the columnar XX000 crash surface.
	if n := strings.Count(code, "candidate_failure_logs_unified"); n != 1 {
		t.Errorf("candidate_failure_logs_unified must be referenced only by the staging CTAS (NULL placeholder follows it), found %d references", n)
	}
	fromView := strings.Index(code, "FROM candidate_failure_logs_unified")
	placeholder := strings.Index(code, "NULL::text AS source")
	if fromView < 0 || placeholder < 0 || fromView < placeholder {
		t.Fatal("staging CTAS must select the NULL source placeholder from (or before) candidate_failure_logs_unified — the single sanctioned read of the view")
	}

	// After removing the sanctioned alias spelling, no bare `source` token may
	// remain anywhere in executable SQL: it would be a reference to the
	// view-synthesized column. Word boundaries keep this precise —
	// last_source_id / all_source_rows / provider_error_agg_src do not match,
	// which is exactly the "引用列 vs 占位别名" distinction.
	withoutAlias := aggregatorReAliasSource.ReplaceAllString(code, "")
	if loc := aggregatorReBareSource.FindStringIndex(withoutAlias); loc != nil {
		t.Errorf("aggregator SQL references the unified view's synthesized `source` column in executable SQL near %q; citus-columnar partitions abort such plans with XX000 (doc §3.1). Stage rows via provider_error_agg_src instead", withoutAlias[max(loc[0]-40, 0):loc[1]+40])
	}

	// Defense in depth: qualified references, even if the bare-token scan ever
	// regresses in precision.
	for _, bad := range []string{"c.source", "c .source", "s.source"} {
		if strings.Contains(withoutAlias, bad) {
			t.Errorf("aggregator SQL must not reference the view-synthesized column via %q", bad)
		}
	}

	// SELECT c.* over the unified view would silently expand to include the
	// synthesized `source` column — same crash, invisible in the SQL text.
	if strings.Contains(code, "SELECT c.*") {
		t.Error("aggregator must not `SELECT c.*` from candidate_failure_logs_unified: the wildcard expands to the synthesized `source` column that crashes citus-columnar plans")
	}
}

// aggregatorStripLineComments removes Go (`//`) and SQL (`--`) line comments so
// the scan below only sees code and string literals.
func aggregatorStripLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "//"); idx >= 0 {
			ln = ln[:idx]
		}
		if idx := strings.Index(ln, "--"); idx >= 0 {
			ln = ln[:idx]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

// aggregatorExtractSQLText concatenates the contents of string literals that
// plausibly hold SQL: backtick raw strings (how the aggregator declares its
// statements) and double-quoted strings containing SQL keywords (inline Exec
// arguments). Log messages and identifiers are dropped.
func aggregatorExtractSQLText(code string) string {
	var b strings.Builder
	reSQLKeyword := regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE|WITH|FROM)\b`)
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '`':
			end := strings.IndexByte(code[i+1:], '`')
			if end < 0 {
				return b.String()
			}
			b.WriteString(code[i+1 : i+1+end])
			b.WriteByte('\n')
			i += end + 1
		case '"':
			var j int
			for j = i + 1; j < len(code); j++ {
				if code[j] == '\\' {
					j++
					continue
				}
				if code[j] == '"' {
					break
				}
			}
			lit := code[i+1 : min(j, len(code))]
			if reSQLKeyword.MatchString(lit) {
				b.WriteString(lit)
				b.WriteByte('\n')
			}
			i = j
		}
	}
	return b.String()
}

var (
	// The sanctioned placeholder alias spelling ("NULL::text AS source"); also
	// tolerates a lowercase `as`. Anything it strips is legal by definition.
	aggregatorReAliasSource = regexp.MustCompile(`(?i)\bAS\s+source\b`)
	// A bare, lowercase `source` token. `_` is a word character, so
	// last_source_id / all_source_rows / new_source_rows never match.
	aggregatorReBareSource = regexp.MustCompile(`\bsource\b`)
)
