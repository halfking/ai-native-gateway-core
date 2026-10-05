package bg

import (
	"strings"
	"testing"
)

// A message that misdescribes the data is not a style problem: an operator who
// reads "128 models are priced at 0" and then runs
//
//	WHERE unit_price_in_per_1m = 0
//
// finds zero rows and concludes the check is broken. Measured 2026-10-05 on the
// real database: 145 routable per_token bindings, of which 145 have BOTH price
// columns NULL and 0 carry a literal 0 — the "0" in the old wording existed only
// because the query COALESCE'd NULL to 0 for its own NULL-safe comparison.
//
// The two populations need OPPOSITE fixes (fill in a missing price vs. decide
// whether a quoted 0 is genuine), so conflating them is worse than cosmetic.
//
// This is a source-shape ratchet on the alert wording, not a behavioural test:
// the defect being guarded IS the wording. It runs without a database.

func supplierPriceCheckSQL(t *testing.T) string {
	t.Helper()
	for _, d := range AllHealthChecks() {
		if d.CheckID == "supplier_price_missing_from_cost" {
			return d.Query
		}
	}
	// NOT a skip: if the check is renamed or dropped, this file's claim that
	// the wording is guarded becomes unverifiable, and a silent skip turns the
	// ratchet into a constant-green assertion.
	t.Fatalf("check supplier_price_missing_from_cost not found in AllHealthChecks(); " +
		"this test cannot verify what it claims to")
	return ""
}

// stripSQLComments removes `--` line comments. Without it this ratchet would
// match the banned phrases inside the comments that DOCUMENT the defect — which
// is exactly the false positive that showed up the first time this test ran:
// the phrase survived only as commentary, while the alert no longer emitted it.
// A ratchet that cannot tell prose from payload is not a ratchet.
func stripSQLComments(sql string) string {
	lines := strings.Split(sql, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func TestSupplierPriceMissingFromCost_SeparatesNullFromLiteralZero(t *testing.T) {
	// The banned/required phrase checks run against the SQL with its comments
	// stripped: what matters is what the alert SAYS, not what the query's
	// documentation says about what it used to say.
	sql := stripSQLComments(supplierPriceCheckSQL(t))

	// 1. The conflated phrasings that this defect actually shipped with. Each
	//    one describes a value that only exists because of the COALESCE above.
	for _, banned := range []string{
		"are priced at 0",
		"are both 0",
		"in_per_1m / unit_price_out_per_1m are both 0",
	} {
		if strings.Contains(sql, banned) {
			t.Errorf("alert wording still conflates a NULL price with a literal 0: found %q. "+
				"A NULL price must be reported as never filled in; quoting it as 0 sends the "+
				"operator to search for rows that do not exist", banned)
		}
	}

	// 2. Both populations must be named, so the operator can act on each.
	for _, required := range []string{
		"NULL",           // the missing-price population
		"literal 0",      // the quoted-zero population
		"price_absent_n", // the count of the former
		"price_zero_n",   // the count of the latter
		"raw_in",         // the un-COALESCEd column must reach the output
		"raw_out",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("query must carry %q so the report can separate missing prices from "+
				"quoted zeros; without it the two are reported as one population", required)
		}
	}

	// 3. The COALESCE must stay: it is what makes the "> 0" comparison NULL-safe.
	//    Removing it would make `p_in > 0` NULL for unpriced rows, `NOT priced`
	//    would go NULL, and the check would silently report nothing. So this
	//    ratchet pins that the fix did NOT "solve" the wording by deleting the
	//    COALESCE, which would trade a wrong message for a dead alert.
	if !strings.Contains(sql, "COALESCE(cmb.unit_price_in_per_1m, 0)") {
		t.Error("the COALESCE on p_in must stay: it is the NULL-safe guard that makes the " +
			"'> 0' comparison true for unpriced rows instead of NULL")
	}
}
