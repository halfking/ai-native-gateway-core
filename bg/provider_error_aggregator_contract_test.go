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
		// 2026-09-05 (round2 audit F-4): the watermark-bounded pass only
		// identifies affected buckets; the second staging pass re-reads the
		// complete buckets so the replace-style upsert stays exact per tick.
		"CREATE TEMP TABLE provider_error_agg_bucket_rows",
		"FROM provider_error_agg_bucket_rows",
		"new_source_rows AS",
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
		// 2026-09-05 (round2 audit E-#3): error_message left the aggregation
		// key (migration 663 rebuilt the unique index without it) and became a
		// sample column refreshed by the UPSERT.
		"error_message = EXCLUDED.error_message",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provider error aggregator missing %q", want)
		}
	}

	// The conflict-target spelling of the message key must be gone from the
	// source. The staging CTAS keeps LEFT(COALESCE(c.error_message, ''), 200)
	// as the sample projection — a different spelling, so this negative scan
	// is precise.
	if strings.Contains(s, "COALESCE(LEFT(error_message") {
		t.Error("provider error aggregator must not key buckets on error_message (audit E-#3: wording jitter split one logical error into many rows); message is a sample column now")
	}

	// Watermark rows only identify affected aggregate keys. The second staging
	// pass re-reads the complete matching buckets so N existing rows plus one
	// new row write N+1. A replay follows the same path and therefore remains
	// N+1 rather than multiplying.
	srcStage := strings.Index(s, "CREATE TEMP TABLE provider_error_agg_src")
	bucketStage := strings.Index(s, "CREATE TEMP TABLE provider_error_agg_bucket_rows")
	newRows := strings.Index(s, "new_source_rows AS")
	aggregated := strings.Index(s, "aggregated AS")
	advanced := strings.Index(s, "advanced AS")
	if srcStage < 0 || bucketStage < srcStage || newRows < bucketStage ||
		aggregated < newRows || advanced < aggregated {
		t.Fatal("aggregator must stage new rows, re-stage complete affected buckets, aggregate, then advance the watermark")
	}
	if strings.Contains(s, "occurrences = provider_error_details.occurrences + EXCLUDED.occurrences") {
		t.Fatal("aggregator must replace complete bucket counts, not add overlapping windows")
	}
}

// 2026-09-05 (round2 audit F-4): the replace-style DO UPDATE
// (occurrences = EXCLUDED.occurrences) is only correct when the staged rows
// cover the COMPLETE affected buckets. This pins the two-pass staging
// contract: pass one is watermark-bounded (its rows identify the buckets),
// pass two re-reads those buckets in full — with no watermark predicate
// anywhere in it — so cross-tick accumulation and the first_seen onset
// survive. Commit 3344f3f17 bounded the single staging pass by the watermark
// and clobbered exactly those: occurrences lost their accumulated count and
// first_seen_at drifted to the newest tick's window min.
func TestProviderErrorAggregatorStagesCompleteAffectedBuckets(t *testing.T) {
	data, err := os.ReadFile("provider_error_aggregator.go")
	if err != nil {
		t.Fatal(err)
	}
	code := aggregatorStripLineComments(string(data))

	const bucketStageDecl = "CREATE TEMP TABLE provider_error_agg_bucket_rows"
	start := strings.Index(code, bucketStageDecl)
	if start < 0 {
		t.Fatal("missing affected-bucket staging CTAS (audit F-4: the replace-style upsert needs complete buckets)")
	}
	end := strings.IndexByte(code[start:], '`')
	if end < 0 {
		t.Fatal("affected-bucket staging CTAS raw string not terminated")
	}
	bucketStage := code[start : start+end]

	for _, want := range []string{
		// Pass two must re-read the unified view itself — re-reading only the
		// already-staged new rows is the F-4 regression in another dress.
		"FROM candidate_failure_logs_unified c",
		// Full 8-column NULL-safe bucket-key join (error_message excluded per
		// E-#3): a narrower join would silently under-count affected buckets.
		"c.tenant_id IS NOT DISTINCT FROM b.tenant_id",
		"c.provider_id IS NOT DISTINCT FROM b.provider_id",
		"COALESCE(c.credential_id::text, '') IS NOT DISTINCT FROM b.credential_id",
		"c.raw_model_name IS NOT DISTINCT FROM b.model_name",
		"IS NOT DISTINCT FROM b.endpoint",
		"c.error_kind IS NOT DISTINCT FROM b.error_type",
		"IS NOT DISTINCT FROM b.error_code",
		"IS NOT DISTINCT FROM b.aggregation_bucket",
		// Bucket-scoped ts bounds (execution-time partition-pruning hints for
		// the historical month partitions): a row in one of the staged
		// 10-minute buckets necessarily falls inside this window.
		"c.ts >= (SELECT MIN(aggregation_bucket) FROM provider_error_agg_src)",
		"c.ts < (SELECT MAX(aggregation_bucket) FROM provider_error_agg_src) + interval '10 minutes'",
	} {
		if !strings.Contains(bucketStage, want) {
			t.Errorf("affected-bucket staging must contain %q", want)
		}
	}
	// Bounding the re-read by the watermark is precisely the F-4 regression:
	// cross-tick buckets would again be replaced by a single tick's increment.
	if strings.Contains(bucketStage, "aggregation_id >") {
		t.Error("affected-bucket staging must not be bounded by the watermark (audit F-4)")
	}

	// The replace-style upsert stays, backed by the complete-bucket staging.
	// The cumulative spelling (audit option 2: old + EXCLUDED) must not
	// return — it double-counts whenever a watermark rollback makes the
	// aggregator reprocess a bucket, which complete-bucket replacement
	// absorbs idempotently.
	if !strings.Contains(code, "occurrences = EXCLUDED.occurrences") ||
		!strings.Contains(code, "first_seen_at = EXCLUDED.first_seen_at") {
		t.Error("DO UPDATE must keep replace semantics backed by complete-bucket staging")
	}
	if strings.Contains(code, "provider_error_details.occurrences +") {
		t.Error("DO UPDATE must not accumulate occurrences; replacement over complete buckets is already idempotent per tick")
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

// 2026-09-05 (round2 audit E-#3): migration 662 rebuilds the
// provider_error_details fingerprint without error_message. Unlike the
// fail-closed 620/639 rebuilds, 662 merges message-fragmented duplicates
// automatically (616 precedent): fragmentation is the normal case on real
// data and fail-closed would block every startup. This pins the mechanics so
// the old message-scoped key cannot quietly return.
func TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint(t *testing.T) {
	data, err := os.ReadFile("../sql/migrations/startup/664_provider_error_details_agg_key_dedup.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(data)
	for _, want := range []string{
		// Rebuild mechanics (idempotent reinstall).
		"DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_cred_fingerprint",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_cred_fingerprint",
		// Legacy duplicate fold before the unique index can be created.
		"SUM(ped.occurrences)::int",
		"MIN(ped.first_seen_at)",
		"MAX(ped.last_seen_at)",
		// Validation proves the message left the key.
		"pg_get_indexdef",
		"664 VALIDATION FAIL",
	} {
		if !strings.Contains(migration, want) {
			t.Errorf("664 migration missing %q", want)
		}
	}

	// The new fingerprint expression list must not reference error_message.
	const createIdx = "CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_cred_fingerprint"
	idx := strings.Index(migration, createIdx)
	if idx < 0 {
		t.Fatal("664 migration has no CREATE UNIQUE INDEX for the credential fingerprint")
	}
	defEnd := strings.Index(migration[idx:], ";")
	if defEnd < 0 {
		t.Fatal("664 CREATE UNIQUE INDEX not terminated")
	}
	indexDef := migration[idx : idx+defEnd]
	if strings.Contains(indexDef, "error_message") {
		t.Error("664 fingerprint index must not key on error_message (E-#3)")
	}
	if !strings.Contains(indexDef, "COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')") {
		t.Error("664 fingerprint index must keep the epoch-sentinel bucket column (620 contract)")
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
// The aggregator's fix stages source rows via plain CTAS statements into the
// per-transaction temp tables provider_error_agg_src (new rows above the
// watermark) and provider_error_agg_bucket_rows (the complete affected
// buckets, audit F-4), with `NULL::text AS source` as a shape-preserving
// placeholder alias in both. This test pins that contract: the placeholder
// alias is the ONLY legal spelling of the token `source`; any other bare or
// qualified reference (c.source) in executable SQL is a regression.
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

	// The unified view must be read only by the two plain staging CTAS
	// statements (new rows above the watermark, then the complete affected
	// buckets — audit F-4). Any further reference — in particular a read from
	// the window/DISTINCT ON pipeline — re-opens the columnar XX000 crash
	// surface.
	const maxViewReads = 2
	if n := strings.Count(code, "candidate_failure_logs_unified"); n != maxViewReads {
		t.Errorf("candidate_failure_logs_unified must be referenced only by the %d staging CTAS statements (NULL placeholder precedes each), found %d references", maxViewReads, n)
	}
	rest := code
	for i := 0; i < maxViewReads; i++ {
		fromView := strings.Index(rest, "FROM candidate_failure_logs_unified")
		placeholder := strings.Index(rest, "NULL::text AS source")
		if fromView < 0 || placeholder < 0 || placeholder > fromView {
			t.Fatal("each staging CTAS must select the NULL source placeholder before FROM candidate_failure_logs_unified — the only sanctioned reads of the view")
		}
		rest = rest[fromView+len("FROM candidate_failure_logs_unified"):]
	}
	// No further view reference may remain after the second staging statement:
	// the aggregation pipeline reads only the staging temp tables.
	if strings.Contains(rest, "candidate_failure_logs_unified") {
		t.Error("the aggregation pipeline must not read candidate_failure_logs_unified directly; it reads only the staging temp tables")
	}

	// After removing the sanctioned alias spelling, no bare `source` token may
	// remain anywhere in executable SQL: it would be a reference to the
	// view-synthesized column. Word boundaries keep this precise —
	// last_source_id / new_source_rows / provider_error_agg_src do not match,
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
	// last_source_id / new_source_rows / provider_error_agg_src never match.
	aggregatorReBareSource = regexp.MustCompile(`\bsource\b`)
)
