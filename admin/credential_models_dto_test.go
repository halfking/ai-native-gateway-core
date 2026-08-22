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
