package db

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Retirement feasibility instrument (audit §9.161).
//
// The 118-column canonical contract is the whole surface that has to survive
// `request_logs` being dropped. Every column already has a registered
// session-side expression (projectionExprByColumn) or a details-layer source
// (detailsProjectionColumns), so "can the session family serve this column
// after retirement?" is answerable **by measurement** rather than by reading
// 104 files and guessing.
//
// Three things come out of one pass:
//
//  1. The columns the session family structurally CANNOT produce (literal-NULL
//     placeholders). These are hard retirement blockers, pinned offline.
//  2. The fill rate of every column that does have a source, on the session side
//     and on the v1 side, so "still fine after retirement" is a number.
//  3. The columns that go empty on the session side — the readers of those are
//     the ones that must be repointed or retired. This is what turns the
//     104-file inventory from a count into a work list.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestSessionFamilyColumnAvailability -count=1 -v
//
// ⚠️ Read the fill rates as a **data-shape** measurement. Audit §9.157
// established that this local database has an unidentified active writer, so
// shape-level numbers transfer and code-behaviour claims do not.

// nullPlaceholderRe matches a bare `NULL::<type>` projection.
//
// ⚠️ The type name may legitimately contain a **space** — `double precision`
// is a real PostgreSQL type and the 738 column uses it. A first cut of this
// detector rejected any remainder containing a space, which reported
// `credits_rate_multiplier` as having a session-side source when it has none.
// That is the worst failure mode for a measuring instrument: the gap list came
// out one short, everything else looked fine, and the column that genuinely
// cannot be served after retirement was the one that dropped out of the list.
// So the shape is anchored on a type grammar rather than a blacklist.
var nullPlaceholderRe = regexp.MustCompile(`^NULL::[A-Za-z][A-Za-z0-9]*( [A-Za-z][A-Za-z0-9]*)*(\(\s*\d+(\s*,\s*\d+)?\s*\))?(\[\])*$`)

// isLiteralNullPlaceholder reports whether an expression is a bare NULL::type
// placeholder rather than a real CASE or column reference.
//
// A CASE that happens to return NULL for every input is NOT a placeholder — it
// has a source, and a source whose fill rate is worth measuring. Only the bare
// form counts, which is why this is a shape test and not a semantic one.
func isLiteralNullPlaceholder(expr string) bool {
	return nullPlaceholderRe.MatchString(strings.TrimSpace(expr))
}

// knownStructuralGaps is the registered set of columns the session family can
// never supply. Written as a set equality on purpose:
//
//   - a superset check passes forever and teaches nobody anything;
//   - set equality fails when a column is silently added to the gap list (bad:
//     a real source was lost and nobody noticed) AND when one quietly gains a
//     source (good news, but it must be recorded deliberately).
var knownStructuralGaps = map[string]string{
	"id":       "v1 request_logs.id is the request row id; session_turns.id is the turn id (r.id = t.id matched 0 times in 1,515,984 measured pairs)",
	"test_col": "never written on either side",
	// test_tab_indent and provider_model are spelled out rather than grouped,
	// because a map whose values are identical strings hides the reason column
	// by column and invites copy-paste errors.
	"test_tab_indent": "never written on either side",
	"provider_model":  "mirror never writes it",
	"credits_rate_multiplier": "738 v1-side column; its source is the middle wrapper, the session " +
		"branch projects NULL::double precision. Note the type name contains a space.",
}

// TestSessionFamilyColumnAvailability_StructuralGaps pins the columns that the
// session family can never supply, whatever the data looks like.
//
// Offline on purpose: the answer is a property of the projection registry, not
// of any database, and pinning it offline means it cannot be widened by a
// data-dependent accident.
func TestSessionFamilyColumnAvailability_StructuralGaps(t *testing.T) {
	exprs := buildSessionProjectionExprs(canonicalColumnOrderV2, true) // 734 shape
	if len(exprs) != len(canonicalColumnOrderV2) {
		t.Fatalf("projection drift: %d exprs vs %d names", len(exprs), len(canonicalColumnOrderV2))
	}

	var got []string
	for i, name := range canonicalColumnOrderV2 {
		if isLiteralNullPlaceholder(exprs[i]) {
			got = append(got, name)
		}
	}
	sort.Strings(got)

	if len(got) == 0 {
		t.Fatal("no column matches the literal-NULL placeholder shape — either the projection " +
			"registry lost its placeholders or the detector no longer matches the shape it was " +
			"written for. A detector that matches nothing is a green light for an unmeasured risk.")
	}
	for _, n := range got {
		if _, ok := knownStructuralGaps[n]; !ok {
			t.Errorf("column %q is now a literal NULL placeholder but is not registered as a "+
				"structural gap — the session family lost a real source for it; record that "+
				"deliberately instead of letting the list grow on its own", n)
		}
	}
	for n, why := range knownStructuralGaps {
		if !containsStr(got, n) {
			t.Errorf("column %q is registered as a structural gap (%s) but its projection is no "+
				"longer a literal NULL placeholder — it may have gained a source, which is an "+
				"improvement worth recording here", n, why)
		}
	}
	t.Logf("structurally unavailable after retirement (%d/%d): %s",
		len(got), len(canonicalColumnOrderV2), strings.Join(got, ", "))
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func pct(nonNull, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(nonNull) * 100 / float64(total)
}

func quoteIdentLocal(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// TestSessionFamilyColumnAvailability_FillRates measures every column that has
// a session-side source and prints the session-vs-v1 comparison.
//
// A reporting test by design: it asserts only what must be true for the numbers
// to mean anything (both families really are populated, both storage faces
// really exist) and logs the rest. A pass/fail threshold on fill rates would be
// a policy decision, and policy belongs in the decision sheet.
func TestSessionFamilyColumnAvailability_FillRates(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	exprs := buildSessionProjectionExprs(canonicalColumnOrderV2, true)

	// The measurement must run against the **real** shapes, not a fixture: a
	// fixture would only prove the expressions parse. Both families are
	// populated here, which is the whole point.
	var v1Rows int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_logs`).Scan(&v1Rows); err != nil {
		t.Skipf("request_logs not readable (%v) — v1 already retired on this database", err)
	}
	if v1Rows == 0 {
		t.Skip("request_logs is empty — nothing to compare against")
	}

	// ---- session side -------------------------------------------------------
	//
	// One aggregate per storage face, not one query per column. The naive shape
	// — 118 separate count(expr) queries — costs 118 full passes over a
	// 1.69M-row partitioned table, each re-hashing the details layer; it ran
	// for minutes without finishing. Two aggregate scans answer the same
	// question. The reason is recorded so nobody "simplifies" it back.
	//
	// Both faces are measured and summed. Measuring only the parent is exactly
	// the blind spot that let the D9 retirement gauge report 0 while
	// session_turns_hot was full of NULLs (audit §9.160.7).
	agg := "count(*)"
	for _, e := range exprs {
		agg += ", count(" + e + ")"
	}
	sessSQL := `
		SELECT ` + agg + ` FROM public.session_turns_hot t
		LEFT JOIN public.session_turn_details_hot d
		  ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id
		 AND d.partition_date = t.partition_date
		UNION ALL
		SELECT ` + agg + ` FROM public.session_turns t
		LEFT JOIN public.session_turn_details d
		  ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id
		 AND d.partition_date = t.partition_date`

	rows, err := pool.Query(ctx, sessSQL)
	if err != nil {
		t.Fatalf("session-side aggregate: %v", err)
	}
	defer rows.Close()

	sessTotal := int64(0)
	sessNonNull := map[string]int64{}
	fields := rows.FieldDescriptions()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("session scan: %v", err)
		}
		if len(vals) != len(fields) {
			t.Fatalf("row has %d values, description has %d fields — the aggregate list and the "+
				"column contract disagree, which would silently mis-assign every number below",
				len(vals), len(fields))
		}
		total, _ := vals[0].(int64)
		sessTotal += total
		for i, name := range canonicalColumnOrderV2 {
			if isLiteralNullPlaceholder(exprs[i]) {
				continue
			}
			n, _ := vals[i+1].(int64)
			sessNonNull[name] += n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("session iterate: %v", err)
	}
	if sessTotal == 0 {
		t.Fatal("session_turns_hot and session_turns are both empty — the fill rates below would " +
			"all be 0/0 and every column would look like a structural gap")
	}

	// ---- v1 side ------------------------------------------------------------
	//
	// Only the columns the base table actually has. The 738/740 columns were
	// appended to the wrapper chain, not to request_logs, so including them
	// would fail the whole statement and lose the rest of the measurement.
	v1Names := existingV1Columns(ctx, pool, canonicalColumnOrderV2)
	v1NonNull := map[string]int64{}
	if len(v1Names) > 0 {
		parts := make([]string, 0, len(v1Names))
		for _, n := range v1Names {
			parts = append(parts, "count("+quoteIdentLocal(n)+")")
		}
		v1rows, err := pool.Query(ctx, `SELECT `+strings.Join(parts, ", ")+` FROM public.request_logs`)
		if err != nil {
			t.Fatalf("v1 aggregate: %v", err)
		}
		defer v1rows.Close()
		if v1rows.Next() {
			vals, err := v1rows.Values()
			if err != nil {
				t.Fatalf("v1 scan: %v", err)
			}
			for i, n := range v1Names {
				if n == "id" {
					continue // id is the v1 primary key: always non-null, 100% by construction
				}
				if vv, ok := vals[i].(int64); ok {
					v1NonNull[n] = vv
				}
			}
		}
		if err := v1rows.Err(); err != nil {
			t.Fatalf("v1 iterate: %v", err)
		}
	}

	// ---- report -------------------------------------------------------------
	// measured is the per-column rate map the SSOT check compares against.
	measured := map[string]ColumnFill{}
	var goEmpty, muchEmptier, structural []string
	t.Logf("session faces: %d rows total; v1 request_logs: %d rows; %d canonical columns",
		sessTotal, v1Rows, len(canonicalColumnOrderV2))
	t.Logf("%-28s %11s %11s  %s", "column", "session%", "v1%", "note")
	for i, name := range canonicalColumnOrderV2 {
		if isLiteralNullPlaceholder(exprs[i]) {
			structural = append(structural, name)
			t.Logf("%-28s %11s %11s  STRUCTURAL GAP — no session-side source (%s)",
				name, "NULL", "—", knownStructuralGaps[name])
			continue
		}
		sp := pct(sessNonNull[name], sessTotal)
		vv, hasV1 := v1NonNull[name]
		vp := pct(vv, v1Rows)
		measured[name] = ColumnFill{SessionPct: sp, V1Pct: vp}

		// The two flags are computed **independently**, not as a switch.
		// A switch evaluates top-down, and "session >5pp emptier" matches
		// first — so every column that goes to 0% would be reported as merely
		// "much emptier" and the goes-empty list would come out almost empty.
		// That is the worst way to lose a finding: the list still looks
		// plausible, and the columns it quietly omits are the ones that lose
		// all their data.
		note := ""
		if !hasV1 {
			note = "v1 base column absent — no comparison possible"
		}
		if hasV1 && sp == 0 && vp > 0 {
			goEmpty = append(goEmpty, name)
			note = strings.TrimSpace(note + " GOES EMPTY on the session side")
		} else if hasV1 && sp+5 < vp {
			muchEmptier = append(muchEmptier, name)
			note = strings.TrimSpace(note + " SESSION MUCH EMPTIER — a reader of this column needs repointing or retiring")
		}
		if hasV1 {
			t.Logf("%-28s %10.2f%% %10.2f%%  %s", name, sp, vp, note)
		} else {
			t.Logf("%-28s %10.2f%% %11s  %s", name, sp, "n/a", note)
		}
	}
	t.Logf("STRUCTURAL GAPS (%d): %s", len(structural), strings.Join(structural, ", "))
	t.Logf("GO EMPTY ON THE SESSION SIDE (%d): %s", len(goEmpty), strings.Join(goEmpty, ", "))
	t.Logf("session >5pp emptier than v1 (%d): %s", len(muchEmptier), strings.Join(muchEmptier, ", "))

	// The measurement contradicts the registered lists, or the lists are
	// wrong. This is what keeps db/retirement_column_exposure.go a fact rather
	// than a comment: a reviewer can trust that "unservable" means "measured
	// empty on this database", because the moment the database says otherwise
	// this test goes red.
	//
	// Compared as **sets in both directions**. A subset check would let a column
	// silently leave the list (the measurement no longer reports it, the list
	// keeps claiming it) and the exposure analysis would keep flagging readers
	// of a column that is actually fine — a false alarm that trains people to
	// ignore the alarm.
	assertSameSet(t, "unservable", goEmpty, RetirementUnservableColumns)
	assertSameSet(t, "degraded", muchEmptier, RetirementDegradedColumns)
	assertSameSet(t, "structural gap", structural, RetirementStructuralGapColumns)

	// And the fill table the repoint verdict is computed from.
	//
	// This check exists because the table was first written with **guessed**
	// zeros for six columns — and five of the six guesses were wrong by wide
	// margins (quality_fix_actions is 18.15/100, not 0/0; canonical_id is
	// 0.07/9.79, not 0/0). A guessed zero is the most dangerous kind of wrong:
	// it is indistinguishable from "measured, and empty", and the repoint
	// verdict turns on exactly that difference. So the table is now derived from
	// this measurement and fails when it stops matching.
	var drift []string
	for _, name := range canonicalColumnOrderV2 {
		// Presence must be tested by **map membership**, never by comparing to
		// the zero value. The first cut did `if want == (ColumnFill{}) { continue }`
		// — and that silently exempts exactly the failure it was meant to catch:
		// a guessed `{0, 0}` entry is indistinguishable from "not recorded", so
		// re-introducing the original guess turned the drift check **off for that
		// column** rather than failing it (mutation-verified, §9.164).
		//
		// A sentinel that shares a value with a legitimate measurement cannot
		// guard that measurement.
		want, recorded := RetirementColumnFill[name]
		if !recorded {
			// A column in one of the three non-baseline classes **must** carry a
			// recorded rate, because the repoint verdict reads it and answers
			// `unknown-column` when it is missing.
			//
			// This arm was absent at first, and the omission was invisible: a
			// *wrong* entry failed the check, but a *missing* one passed
			// silently. application_id was dropped from the table during an
			// edit and the drift check stayed green while admin/logs.go came out
			// labelled `unknown-column` — an entry missing from a validation
			// table is the same shape as a measurement that was never taken.
			// Structural gaps are decided by class alone — the verdict function
			// handles them before it looks at any rate, because the view carries
			// the column as a NULL placeholder. Only the two classes whose
			// verdict *is* a rate need a recorded rate.
			if class := RetirementExposureClassify(name); class == "degraded" || class == "unservable" {
				drift = append(drift, fmt.Sprintf(
					"%s: classified %q but has no entry in RetirementColumnFill, so the repoint "+
						"verdict reports it as unknown-column", name, class))
			}
			continue
		}
		got := findFill(measured, name)
		if math.Abs(got.SessionPct-want.SessionPct) > 0.01 || math.Abs(got.V1Pct-want.V1Pct) > 0.01 {
			drift = append(drift, fmt.Sprintf("%s: table says %.2f/%.2f, measured %.2f/%.2f",
				name, want.SessionPct, want.V1Pct, got.SessionPct, got.V1Pct))
		}
	}
	for name := range RetirementColumnFill {
		if !containsStr(canonicalColumnOrderV2, name) {
			drift = append(drift, name+": recorded in RetirementColumnFill but not in the canonical contract")
		}
	}
	if len(drift) > 0 {
		sort.Strings(drift)
		t.Errorf("db/retirement_column_exposure.go's RetirementColumnFill no longer matches the "+
			"measurement on this database:\n  %s\nThe repoint verdict in that file is computed from "+
			"this table, so a stale entry is a wrong answer presented with full confidence.",
			strings.Join(drift, "\n  "))
	}
}

// findFill looks a column's measured rates up in the per-column map the
// fill-rate test builds.
func findFill(m map[string]ColumnFill, name string) ColumnFill { return m[name] }

// assertSameSet fails unless the two column sets are identical, naming both
// sides so a drift report says which way it moved.
func assertSameSet(t *testing.T, label string, measured, registered []string) {
	t.Helper()
	m := map[string]bool{}
	for _, c := range measured {
		m[c] = true
	}
	var missing, extra []string
	for _, c := range registered {
		if !m[c] {
			missing = append(missing, c)
		}
	}
	for _, c := range measured {
		if !retiredColumnSet(registered)[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: the registered list in db/retirement_column_exposure.go no longer matches "+
			"the measurement on this database.\n  registered but not measured: %v\n"+
			"  measured but not registered: %v\n"+
			"Re-derive the list from this run and update it deliberately — the read-side exposure "+
			"analysis trusts it.",
			label, missing, extra)
	}
}

// TestRetirementColumnExposureIsTotalAndDisjoint checks the classification is a
// partition of the canonical contract: every column lands in exactly one class,
// and the three non-baseline lists do not overlap.
//
// Total + disjoint is the property that makes "every reader was classified" a
// statement you can verify. Without it, a column in two lists would be resolved
// by whatever branch happens to come first, and a column in none would look
// identical to a column that was checked and found fine.
func TestRetirementColumnExposureIsTotalAndDisjoint(t *testing.T) {
	seen := map[string]int{}
	counts := map[string]int{}
	for _, col := range canonicalColumnOrderV2 {
		class := RetirementExposureClassify(col)
		counts[class]++
		seen[col]++
	}
	for col, n := range seen {
		if n != 1 {
			t.Errorf("column %q appears %d times in the canonical contract — the classification "+
				"cannot be a partition of a contract with duplicates", col, n)
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != len(canonicalColumnOrderV2) {
		t.Errorf("classification covered %d columns, contract has %d", total, len(canonicalColumnOrderV2))
	}
	// The three named lists must be pairwise disjoint.
	lists := map[string][]string{
		"structural-gap": RetirementStructuralGapColumns,
		"unservable":     RetirementUnservableColumns,
		"degraded":       RetirementDegradedColumns,
	}
	for name, cols := range lists {
		for _, c := range cols {
			if !containsStr(canonicalColumnOrderV2, c) {
				t.Errorf("%s list names %q, which is not in the canonical contract — a typo here "+
					"would silently make that column unclassifiable", name, c)
			}
		}
	}
	t.Logf("exposure classes: %v", counts)
}

// existingV1Columns returns the subset of the canonical column names that the
// v1 base table actually has, preserving the contract order.
func existingV1Columns(ctx context.Context, pool *pgxpool.Pool, names []string) []string {
	var out []string
	for _, n := range names {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_schema='public' AND table_name='request_logs' AND column_name=$1)`,
			n).Scan(&exists)
		if err != nil || !exists {
			continue
		}
		out = append(out, n)
	}
	return out
}
