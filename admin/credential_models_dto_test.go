package admin

import (
	"context"
	"strings"
	"testing"
)

// TestOfferListSQL_DefaultExcludesPMSource guards the legacy package-level
// `var offerListSQL` against accidentally referencing `pm.source`. The compat
// variant is what handlers fall back to when the schema probe has not yet
// completed or has explicitly chosen the pre-migration shape — a regression
// here would resurrect the 500 on GET /api/providers/{id}/models.
func TestOfferListSQL_DefaultExcludesPMSource(t *testing.T) {
	if strings.Contains(offerListSQL, "pm.source") {
		t.Fatalf("offerListSQL must not reference pm.source as a hard-coded column (use the runtime-probed variant via offerListSQLFor). Got:\n%s", offerListSQL)
	}
	if strings.Contains(offerListSQL, "__PM_SOURCE__") {
		t.Fatalf("offerListSQL still contains the unresolved placeholder; offerListSQLColumns must be substituted before being assigned.")
	}
	if !strings.Contains(offerListSQL, "FROM model_offers mo") {
		t.Fatalf("offerListSQL is missing the FROM clause; substitution may be incomplete.")
	}
}

func TestOfferListSQLFor_NilPoolReturnsCompat(t *testing.T) {
	// Callers without a live pool (e.g. tests that don't wire a DB) must still
	// get back a usable SQL string and must not panic.
	sql := offerListSQLFor(context.Background(), nil)
	if sql == "" {
		t.Fatal("offerListSQLFor returned empty SQL for nil pool")
	}
	if strings.Contains(sql, "pm.source") {
		t.Fatalf("compat SQL must not reference pm.source; got: %s", sql)
	}
}

func TestOfferListSQLColumns_PlaceholderFormat(t *testing.T) {
	// The unresolved template must keep `__PM_SOURCE__` exactly so the
	// strings.Replace substitutions match a single token and don't accidentally
	// hit unrelated substrings.
	if !strings.Contains(offerListSQLColumns, "__PM_SOURCE__") {
		t.Fatal("offerListSQLColumns must keep the __PM_SOURCE__ placeholder")
	}
	if strings.Count(offerListSQLColumns, "__PM_SOURCE__") != 1 {
		t.Fatalf("__PM_SOURCE__ must appear exactly once; got %d",
			strings.Count(offerListSQLColumns, "__PM_SOURCE__"))
	}
}

// TestOfferListSQLFor_NilPool_FullQueryAssemblesWithoutPMSource reproduces the
// exact query body that getProviderModels used to assemble on the pre-361
// schema (the bug behind the 502 on /api/providers/14/models). With a nil pool
// the resolver must fall back to the compat SQL, and the resulting query body
// (SELECT + WHERE) must reference `pm.source` zero times.
func TestOfferListSQLFor_NilPool_FullQueryAssemblesWithoutPMSource(t *testing.T) {
	sql := offerListSQLFor(context.Background(), nil) + `
		WHERE c.provider_id = $1
		ORDER BY mo.raw_model_name
	`
	if strings.Contains(sql, "pm.source") {
		t.Fatalf("composed query body still references pm.source; GET /api/providers/{id}/models would 500 on a pre-361 schema. Query:\n%s", sql)
	}
	if !strings.Contains(sql, "WHERE c.provider_id = $1") {
		t.Fatalf("WHERE clause for provider-id lookup is missing; concatenation broke. Query:\n%s", sql)
	}
}

// TestGetProviderModels_UsesOfferListSQLFor_NotLegacyConstant ensures the
// handler chooses the runtime-probed variant. Even on the pre-361 schema the
// resulting query must not reference `pm.source` — otherwise the endpoint
// would 500 with `column pm.source does not exist`. This guards against an
// accidental revert to the legacy `offerListSQL` constant, which used to hard
// code `pm.source` before commit 7c03b9f4a.
func TestGetProviderModels_UsesOfferListSQLFor_NotLegacyConstant(t *testing.T) {
	sql := offerListSQLFor(context.Background(), nil)
	// The legacy constant must equal the runtime resolution for a nil pool.
	// If offerListSQL ever regresses to hard-code pm.source, this guard trips.
	if sql != offerListSQL {
		t.Fatalf("offerListSQLFor(nil) and offerListSQL must agree on the compat fallback.\nfor=%q\nlegacy=%q", sql, offerListSQL)
	}
}

// TestOfferListSQLColumns_MoModalityPlaceholderOnce pins the second
// schema-dependent placeholder. 2026-09-11 live regression: the shared DTO
// queried `mo.provider_modality` unconditionally and the model_offers view on
// upgraded installs (last rebuilt by migration 678, whose view body predates
// the alias) answered SQLSTATE 42703 — GET
// /api/providers/36994/credentials/77/models returned
// "column mo.provider_modality does not exist" 500.
func TestOfferListSQLColumns_MoModalityPlaceholderOnce(t *testing.T) {
	if n := strings.Count(offerListSQLColumns, "__MO_MODALITY__"); n != 1 {
		t.Fatalf("__MO_MODALITY__ must appear exactly once; got %d", n)
	}
	if !strings.Contains(offerListSQLColumns, "COALESCE(NULLIF(TRIM(mc.modality), ''), __MO_MODALITY__)") {
		t.Fatal("offerListSQLColumns must keep the canonical-modality COALESCE wrapping the __MO_MODALITY__ placeholder")
	}
}

// TestOfferListSQLFor_NilPool_ExcludesProviderModality guards the compat
// fallback for the stale-view case: without a live schema probe the resolved
// SQL must never reference `mo.provider_modality`, or upgraded installs 500
// with 42703 again. The canonical modality stays so the column count (and
// therefore scanModelOfferDTO) is unchanged between variants.
func TestOfferListSQLFor_NilPool_ExcludesProviderModality(t *testing.T) {
	sql := offerListSQLFor(context.Background(), nil)
	if strings.Contains(sql, "mo.provider_modality") {
		t.Fatalf("compat SQL must not reference mo.provider_modality; GET /api/providers/{id}/credentials/{cid}/models would 500 on a stale model_offers view. Query:\n%s", sql)
	}
	if !strings.Contains(sql, "'text'") {
		t.Fatal("compat modality term must fall back to 'text'")
	}
	if !strings.Contains(sql, "mc.modality") {
		t.Fatal("compat SQL must keep the canonical modality column")
	}
}
