package db

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Population measurement for `session_dim` coverage (audit §9.234).
//
// # The number this file exists to stop people from quoting
//
// "How much of the session family has a `session_dim` row?" is a question that
// looks like a data-completeness metric and is not one. Measured 2026-10-05 on
// the local real database over the full family (parent ∪ hot, 1,693,480 turn
// rows):
//
//	overall dim coverage          : 54.28%
//	user sessions (not `sys:%`)   : 97.78%   (919,185 / 940,055)
//	internal traffic (`sys:%`)    :  0.00%   (     0 / 753,425)
//
// The 54.28% headline is arithmetically correct and completely useless: it
// averages a population that is covered at 97.78% with one that is covered at
// exactly zero, and the reason the second is zero is **by design** — internal
// probe traffic is not a user session and `session_dim` is the user-session
// dimension table. Half the apparent "missing data" is synthetic health-check
// traffic that has no business having a session row.
//
// This is not hypothetical. §9.234 first measured it the naive way, got 49.47%
// on the parent alone, and nearly recorded that as a large data-completeness
// gap. What actually produced the 49% was **distribution, not loss**: 21,295
// internal sessions (2.5% of sessions) carried 774,293 turn rows (45.7% of
// turns) with a maximum of 53,851 turns in one probe session. Splitting by
// `is_auto_request` showed 97.31% of those rows were
// `origin_actor ∈ {active-probe-worker, node-probe-worker, probe-service}`.
//
// # The rule this encodes
//
// `session_dim` coverage is a **user-session** metric. It is only meaningful
// after splitting on the read layer's own "is this a user session" predicate,
// and the split is not cosmetic — the two halves differ by 97.78 points.
//
// # Why the thresholds are design invariants, not pinned measurements
//
// Both thresholds state what the schema is *for*, with wide margins against
// what is measured (0.00% vs a 1% bar; 97.78% vs a 90% bar). A gate that
// pinned the exact measured number would go red on any unrelated change and
// would train people to re-baseline it without reading it — the failure mode
// this repo has already paid for twice (§9.209's precision constant, §9.216's
// work_type). The margins are the point: they are loose enough to survive
// normal drift and tight enough that a real regression in either direction
// turns them red.
//
// Two failure directions are caught, and they are not symmetric:
//
//	internal coverage RISING  → probes started getting session rows, which
//	                             would mean the `sys:` classification stopped
//	                             meaning "not a user session";
//
//	user coverage FALLING     → real user sessions are losing their dimension
//	                             row, which is actual data loss and the thing
//	                             worth waking someone for.

// SessionDimCoverage is one measured population of turn rows.
type SessionDimCoverage struct {
	Label   string
	Turns   int64
	WithDim int64
}

// PP returns the coverage in percent. A zero-row population reports 0 rather
// than dividing by zero; callers must check Turns before trusting the number,
// which is why the verdict function below does exactly that.
func (c SessionDimCoverage) PP() float64 {
	if c.Turns == 0 {
		return 0
	}
	return 100 * float64(c.WithDim) / float64(c.Turns)
}

// internalSessionDimBarPP is the highest internal-traffic dim coverage that
// still means "session_dim is a user-session table". The read layer NULLs
// `sys:` ids out of gw_session_id precisely so they cannot join to a session;
// a session_dim row for internal traffic would mean that contract was
// abandoned somewhere. Measured 0.00%, so the bar has an order of magnitude of
// headroom in the correct direction and no headroom at all in the wrong one.
const internalSessionDimBarPP = 1.0

// userSessionDimFloorPP is the lowest user-session dim coverage accepted before
// the population is treated as lossy. Measured 97.78%. The 90% floor is not a
// target — it is the point at which "some user sessions never get a dimension
// row" has become large enough to be a bug rather than a residue.
const userSessionDimFloorPP = 90.0

// SessionDimCoverageVerdict judges the two measured populations against the
// two design bars.
//
// Returned as a list rather than a bool so the gate message can carry both
// failures at once. Two independent populations failing at the same time is the
// informative case (something changed the classification itself), and a bool
// would force a re-run to see the second one.
//
// The empty-population arms return no reason rather than a failure. An empty
// deployment has lost no data, and a gate that fires there is a gate people
// learn to ignore — the same argument BackfillBlocksRetirement makes for
// `sessionTotal == 0`.
func SessionDimCoverageVerdict(internal, user SessionDimCoverage) []string {
	var reasons []string

	if user.Turns == 0 {
		// Nothing to judge. Not a reason, but also not a silent pass — the
		// caller logs it.
		return nil
	}
	if pp := user.PP(); pp < userSessionDimFloorPP {
		reasons = append(reasons, user.Label+": only "+formatPP(pp)+" of "+itoa(user.Turns)+
			" turn rows have a session_dim row (floor "+formatPP(userSessionDimFloorPP)+
			") — user sessions are losing their dimension row")
	}

	if internal.Turns == 0 {
		// No internal traffic on this deployment. The internal arm cannot be
		// evaluated; say so rather than pretending 0% was measured.
		return reasons
	}
	if pp := internal.PP(); pp > internalSessionDimBarPP {
		reasons = append(reasons, internal.Label+": "+formatPP(pp)+" of "+itoa(internal.Turns)+
			" turn rows have a session_dim row (bar "+formatPP(internalSessionDimBarPP)+
			") — internal `sys:%` traffic should never be a user session; "+
			"session_dim coverage is only meaningful if this stays near zero")
	}
	return reasons
}

func formatPP(pp float64) string {
	return strconv.FormatFloat(pp, 'f', 2, 64) + "%"
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// sessionDimCoverageQuery measures both storage faces separately and labels
// each, so the caller can prove both were counted.
//
// ⚠ **This is the §9.160.7 trap, made mechanical.** `session_turns` is a
// PARTITIONED parent and `session_turns_hot` is a separate plain table. They
// are DISJOINT (measured: 1,691,590 ∩ 1,890 = 0; union 1,693,480). So a query
// that reads only the parent silently omits every row currently in the hot
// table — and the hot table is where the NEWEST rows are (measured: hot holds
// 2026-10-04→10-05, 100% user traffic, 0% probe).
//
// That is the worst possible thing to omit: the rows you would most want to
// look at when asking "is current data OK" are exactly the ones that go
// missing. §9.234's own first measurement made this mistake and read 1,691,590
// where the family holds 1,693,480.
//
// The classification uses the read layer's own predicate rather than a
// restatement of it, and deliberately NOT `is_auto_request` (see
// sessionIsInternalSessionIDExpr for the measured difference between the two).
//
// ⚠ `expected_total` is computed by a **scalar subquery inside this same
// statement**, and that is load-bearing rather than stylistic. The first
// version of this gate read the rows in one query and counted the two faces in
// a second one, and it went red in the full db suite while passing in
// isolation — because this local database has an active writer (§9.157's
// unclosed note), and `session_turns_hot` gained rows between the two
// measurements: 1,890 → 1,892 → 1,894 across three runs. The assertion was
// comparing two different instants and blaming the query for it.
//
// One statement means one snapshot, so the comparison is exact. Moving the
// count back into a second query would reintroduce the race, and it would
// reintroduce it *quietly* — the gate would be red only when the suite runs
// everything together.
const sessionDimCoverageQuery = `
SELECT 'parent' AS face, s.session_id, (d.gw_session_id IS NOT NULL) AS has_dim,
       (SELECT count(*) FROM public.session_turns)
     + (SELECT count(*) FROM public.session_turns_hot) AS expected_total
  FROM public.session_turns s
  LEFT JOIN public.session_dim d ON d.gw_session_id = s.session_id
UNION ALL
SELECT 'hot', s.session_id, (d.gw_session_id IS NOT NULL),
       (SELECT count(*) FROM public.session_turns)
     + (SELECT count(*) FROM public.session_turns_hot)
  FROM public.session_turns_hot s
  LEFT JOIN public.session_dim d ON d.gw_session_id = s.session_id`

// TestSessionDimCoverageIsAUserSessionMetric measures the two populations and
// asserts the two design bars.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestSessionDimCoverageIsAUserSessionMetric -count=1 -v
//
// The face-completeness assertion is the part that is easy to skip and must
// not be: it compares the sum of the per-face rows against an independent
// UNION ALL count. Drop either face from the query above and the two numbers
// stop agreeing, so the gate reports "you are not measuring the whole family"
// instead of reporting a confident fraction of half the data.
func TestSessionDimCoverageIsAUserSessionMetric(t *testing.T) {
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

	rows, err := pool.Query(ctx, sessionDimCoverageQuery)
	if err != nil {
		t.Fatalf("coverage query: %v", err)
	}
	defer rows.Close()

	var internal, user SessionDimCoverage
	internal.Label, user.Label = "internal `sys:%` traffic", "user sessions"
	faceTurns := map[string]int64{}
	total := int64(0)
	var expectedTotal int64
	for rows.Next() {
		var face, sessionID string
		var hasDim bool
		var expected int64
		if err := rows.Scan(&face, &sessionID, &hasDim, &expected); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if expectedTotal == 0 {
			expectedTotal = expected
		} else if expected != expectedTotal {
			t.Fatalf("the two faces of the query disagree about the family size (%d vs %d) — "+
				"they must be read in ONE statement; two statements are two snapshots and this "+
				"database has an active writer (audit §9.234)", expectedTotal, expected)
		}
		faceTurns[face]++
		total++
		c := &user
		if isInternalSessionID(sessionID) {
			c = &internal
		}
		c.Turns++
		if hasDim {
			c.WithDim++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if total == 0 {
		t.Skip("session family is empty on this database — nothing to measure")
	}

	// ---- face completeness (§9.160.7) ---------------------------------------
	// expectedTotal comes from the tables directly, inside the same statement
	// that produced `total`; see the query comment for why that matters.
	if expectedTotal != total {
		t.Fatalf("face coverage is incomplete: query returned %d rows but the family holds %d "+
			"(per-face: %v) — a storage face is not being read. `session_turns` is a PARTITIONED "+
			"parent and `session_turns_hot` is a separate, DISJOINT table holding the NEWEST rows; "+
			"reading only the parent silently omits current data (audit §9.160.7 / §9.234)",
			total, expectedTotal, faceTurns)
	}

	overall := SessionDimCoverage{Label: "overall", Turns: total, WithDim: internal.WithDim + user.WithDim}
	t.Logf("session_dim coverage — overall %s is NOT a completeness metric; "+
		"split on the read layer's `sys:` predicate:",
		formatPP(overall.PP()))
	t.Logf("  %s: %s (%d/%d)", internal.Label, formatPP(internal.PP()), internal.WithDim, internal.Turns)
	t.Logf("  %s:     %s (%d/%d)", user.Label, formatPP(user.PP()), user.WithDim, user.Turns)
	t.Logf("  faces: %v (total %d)", faceTurns, total)

	if user.Turns == 0 {
		t.Skip("no user turns on this database — the population split cannot be judged")
	}
	for _, reason := range SessionDimCoverageVerdict(internal, user) {
		t.Error(reason)
	}
}

// isInternalSessionID mirrors sessionIsInternalSessionIDExpr's predicate in Go.
// It is spelled out separately on purpose: the gate must be able to classify a
// scanned row without re-evaluating SQL per row, and TestInternalSessionIDExpr
// pins the two against each other.
func isInternalSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, "sys:")
}

// TestInternalSessionIDExpr pins the gate's classifier to the expression the
// read layer actually ships.
//
// A gate whose classifier has drifted from production is worse than no gate:
// it would measure a population the product does not serve. Two separate pins,
// because they fail in different ways:
//
//  1. VALUE level — the shipped projection must evaluate to exactly the
//     constant's value. This is what catches actual drift: if someone changes
//     the projection's predicate without changing the constant, the two stop
//     being equal and this goes red.
//
//  2. SOURCE level — the projection slice must *reference the identifier*, not
//     carry a second copy of the literal.
//
// The second pin exists because the first one is **near-tautological for an
// identical copy**, and that was not a theoretical worry: mutation M7 replaced
// the identifier in the projection with the byte-identical literal, the value
// check passed (of course — the strings are equal), and the gate stayed green.
// An SSOT constant that no check can tell from a copy is not an SSOT.
//
// The source check is scoped to the `projectionExprsV2` block rather than the
// whole file, so it does not need comment stripping (the literal is quoted
// verbatim in a doc comment elsewhere in the same file). Its one known
// false-positive shape is a comment *inside the slice literal* quoting the
// expression; the failure message says so.
func TestInternalSessionIDExpr(t *testing.T) {
	const want = "session_id LIKE 'sys:%'"
	if !strings.Contains(sessionIsInternalSessionIDExpr, want) {
		t.Fatalf("sessionIsInternalSessionIDExpr no longer contains %q; the read layer and the "+
			"population gate would classify differently. Got: %s", want, sessionIsInternalSessionIDExpr)
	}
	if !strings.Contains(strings.Join(projectionExprsV2, "\n"), sessionIsInternalSessionIDExpr) {
		t.Error("the session-family projection no longer produces sessionIsInternalSessionIDExpr — " +
			"production and the gate have diverged")
	}

	// ---- source level: the identifier, not a second copy of the literal ----
	src, err := os.ReadFile("request_logs_view_schema.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	const startMarker = "var projectionExprsV2 = []string{"
	i := strings.Index(string(src), startMarker)
	if i < 0 {
		t.Fatalf("%s not found — the source check needs updating if the projection moved", startMarker)
	}
	rest := string(src)[i+len(startMarker):]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatal("could not find the end of the projectionExprsV2 block")
	}
	block := rest[:j]

	if !strings.Contains(block, "sessionIsInternalSessionIDExpr") {
		t.Errorf("the projectionExprsV2 block does not reference sessionIsInternalSessionIDExpr. " +
			"If the expression was inlined back as a literal, the constant is no longer the " +
			"single source and the value check above can no longer tell the two apart (M7).")
	}
	if strings.Contains(block, sessionIsInternalSessionIDExpr) {
		t.Errorf("the projectionExprsV2 block carries a second copy of the literal "+
			"(%q) — keep exactly one, the const. A duplicate here is the drift this "+
			"constant exists to prevent. (If this is a comment quoting the expression, "+
			"move the comment out of the slice literal.)", sessionIsInternalSessionIDExpr)
	}
}

// TestSessionDimCoverageVerdict_ControlPair is the control pair for
// SessionDimCoverageVerdict.
//
// A gate that has only ever been observed passing is indistinguishable from a
// gate that cannot fail. Both directions are asserted, plus the three edges
// that decide whether it is usable in CI at all: no user rows, no internal
// rows, and each bar approached from both sides.
//
// The internal-bar cases matter more than the user-floor cases. The measured
// internal coverage is exactly 0.00%, so the "probe traffic started getting
// session rows" direction has **no real-world example** to copy — these cases
// are the only place that direction is ever exercised, which is exactly why a
// missing negative control would leave the most important arm unverified.
func TestSessionDimCoverageVerdict_ControlPair(t *testing.T) {
	cases := []struct {
		name     string
		internal SessionDimCoverage
		user     SessionDimCoverage
		wantFire bool
	}{
		{
			// Measured on the local real database, 2026-10-05.
			name:     "measured shape passes",
			internal: SessionDimCoverage{Label: "internal", Turns: 753425, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 940055, WithDim: 919185},
			wantFire: false,
		},
		{
			// The arm with no real-world example: internal traffic acquiring
			// session rows means the `sys:` contract was abandoned.
			name:     "internal traffic started getting session rows",
			internal: SessionDimCoverage{Label: "internal", Turns: 753425, WithDim: 20000},
			user:     SessionDimCoverage{Label: "user", Turns: 940055, WithDim: 919185},
			wantFire: true,
		},
		{
			name:     "user sessions losing their dimension row",
			internal: SessionDimCoverage{Label: "internal", Turns: 753425, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 940055, WithDim: 700000},
			wantFire: true,
		},
		{
			// Both at once is the informative case: something changed the
			// classification itself, not one population's coverage.
			name:     "both arms fire together",
			internal: SessionDimCoverage{Label: "internal", Turns: 100, WithDim: 100},
			user:     SessionDimCoverage{Label: "user", Turns: 100, WithDim: 1},
			wantFire: true,
		},
		{
			// Edge: an empty deployment has lost nothing. Firing here is how
			// gates get ignored, so this must NOT be a failure.
			name:     "empty deployment does not fire",
			internal: SessionDimCoverage{Label: "internal", Turns: 0, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 0, WithDim: 0},
			wantFire: false,
		},
		{
			// Edge: no internal traffic on this deployment. The internal arm
			// cannot be evaluated, and must not be reported as a pass.
			name:     "no internal traffic evaluates the user arm only",
			internal: SessionDimCoverage{Label: "internal", Turns: 0, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 100, WithDim: 1},
			wantFire: true,
		},
		{
			// Edge: internal coverage just under / just over the 1% bar.
			name:     "just under the internal bar",
			internal: SessionDimCoverage{Label: "internal", Turns: 100000, WithDim: 999},
			user:     SessionDimCoverage{Label: "user", Turns: 100, WithDim: 100},
			wantFire: false,
		},
		{
			name:     "just over the internal bar",
			internal: SessionDimCoverage{Label: "internal", Turns: 100000, WithDim: 1001},
			user:     SessionDimCoverage{Label: "user", Turns: 100, WithDim: 100},
			wantFire: true,
		},
		{
			// Edge: user coverage just under / just over the 90% floor.
			name:     "just under the user floor",
			internal: SessionDimCoverage{Label: "internal", Turns: 100, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 1000, WithDim: 899},
			wantFire: true,
		},
		{
			name:     "just over the user floor",
			internal: SessionDimCoverage{Label: "internal", Turns: 100, WithDim: 0},
			user:     SessionDimCoverage{Label: "user", Turns: 1000, WithDim: 901},
			wantFire: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reasons := SessionDimCoverageVerdict(tc.internal, tc.user)
			if got := len(reasons) > 0; got != tc.wantFire {
				t.Fatalf("fired = %v, want %v (reasons: %v)", got, tc.wantFire, reasons)
			}
			for _, r := range reasons {
				// A reason nobody can act on is a worse outcome than no
				// reason: it turns a real signal into noise.
				if len(r) < 40 {
					t.Errorf("reason %q is too short to tell an operator what to look at", r)
				}
			}
		})
	}
}

// TestSessionDimCoverageVerdictReportsBothArms pins the "both fire" case to two
// reasons rather than one.
//
// Returning a bool, or short-circuiting after the first reason, would make the
// both-at-once case indistinguishable from a single-population regression — and
// both-at-once is the shape that says the classification itself changed.
func TestSessionDimCoverageVerdictReportsBothArms(t *testing.T) {
	reasons := SessionDimCoverageVerdict(
		SessionDimCoverage{Label: "internal", Turns: 100, WithDim: 100},
		SessionDimCoverage{Label: "user", Turns: 100, WithDim: 1},
	)
	if len(reasons) != 2 {
		t.Fatalf("got %d reasons, want 2: %v", len(reasons), reasons)
	}
	// Assert one reason per population by membership, not by position. An
	// earlier version of this test asserted reasons[0]=="internal" and
	// reasons[1]=="user", and it went red for a real reason: the verdict
	// checks the user floor before the internal bar, so the positions are the
	// other way round. Position is an implementation detail; "one reason names
	// each population" is the property worth pinning.
	var sawInternal, sawUser bool
	for _, r := range reasons {
		switch {
		case strings.Contains(r, "internal"):
			sawInternal = true
		case strings.Contains(r, "user"):
			sawUser = true
		default:
			t.Errorf("reason names neither population, so an operator cannot tell which one moved: %q", r)
		}
	}
	if !sawInternal || !sawUser {
		t.Fatalf("want one reason naming each population, got internal=%v user=%v from %v",
			sawInternal, sawUser, reasons)
	}
}
