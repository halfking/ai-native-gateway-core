package admin

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ─────────────────────────────────────────────────────────────────────────
// fakes
// ─────────────────────────────────────────────────────────────────────────

// fakeOutcomeRow is a pgx.Row that hands back one canned value, or one canned
// error. Scan is the only method pgx.Row has, so this is the whole surface.
type fakeOutcomeRow struct {
	val   *time.Time
	err   error
	scans int
}

func (f *fakeOutcomeRow) Scan(dest ...any) error {
	f.scans++
	if f.err != nil {
		return f.err
	}
	if len(dest) != 1 {
		return errors.New("fakeOutcomeRow: expected exactly one destination")
	}
	p, ok := dest[0].(**time.Time)
	if !ok {
		return errors.New("fakeOutcomeRow: destination is not **time.Time")
	}
	*p = f.val
	return nil
}

var _ pgx.Row = (*fakeOutcomeRow)(nil)

type fakeOutcomeDB struct {
	row *fakeOutcomeRow
}

func (f fakeOutcomeDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return f.row
}

var _ outcomeFreshnessDB = fakeOutcomeDB{}

// ─────────────────────────────────────────────────────────────────────────
// the freshness rule
// ─────────────────────────────────────────────────────────────────────────

// TestQueryOutcomeFreshness pins every branch of the rule. The three that
// matter for the S4 stop-write are "live", "stale" and "absent"; the other
// two guard against a reader mistaking "no evidence" for "no failure".
func TestQueryOutcomeFreshness(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	undefinedTable := &pgconn.PgError{Code: "42P01", Message: `relation "request_logs_hot" does not exist`}

	cases := []struct {
		name       string
		row        *fakeOutcomeRow
		wantReason string
		wantStale  bool
		wantAvail  bool
		wantAge    *int64
	}{
		{
			name:       "fresh evidence settles normally",
			row:        &fakeOutcomeRow{val: timePtr(now.Add(-30 * time.Minute))},
			wantReason: outcomeReasonLive,
			wantStale:  false, wantAvail: true,
			wantAge: int64Ptr(1800),
		},
		{
			// Exactly at the horizon is NOT stale: the comparison is strictly
			// greater-than, so the boundary itself still settles.
			name:       "exactly at the horizon is not yet stale",
			row:        &fakeOutcomeRow{val: timePtr(now.Add(-outcomeSourceStaleAfter))},
			wantReason: outcomeReasonLive,
			wantStale:  false, wantAvail: true,
			wantAge: int64Ptr(int64(outcomeSourceStaleAfter / time.Second)),
		},
		{
			name:       "one second past the horizon is stale",
			row:        &fakeOutcomeRow{val: timePtr(now.Add(-outcomeSourceStaleAfter - time.Second))},
			wantReason: outcomeReasonStale,
			wantStale:  true, wantAvail: true,
			wantAge: int64Ptr(int64(outcomeSourceStaleAfter/time.Second) + 1),
		},
		{
			// This is the state S4 stop-write actually produces.
			name:       "frozen v1 reads as stale, not as an error",
			row:        &fakeOutcomeRow{val: timePtr(now.Add(-72 * time.Hour))},
			wantReason: outcomeReasonStale,
			wantStale:  true, wantAvail: true,
			wantAge: int64Ptr(72 * 3600),
		},
		{
			name:       "relation present but empty",
			row:        &fakeOutcomeRow{val: nil},
			wantReason: outcomeReasonNoRows,
			wantStale:  true, wantAvail: true,
		},
		{
			// The terminal retirement state. Must NOT be reported as a fault:
			// the endpoint has to keep answering.
			name:       "dropped v1 relation is absent, not query_failed",
			row:        &fakeOutcomeRow{err: undefinedTable},
			wantReason: outcomeReasonAbsent,
			wantStale:  true, wantAvail: false,
		},
		{
			// Not the terminal state, so `available` is false but the reason
			// keeps it distinguishable from a dropped relation.
			name:       "any other error stays stale and distinguishable",
			row:        &fakeOutcomeRow{err: errors.New("connection reset")},
			wantReason: outcomeReasonQueryFailed,
			wantStale:  true, wantAvail: false,
		},
		{
			// A future-dated row must not read as maximally fresh.
			name:       "future-dated evidence clamps age to zero",
			row:        &fakeOutcomeRow{val: timePtr(now.Add(2 * time.Hour))},
			wantReason: outcomeReasonLive,
			wantStale:  false, wantAvail: true,
			wantAge: int64Ptr(0),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := queryOutcomeFreshness(context.Background(), fakeOutcomeDB{row: tc.row}, now)
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.Stale != tc.wantStale {
				t.Errorf("stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if got.Available != tc.wantAvail {
				t.Errorf("available = %v, want %v", got.Available, tc.wantAvail)
			}
			if got.StaleAfterSeconds != int64(outcomeSourceStaleAfter/time.Second) {
				t.Errorf("stale_after_seconds = %d, want %d",
					got.StaleAfterSeconds, int64(outcomeSourceStaleAfter/time.Second))
			}
			switch {
			case tc.wantAge == nil && got.AgeSeconds != nil:
				t.Errorf("age_seconds = %d, want null", *got.AgeSeconds)
			case tc.wantAge != nil && got.AgeSeconds == nil:
				t.Errorf("age_seconds = null, want %d", *tc.wantAge)
			case tc.wantAge != nil && *got.AgeSeconds != *tc.wantAge:
				t.Errorf("age_seconds = %d, want %d", *got.AgeSeconds, *tc.wantAge)
			}
			// as_of must be absent exactly when there is no evidence to point at.
			if tc.row.err == nil && tc.row.val != nil && got.AsOf == nil {
				t.Errorf("as_of = null, want RFC3339 of %v", *tc.row.val)
			}
			if (tc.row.err != nil || tc.row.val == nil) && got.AsOf != nil {
				t.Errorf("as_of = %q, want null", *got.AsOf)
			}
		})
	}
}

// TestQueryOutcomeFreshnessScansExactlyOneColumn keeps the fake honest: if the
// query grew a second output column, Scan would fail and this catches it here
// rather than as a confusing runtime failure.
func TestQueryOutcomeFreshnessScansExactlyOneColumn(t *testing.T) {
	row := &fakeOutcomeRow{val: timePtr(time.Now())}
	_ = queryOutcomeFreshness(context.Background(), fakeOutcomeDB{row: row}, time.Now())
	if row.scans != 1 {
		t.Errorf("Scan called %d times, want 1 — the query must project exactly one column", row.scans)
	}
}

// TestOutcomeSourceReasonsAreClosed keeps the reason set closed. These strings
// are part of the API contract; adding one is fine, but it must be a decision
// recorded here rather than a stray literal in a handler.
func TestOutcomeSourceReasonsAreClosed(t *testing.T) {
	closed := map[string]bool{
		outcomeReasonLive:        true,
		outcomeReasonNoRows:      true,
		outcomeReasonStale:       true,
		outcomeReasonAbsent:      true,
		outcomeReasonQueryFailed: true,
	}
	got := queryOutcomeFreshness(
		context.Background(),
		fakeOutcomeDB{row: &fakeOutcomeRow{val: nil}},
		time.Now(),
	)
	if !closed[got.Reason] {
		t.Errorf("reason %q is not in the closed set", got.Reason)
	}
	if len(closed) != 5 {
		t.Errorf("closed set has %d entries, want 5 — update this test alongside the contract", len(closed))
	}
}

// TestOutcomeSourceStaleAfterMirrorsSettleAbandonAfter is the anti-drift guard.
//
// outcomeSourceStaleAfter is a hand-mirrored copy of bg.settleAbandonAfter.
// The copy is deliberate (admin must not import the bg worker), which makes it
// exactly the kind of constant that silently drifts: nobody changes both.
//
// It is parsed out of the bg source rather than imported, and it is parsed by
// VALUE (a time.Duration literal) rather than by matching the surrounding
// comment text — a reworded comment must not turn this guard red, and a changed
// duration must.
func TestOutcomeSourceStaleAfterMirrorsSettleAbandonAfter(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "bg", "auto_route_settle_worker.go"))
	if err != nil {
		t.Fatalf("read bg/auto_route_settle_worker.go: %v", err)
	}
	// Anchor on the DECLARATION, not the first mention of the name: the
	// identifier also appears in a doc comment several lines above the const,
	// and matching that is how the first version of this guard parsed a
	// sentence as Go and failed with "could not find '='".
	var decl string
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "settleAbandonAfter") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "settleAbandonAfter"))
		if !strings.HasPrefix(rest, "=") {
			// `settleAbandonAfter is when a row ...` — prose, not a decl.
			continue
		}
		decl = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rest, "="), ","))
		break
	}
	if decl == "" {
		t.Fatalf("no `settleAbandonAfter = <duration>` declaration in " +
			"bg/auto_route_settle_worker.go — this guard is guarding a name that is gone; " +
			"rewrite it against the new owner")
	}

	got, err := parseDurationLiteral(decl)
	if err != nil {
		t.Fatalf("parse settleAbandonAfter's value %q: %v", decl, err)
	}
	if got != outcomeSourceStaleAfter {
		t.Fatalf("outcomeSourceStaleAfter = %v but bg.settleAbandonAfter = %v (%q). "+
			"The two must stay equal: the admin freshness block exists to flag the "+
			"moment the settle worker can no longer produce a reward, and that moment "+
			"is defined by settleAbandonAfter, not by this mirror.", outcomeSourceStaleAfter, got, decl)
	}
}

// parseDurationLiteral evaluates a pure Go time.Duration literal such as
// "4 * time.Hour" without importing bg. It accepts only a single product of a
// number and a time unit — deliberately narrow, so it cannot silently accept an
// expression whose value it guessed wrong.
func parseDurationLiteral(expr string) (time.Duration, error) {
	parts := strings.Split(expr, "*")
	if len(parts) != 2 {
		return 0, errors.New("expected exactly one '*' product, got: " + expr)
	}
	numText := strings.TrimSpace(parts[0])
	unitText := strings.TrimSpace(parts[1])

	num, err := strconv.ParseFloat(numText, 64)
	if err != nil {
		return 0, errors.New("left factor is not a number: " + numText)
	}
	var unit time.Duration
	switch unitText {
	case "time.Nanosecond", "time.Microsecond", "time.Millisecond",
		"time.Second", "time.Minute", "time.Hour":
		unit = time.Nanosecond
		switch unitText {
		case "time.Microsecond":
			unit = time.Microsecond
		case "time.Millisecond":
			unit = time.Millisecond
		case "time.Second":
			unit = time.Second
		case "time.Minute":
			unit = time.Minute
		case "time.Hour":
			unit = time.Hour
		}
	default:
		return 0, errors.New("unrecognised time unit: " + unitText)
	}
	return time.Duration(num * float64(unit)), nil
}

// ─────────────────────────────────────────────────────────────────────────
// wiring guard (default-deny)
// ─────────────────────────────────────────────────────────────────────────

// autoRouteFreshnessMounts is the set of handlers that MUST carry the block,
// each with the reason it matters. Adding a handler to this list is a decision;
// removing a line is what this guard exists to catch.
//
// The check is structural, not textual: it parses the file and requires that
// the named function's writeJSONOk call is handed a composite literal holding
// the key. So reformatting, reordering, or re-wrapping the map does not break
// it, and deleting the field does — which a substring search over the file
// would get backwards (it would pass on a mention in a comment).
var autoRouteFreshnessMounts = map[string]string{
	"handleAudit":              "reports success rate / routing KPIs settled later by the worker",
	"HandleAffinityRanking":    "reports avg_reward / ema_reward produced by the worker's baselines",
	"handleAffinitySelections": "reports per-selection reward / reward_source / settled_at",
}

// TestAutoRouteFreshnessMountedOnEveryNamedHandler is default-deny over
// autoRouteFreshnessMounts: every listed handler must carry the key.
func TestAutoRouteFreshnessMountedOnEveryNamedHandler(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "auto_route.go", nil, 0)
	if err != nil {
		t.Fatalf("parse auto_route.go: %v", err)
	}

	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if _, want := autoRouteFreshnessMounts[fn.Name.Name]; !want {
			return true
		}
		if hasFreshnessKey(fn.Body) {
			found[fn.Name.Name] = true
		}
		return true
	})

	for name, why := range autoRouteFreshnessMounts {
		if !found[name] {
			t.Errorf("%s no longer carries the outcome_source block (%s). "+
				"Its numbers are settled by bg.AutoRouteSettleWorker against the v1 hot "+
				"table; without the block a frozen source is indistinguishable from a "+
				"quietly zero one. If this mount is being removed on purpose, remove the "+
				"entry from autoRouteFreshnessMounts with the reason recorded there.", name, why)
		}
	}
}

// hasFreshnessKey reports whether body mounts the block, accepting the two
// shapes the package actually uses:
//
//	A. writeJSONOk(w, map[string]any{..., "outcome_source": ...})  — literal
//	B. out["outcome_source"] = ...; writeJSONOk(w, out)             — index assign
//
// The callee is matched as an *ast.Ident because writeJSONOk is a PACKAGE-level
// function, not a method. The first version of this guard matched only
// *ast.SelectorExpr and therefore found zero mounts in a file that had all three
// — a guard that reports "missing" for something that is present trains its
// reader to ignore it, which is worse than having no guard.
//
// Matching is structural: reformatting, key reordering and re-wrapping do not
// break it, while deleting the mount does. A substring search over the file
// would have the opposite behaviour — it would pass on the mere mention of the
// key in a comment.
func hasFreshnessKey(body *ast.BlockStmt) bool {
	const key = `"outcome_source"`

	// Identifiers that are handed to writeJSONOk as its payload.
	envelopes := map[string]bool{}
	// Map variables that receive the key by index assignment.
	indexed := map[string]bool{}
	foundLiteral := false

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if !isWriteJSONOk(node) || len(node.Args) != 2 {
				return true
			}
			if id, ok := node.Args[1].(*ast.Ident); ok {
				envelopes[id.Name] = true
			}
			lit, ok := node.Args[1].(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.BasicLit); ok && k.Value == key {
					foundLiteral = true
				}
			}
		case *ast.AssignStmt:
			// `out["outcome_source"] = ...`
			if len(node.Lhs) != 1 {
				return true
			}
			idx, ok := node.Lhs[0].(*ast.IndexExpr)
			if !ok {
				return true
			}
			lit, ok := idx.Index.(*ast.BasicLit)
			if !ok || lit.Value != key {
				return true
			}
			if id, ok := idx.X.(*ast.Ident); ok {
				indexed[id.Name] = true
			}
		}
		return true
	})

	if foundLiteral {
		return true
	}
	for name := range indexed {
		if envelopes[name] {
			return true
		}
	}
	return false
}

// isWriteJSONOk reports whether call is a call to the package-level
// writeJSONOk helper, matching either an Ident (package func) or a
// SelectorExpr (a method or a qualified name) so a future move to a method
// does not silently disarm the guard.
func isWriteJSONOk(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "writeJSONOk"
	case *ast.SelectorExpr:
		return fun.Sel.Name == "writeJSONOk"
	}
	return false
}

// TestOutcomeFreshnessKeysAreJSONTagged pins the wire names. These are the API
// contract; renaming one silently breaks every consumer that reads it.
func TestOutcomeFreshnessKeysAreJSONTagged(t *testing.T) {
	want := []string{
		`json:"available"`,
		`json:"as_of,omitempty"`,
		`json:"age_seconds,omitempty"`,
		`json:"stale"`,
		`json:"stale_after_seconds"`,
		`json:"reason"`,
	}
	src, err := os.ReadFile("auto_route_outcome_freshness.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, tag := range want {
		if !strings.Contains(string(src), tag) {
			t.Errorf("outcomeSourceFreshness is missing the wire tag %s", tag)
		}
	}
}
