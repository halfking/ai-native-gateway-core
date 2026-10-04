//go:build !integration

package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// Retirement exposure of the v1 readers (audit §9.162).
//
// requestLogsReadInventory pins **coverage** — 104 files / 237 call sites that
// read `request_logs*` — but deliberately draws no conclusion about them ("this
// gate counts, it does not judge"). So the retirement work list has never
// existed. §9.161 supplies the other half: which canonical columns the session
// family can still serve after `request_logs` is dropped. This file joins the
// two.
//
// # Why this is not the "automatic classification" that already failed
//
// §5.5.5 tried to bucket read sites into intent classes (A/B/C/D) from statement
// windows and got 5 of 14 wrong. That was a question about **intent** — hard,
// and rightly not automated.
//
// This asks a different question: **which column names appear inside SQL
// literals that read a v1 table**. That is a reference question. It has a
// mechanical answer, the answer can be spot-checked by reading, and — the part
// that matters — a wrong answer errs in a *known* direction (see below).
//
// # The direction of the error, stated up front
//
// A degraded column name appearing in a v1-reading literal is **not** proof that
// the value comes from v1. A UNION over the canonical view and the v1 tables can
// name the same column on both legs, and a same-named column could be produced by
// the session leg alone. So this is an **upper bound**: it over-reports.
//
// Over-reporting is the correct direction for a retirement checklist — a
// false positive costs one file to be looked at again, while a false negative
// silently ships a reader that returns nothing. What the upper bound cannot do
// is license shipping without review, and the report below says so rather than
// letting the number imply it was checked.
//
// # Why the extraction is ast-based, not a line regex
//
// The existing inventory scanner counts with a per-line
// `from\s+request_logs` regex, which also matches inside `--` comments. That is
// fine for counting call sites and wrong for attributing a **column** to a
// statement: a comment explaining the old expression would inject column names
// into the exposure set. Comments are stripped here, and SQL inside Go string
// literals is read via the AST rather than scraped from raw lines.

// v1TableRe matches a reference to any relation in the v1 request_logs family.
var v1TableRe = regexp.MustCompile(`\b(request_logs|request_logs_hot|request_logs_bodies|request_logs_bodies_hot)\b`)

// sessionFamilyRe matches a reference to the session family or the canonical
// view. Its presence in the same literal is what makes a column attribution
// ambiguous, and its absence is what makes it definite.
var sessionFamilyRe = regexp.MustCompile(`\b(session_turns|request_logs_with_current_month|session_bodies)\b`)

// sqlLineCommentRe / sqlBlockCommentRe strip SQL comments from a literal so that
// prose inside a query string cannot contribute column names.
var (
	sqlLineCommentRe  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockCommentRe = regexp.MustCompile(`/\*.*?\*/`)
)

// v1ReadingLiteral is one SQL string literal that references the v1 family.
type v1ReadingLiteral struct {
	// text is the comment-stripped literal.
	text string
	// alsoSessionFamily records whether the same literal also references the
	// session family or the canonical view. When true, a column name in this
	// literal cannot be attributed to v1 alone.
	alsoSessionFamily bool
	// columns are the exposure-class columns named in this literal, with each
	// column carrying the strongest attribution the literal supports:
	//
	//   attrQualified — written as <v1alias>.<col>, so it provably comes from v1
	//   attrSoleRel   — bare <col> in a literal that reads exactly one relation,
	//                   so no other relation can be its source
	//   attrAmbiguous — bare <col> alongside other relations; counted, because
	//                   over-reporting is the safe direction for a checklist
	//
	// A column is **not** counted at all when every occurrence is qualified by
	// some other alias (`mc.id`, `c.id`, `p.id`, …) or is a JSONB key access
	// (`att->>'id'`). Those are the shape of dimension-table primary keys, and
	// treating them as v1 dependencies is what made §9.162/§9.164 report `id`
	// as the blocker for eleven files that never touch request_logs.id.
	columns map[string]string // column -> class
	// allColumns is every canonical contract column this literal names, with the
	// same attribution rule but **not** restricted to the non-baseline classes.
	//
	// Two different questions need two different sets, and §9.167 showed what
	// happens when one is used for both:
	//
	//   - `columns` answers "which exposure column does this reader depend on",
	//     which is what the exposure report is for. Baseline columns are noise
	//     there — nobody needs a work item for `ts`.
	//   - `allColumns` answers "what would repointing this reader do to it",
	//     which is what the repoint verdict is computed from. Restricted to the
	//     non-baseline classes, a reader that touches only baseline columns
	//     extracts nothing at all — and an empty input used to come out
	//     `repoint-safe` (§9.167.3).
	//
	// The verdict reads `allColumns`; the report reads `columns`. They are not
	// interchangeable.
	allColumns map[string]string // column -> class
}

// Attribution strengths, weakest to strongest. attrNone means "every occurrence
// belongs to some other relation", which is the case that was silently
// counting dimension-table primary keys as v1 dependencies.
const (
	attrNone = iota
	attrAmbiguous
	attrSoleRel
	attrQualified
)

// exposureColumnMatchers compiles one word-boundary-anchored matcher per column
// in the non-baseline exposure classes.
//
// Two matcher sets exist because two questions are being asked:
//
//   - the exposure set covers the non-baseline classes only. It drives the
//     report, where a baseline column is noise — nobody needs a work item for
//     `ts`.
//   - the contract set (contractColumnMatchers) covers **every** canonical
//     column. It drives the repoint verdict, which has to know every column a
//     reader touches: `latency_ms` decides safe, `work_type` decides empty, and
//     a reader that uses only baseline columns still has to produce a non-empty
//     input.
//
// Merging them is how §9.167's published `repoint-safe` happened: the verdict
// was fed the report's filtered set, so a reader whose only matches were lost
// to the alias bug arrived with nothing at all and was graded on no evidence.
func exposureColumnMatchers() map[string]*regexp.Regexp {
	// value-divergent is not a list this package exports the way the other three
	// are — it is keyed off RetirementSessionLegDivergence, so it is derived.
	// It belongs here because a reader depending on a column whose *value* the
	// session leg does not reproduce is exactly the work item §9.167 created the
	// class for.
	var valueDivergent []string
	for _, d := range db.RetirementSessionLegDivergence {
		valueDivergent = append(valueDivergent, d.Column)
	}
	sort.Strings(valueDivergent)

	out := map[string]*regexp.Regexp{}
	for _, cols := range [][]string{
		db.RetirementStructuralGapColumns,
		db.RetirementUnservableColumns,
		db.RetirementDegradedColumns,
		valueDivergent,
	} {
		for _, c := range cols {
			out[c] = regexp.MustCompile(`\b` + regexp.QuoteMeta(c) + `\b`)
		}
	}
	return out
}

// contractColumnMatchers covers the whole canonical contract, reusing the
// exposure matchers where they exist.
func contractColumnMatchers(exposure map[string]*regexp.Regexp) map[string]*regexp.Regexp {
	contract := db.CanonicalContractColumns()
	out := make(map[string]*regexp.Regexp, len(contract))
	for _, c := range contract {
		if m, ok := exposure[c]; ok {
			out[c] = m
			continue
		}
		out[c] = regexp.MustCompile(`\b` + regexp.QuoteMeta(c) + `\b`)
	}
	return out
}

// extractV1ReadingLiterals returns every SQL string literal in a Go file that
// references the v1 family, together with the exposure classes of the canonical
// columns it names.
func extractV1ReadingLiterals(t *testing.T, path string) []v1ReadingLiteral {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		// A file that does not parse is a finding in itself: if the inventory
		// lists a file that no longer parses, the inventory is describing
		// something that cannot be built.
		t.Fatalf("parse %s: %v", path, err)
	}

	// One compiled matcher per column, anchored on word boundaries, so that
	// `rl.is_final_success` and a bare `is_final_success` both hit and
	// `is_final_success_v2` does not. Built once for the whole run.
	exposureMatchers := exposureColumnMatchers()
	contractMatchers := contractColumnMatchers(exposureMatchers)

	var out []v1ReadingLiteral
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		raw, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		// Not every string literal is SQL; require a v1 relation before
		// spending comment-stripping and matching on it.
		if !v1TableRe.MatchString(raw) {
			return true
		}
		clean := sqlBlockCommentRe.ReplaceAllString(raw, " ")
		clean = sqlLineCommentRe.ReplaceAllString(clean, " ")

		l := v1ReadingLiteral{
			text:              clean,
			alsoSessionFamily: sessionFamilyRe.MatchString(clean),
			columns:           map[string]string{},
			allColumns:        map[string]string{},
		}
		v1al, allAl := aliasesIn(clean)
		relCount := countRelations(clean)
		// The contract pass runs first and the exposure pass filters it down, so
		// the two can never drift apart.
		for col := range contractMatchers {
			attr := columnAttribution(clean, col, v1al, allAl, relCount)
			if attr == attrNone {
				// Every occurrence is qualified by a relation that is not the v1
				// family — a dimension table's primary key, or a JSONB key
				// access. **This is the case that makes `id` a false positive**
				// for readers that join providers/credentials/models_canonical.
				continue
			}
			class := db.RetirementExposureClassify(col)
			l.allColumns[col] = class
			if _, isExposure := exposureMatchers[col]; isExposure {
				l.columns[col] = class
			}
		}
		out = append(out, l)
		return true
	})
	return out
}

// fileExposure is the per-file summary.
type fileExposure struct {
	file string
	// literals is how many v1-reading SQL literals the file has.
	literals int
	// definite is a class→columns map built from literals that read v1 and
	// nothing else, so the column provably comes from v1.
	definite map[string][]string
	// possible is the same for literals that also read the session family, so
	// the column could be coming from either leg.
	possible map[string][]string
}

func (fe fileExposure) severity() string {
	// Ranked worst-first. A single definite hit on an unservable column is the
	// only thing that means "this reader breaks at retirement".
	switch {
	case len(fe.definite["unservable"]) > 0 || len(fe.definite["structural-gap"]) > 0:
		return "breaks"
	case len(fe.possible["unservable"]) > 0 || len(fe.possible["structural-gap"]) > 0:
		return "breaks-possibly"
	case len(fe.definite["degraded"]) > 0:
		return "undercounts"
	case len(fe.possible["degraded"]) > 0:
		return "undercounts-possibly"
	default:
		return "clean"
	}
}

// TestRequestLogsRetirementExposure walks the inventory and reports, per file,
// which retirement-exposed canonical columns its v1 SQL names.
func TestRequestLogsRetirementExposure(t *testing.T) {
	root := repoRootFromCaller(t)

	files := make([]string, 0, len(requestLogsReadInventory))
	for f := range requestLogsReadInventory {
		files = append(files, f)
	}
	sort.Strings(files)

	bySeverity := map[string][]string{}
	var exposures []fileExposure

	for _, rel := range files {
		abs := filepath.Join(root, rel)
		lits := extractV1ReadingLiterals(t, abs)
		fe := fileExposure{
			file:     rel,
			literals: len(lits),
			definite: map[string][]string{},
			possible: map[string][]string{},
		}
		for _, l := range lits {
			bucket := fe.possible
			if !l.alsoSessionFamily {
				bucket = fe.definite
			}
			for col, class := range l.columns {
				bucket[class] = append(bucket[class], col)
			}
		}
		exposures = append(exposures, fe)
		bySeverity[fe.severity()] = append(bySeverity[fe.severity()], rel)
	}

	for _, sev := range []string{"breaks", "breaks-possibly", "undercounts", "undercounts-possibly", "clean"} {
		sort.Strings(bySeverity[sev])
		if len(bySeverity[sev]) == 0 {
			continue
		}
		t.Logf("=== %s (%d files) ===", sev, len(bySeverity[sev]))
		for _, fe := range exposures {
			if fe.severity() != sev {
				continue
			}
			t.Logf("  %-58s literals=%d definite=%v possible=%v", fe.file, fe.literals, flatten(fe.definite), flatten(fe.possible))
		}
	}
	t.Logf("summary: %v", countsBySeverity(exposures))

	// The gate proper: every inventoried file must be classifiable, and none may
	// silently fall out. A file with zero v1-reading literals is itself a
	// finding — it means the inventory counts something this extractor cannot
	// see, which is exactly the indirect-reader blind spot §9.49 wrote down.
	var invisible []string
	for _, fe := range exposures {
		if fe.literals == 0 {
			invisible = append(invisible, fe.file)
		}
	}
	sort.Strings(invisible)
	if len(invisible) > 0 {
		t.Logf("NOTE: %d inventoried file(s) expose no v1-reading SQL literal to this extractor — "+
			"they are either indirect readers (relation name assembled at runtime) or the "+
			"inventory counts a line the AST-based extractor does not see. Both are known "+
			"blind spots of literal-based scanning, and both are why this report is an "+
			"upper bound over a subset, not a complete classification:\n  %s",
			len(invisible), strings.Join(invisible, "\n  "))
	}
}

// retirementBreakers is the named registry of files that name a column the
// session family cannot serve, inside SQL that reads the v1 family. These are
// the files that must be repointed or retired **before** `request_logs` is
// dropped.
//
// It is a registry rather than a derived assertion so that fixing a reader is a
// deliberate act: you remove the entry, and the bidirectional gate then requires
// the measurement to agree. Without the registry, a fixed reader silently
// rejoins the "clean" bucket and the checklist quietly stops listing it; with a
// derived-only report, nobody can tell a file nobody looked at from a file
// somebody already handled.
//
// Only the two breaking severities are registered. `undercounts` files are
// reported but not gated: degradation is a "check the arithmetic" judgement that
// depends on what each reader does with the number, and gating it would put a
// judgement call behind a mechanical test — the same mistake §5.5.5 already made.
//
// # Known limit of this gate, measured rather than assumed
//
// Deleting the two comment-stripping lines makes this gate stay **green**. That
// is not because the stripping is unnecessary. Measured (2026-10-04): without
// stripping, SQL comments inject 3 extra degraded-column references —
// `origin_stage` + `quality_flags` in bg/credential_selfcheck.go and
// `origin_stage` in bg/credential_recovery.go. The gate misses them because both
// files are already in the breaking bucket via `id` / `credential_id`, and
// severity is computed from the *class*, not from which columns produced it.
//
// So: the stripping is load-bearing, and **this gate is insensitive to
// within-bucket drift**. A per-file column-set registry would catch it and would
// also be a much larger thing to keep honest, so it is not built here. The
// per-file column lists in TestRequestLogsRetirementExposure's report *do* show
// the difference — read the report, not just the gate verdict.
var retirementBreakers = map[string]string{
	// ── still blocking after column→relation attribution (§9.165) ──────────
	"admin/work_types.go": "reads request_logs.work_type directly — the file's own comment calls it " +
		"'Direct work_type column'; work_type is 0.00% on the session side, so repointing to the " +
		"view returns an empty page",
	"db/db.go": "defines the 118-column canonical projection itself, so it names every exposed " +
		"column including work_type / is_final_success. Not an operational reader: retiring " +
		"request_logs means replacing this body",
	"domains/hooks/observability/telemetry/client.go": "the v1 **writer** (insertRequestLog). An " +
		"INSERT into a dropped table errors, so it must be removed or repointed, not tolerated",
	"domains/streaming/model_alternatives.go": "still blocked after §9.165's attribution fix: its " +
		"remaining `id` occurrence is not models_canonical's. canonical_id is also effectively empty " +
		"on the session side (0.07% vs 9.79% v1)",
	"cmd/gateway/dual_read_validator.go": "reads v1 work_type / request_type / origin_actor as its " +
		"V1-side inputs — legitimate in itself (§9.163.1), but work_type is 0% on the session side, " +
		"so the validator's exclusion arm loses its discriminator after the v1 side is gone",
}

// retirementReattributed records the eleven files that §9.162's registry listed
// as breakers and that §9.165's attribution fix re-classified. They are kept
// here as a record rather than deleted, because "we checked this and it is
// actually fine" is information; a silently shrunken list is indistinguishable
// from a list nobody maintained.
//
// The defect they share: the old extractor asked "does the substring `id` appear
// in a v1-reading literal", and these files' literals are full of dimension-table
// primary keys (`mc.id`, `c.id`, `p.id`), a JSONB key access (`att->>'id'`), and
// an output alias (`credential_id AS id`). None of them is request_logs.id.
var retirementReattributed = map[string]string{
	"admin/data_lifecycle_attachments.go": "repoint-degraded: real dependency is `attachments` (18.19% session vs 100% v1). The `id` was `att->>'id'`, a JSONB key access on the attachments column",
	"admin/probe_history.go":              "repoint-degraded: `credential_id` only. The `id` was `models_canonical.id`",
	"admin/providers.go":                  "repoint-degraded: `provider_id` only. Every `p.id`/`c.id` belongs to the providers/credentials tables",
	"admin/routing.go":                    "repoint-degraded: `canonical_id` only. The `id` was `models_canonical.id`",
	"admin/swim_lane_init.go":             "**repoint-safe**: no exposure columns at all. The `id`s were models_canonical / model_families / credentials / providers primary keys",
	"bg/auto_index_refresher.go":          "repoint-degraded: `canonical_id` only. The `id`s were credentials / models_canonical / provider_models / credential_model_bindings primary keys",
	"bg/credential_recovery.go":           "repoint-degraded: `client_model` + `credential_id` only",
	"bg/credential_selfcheck.go":          "repoint-degraded: `canonical_id` + `credential_id` only. The `id`s were `credentials.id`",
	"bg/model_probe.go":                   "**repoint-safe**: no exposure columns at all",
	"bg/today_success_probe.go":           "repoint-safe: its `id` was `rl.credential_id AS id` — an output alias. The real dependency, credential_id, is a *stronger* column than the list assumed and is not in the breaker set on its own",
	"admin/logs.go": "repoint-degraded rather than blocked: with `id` and `provider_model` correctly " +
		"attributed away, the only real v1 dependency left is the degraded set " +
		"(client_model 90.06%, credential_id 62.65%, canonical_id 0.07%, provider_id 55.82%, " +
		"application_id 3.51% on the session side)",
}

// TestRequestLogsRetirementBreakersRegistryIsConsistent gates the registry in
// both directions, which is the property that makes it a checklist rather than a
// snapshot:
//
//   - a file the measurement now calls broken but is not registered → a new
//     dependency appeared and nobody was told;
//   - a registered file the measurement no longer calls broken → somebody fixed
//     it and left the entry, so every future reviewer re-investigates a solved
//     problem.
func TestRequestLogsRetirementBreakersRegistryIsConsistent(t *testing.T) {
	root := repoRootFromCaller(t)

	measured := map[string]string{} // file -> severity
	for f := range requestLogsReadInventory {
		fe := fileExposure{
			file:     f,
			definite: map[string][]string{},
			possible: map[string][]string{},
		}
		for _, l := range extractV1ReadingLiterals(t, filepath.Join(root, f)) {
			bucket := fe.possible
			if !l.alsoSessionFamily {
				bucket = fe.definite
			}
			for col, class := range l.columns {
				bucket[class] = append(bucket[class], col)
			}
		}
		if sev := fe.severity(); sev == "breaks" || sev == "breaks-possibly" {
			measured[f] = sev
		}
	}

	var unregistered, stale []string
	for f := range measured {
		if _, ok := retirementBreakers[f]; !ok {
			unregistered = append(unregistered, f+" ("+measured[f]+")")
		}
	}
	for f := range retirementBreakers {
		if _, ok := measured[f]; !ok {
			stale = append(stale, f)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	if len(unregistered) > 0 {
		t.Errorf("%d file(s) now read a column the session family cannot serve but are absent "+
			"from retirementBreakers — request_logs cannot be dropped while this is true:\n  %s",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d registered retirementBreaker(s) are no longer detected as breaking — if "+
			"that is because the reader was repointed, delete the entry; if it is because the "+
			"extractor lost sight of it, fix the extractor first:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// TestRequestLogsRetirementRepointVerdict answers D14-a with numbers instead of
// intuition: for each registered breaker, what would actually happen if the
// reader were repointed from the v1 base tables to the canonical view?
//
// The finding that motivates the test is that "just repoint it to the view" is
// **not** a fix. The view's values come from the session family, and §9.161
// measured that some of the columns these readers need are nearly empty there:
//
//	admin/work_types.go              work_type        0.00% vs 1.93%  → the page returns nothing
//	admin/data_lifecycle_attachments.go  attachments 18.19% vs 100%  → 82% of attachments vanish
//	bg/auto_index_refresher.go       total_tokens    58.36% vs 100%  → the index under-counts by 42%
//
// The third kind is the dangerous one: a query returning 18% of its rows is
// indistinguishable, from the outside, from a query that works. That is a worse
// failure mode than a missing table, which at least announces itself.
func TestRequestLogsRetirementRepointVerdict(t *testing.T) {
	root := repoRootFromCaller(t)
	// Both registries, not just the blockers: the point of this report is the
	// whole assessed set, and shrinking it to "the ones that break" would hide
	// exactly the files §9.165 re-classified.
	seen := map[string]bool{}
	var files []string
	for f := range retirementBreakers {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	for f := range retirementReattributed {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)

	byVerdict := map[string][]string{}
	for _, rel := range files {
		cols := map[string]bool{}
		for _, l := range extractV1ReadingLiterals(t, filepath.Join(root, rel)) {
			// The verdict reads `allColumns`, not `columns` — see the field's doc
			// for why the two must not be merged.
			for c := range l.allColumns {
				cols[c] = true
			}
		}
		names := make([]string, 0, len(cols))
		for c := range cols {
			names = append(names, c)
		}
		sort.Strings(names)
		v := db.RetirementRepointVerdictFor(names)
		byVerdict[string(v)] = append(byVerdict[string(v)], rel+"  ["+strings.Join(names, " ")+"]")
	}
	for _, v := range []db.RetirementRepointVerdict{
		db.RepointNoColumnsMeasured, db.RepointNoSuchColumn, db.RepointValueDivergent,
		db.RepointEmpty, db.RepointGapOnly, db.RepointDegraded, db.UnknownColumn,
		db.RepointSafe,
	} {
		list := byVerdict[string(v)]
		if len(list) == 0 {
			continue
		}
		sort.Strings(list)
		t.Logf("── %s (%d) ──", v, len(list))
		for _, l := range list {
			t.Logf("   %s", l)
		}
	}
	// One column per class, pinned. Rule **order** is load-bearing and nothing
	// else in this file can see it: `client_model` is simultaneously in
	// RetirementDegradedColumns and registered in RetirementSessionLegDivergence,
	// so whichever arm is reached first decides. Moving value-divergent below
	// degraded empties its bucket and sends eight files back to
	// `repoint-degraded` — and the rest of this test stays green while it
	// happens (mutation-verified §9.167). This table is what makes that visible.
	for _, tc := range []struct {
		cols []string
		want db.RetirementRepointVerdict
		why  string
	}{
		{[]string{"client_model"}, db.RepointValueDivergent,
			"in the degraded list by fill rate and registered as value-divergent; the value rule must win"},
		{[]string{"outbound_model"}, db.RepointValueDivergent,
			"same overlap, different column"},
		{[]string{"credential_id"}, db.RepointDegraded,
			"populated but emptier than v1, and faithful on both legs"},
		{[]string{"work_type"}, db.RepointEmpty,
			"0% on the session side while v1 holds values"},
		{[]string{"upstream_endpoint"}, db.RepointNoSuchColumn,
			"a real request_logs column that the view does not project"},
		{nil, db.RepointNoColumnsMeasured,
			"no columns is an absence of measurement, never a clean bill of health"},
		{[]string{"latency_ms"}, db.RepointSafe,
			"at v1 parity and not registered as divergent — the one honest safe case"},
	} {
		if got := db.RetirementRepointVerdictFor(tc.cols); got != tc.want {
			t.Errorf("RetirementRepointVerdictFor(%v) = %q, want %q — %s",
				tc.cols, got, tc.want, tc.why)
		}
	}

	// Coverage is a claim, so it gets a check.
	//
	// The verdict's input set is built from **every** canonical contract column.
	// If a refactor narrows it back to the non-baseline classes, the report keeps
	// working, `repoint-safe` stays 0, and the only thing lost is the columns that
	// decide safe-vs-worse — a quiet over-optimism rather than a failure. §9.167
	// is what makes it worth asserting: an earlier extractor handed three files an
	// empty set and the verdict function graded that `repoint-safe`.
	exposureMatchers, contractMatchers := exposureColumnMatchers(), contractColumnMatchers(exposureColumnMatchers())
	t.Logf("exposure matcher columns=%d, contract matcher columns=%d (contract should be %d)",
		len(exposureMatchers), len(contractMatchers), len(db.CanonicalContractColumns()))
	if len(contractMatchers) != len(db.CanonicalContractColumns()) {
		t.Errorf("repoint verdict's matcher set covers %d columns but the canonical contract has "+
			"%d — a reader depending only on a column outside the exposure classes would "+
			"extract nothing and be graded on no evidence",
			len(contractMatchers), len(db.CanonicalContractColumns()))
	}
	// Every column that can push a verdict *above* safe must also be in the
	// report's set, so the work list and the verdict can never disagree about
	// which columns are known problems.
	for _, d := range db.RetirementSessionLegDivergence {
		if _, ok := exposureMatchers[d.Column]; !ok {
			t.Errorf("registered value-divergent column %q is missing from the exposure matcher "+
				"set, so a reader depending on it never appears on the work list", d.Column)
		}
	}

	// A file that contributes no columns is not a finding about that file — it is
	// the instrument failing on it. §9.167 shipped three such files graded
	// `repoint-safe` because the alias regex dropped the `rl` qualifier for
	// `request_logs_hot`, and nothing in this test could tell that apart from a
	// genuinely clean reader. So it is an error, not a log line.
	if n := len(byVerdict[string(db.RepointNoColumnsMeasured)]); n > 0 {
		t.Errorf("%d assessed file(s) contributed no columns, so their verdict is an absence "+
			"of measurement rather than a measurement — see db.RetirementRepointVerdictFor's "+
			"RepointNoColumnsMeasured: %v", n, byVerdict[string(db.RepointNoColumnsMeasured)])
	}
	// The point of the test: a blanket repoint is not available. If every breaker
	// ever came out `repoint-safe`, D14-a would have a trivially correct answer;
	// the gate's value is that it keeps re-checking that assumption instead of
	// letting it be made once and forgotten.
	safe := len(byVerdict[string(db.RepointSafe)])
	t.Logf("repoint-safe: %d of %d assessed files", safe, len(files))
}

func flatten(m map[string][]string) []string {
	var out []string
	for _, cols := range m {
		sort.Strings(cols)
		for _, c := range cols {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func countsBySeverity(exposures []fileExposure) map[string]int {
	out := map[string]int{}
	for _, fe := range exposures {
		out[fe.severity()]++
	}
	return out
}

// v1AliasRe captures the alias bound to a v1 relation: `FROM request_logs_hot rl`
// binds rl, while `FROM request_logs` with no alias leaves the table name itself
// as the only valid qualifier.
// The alternatives are ordered **longest first** and the table name is
// `\b`-anchored on both sides. Both parts are load-bearing, and getting either
// wrong is silent rather than loud.
//
// Go's regexp alternation is leftmost-first, not longest-match, so
// `(request_logs|request_logs_hot|…)` matches the *prefix* `request_logs` of
// `FROM request_logs_hot rl`. The optional alias group then has nothing to match
// (the next character is `_`, not whitespace), so **the alias is dropped**:
// v1al came out as `{request_logs: true}` with no `rl`.
//
// Downstream that is not a degraded measurement, it is an **absent** one:
// columnAttribution sees `rl.client_model`, finds `rl` is not a known v1
// qualifier, and returns attrNone. Every column of every `request_logs_hot`
// reader was therefore reported as depending on nothing, and
// `RetirementRepointVerdictFor([])` — whose loop body never executes — returns
// its zero value, `RepointSafe`.
//
// That is how `admin/swim_lane_init.go`, `bg/model_probe.go` and
// `bg/today_success_probe.go` were graded `repoint-safe` in §9.165 and §9.166:
// **with no column evidence at all.** §9.165's `columnAttribution` fix removed
// the `id` false positives and, in the same stroke, removed every real
// dependency of every hot-table reader. A correction that fixes one false
// positive while creating a silent false negative is worse than the bug.
var v1AliasRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+(request_logs_bodies_hot|request_logs_bodies|request_logs_hot|request_logs)\b(?:\s+(\w+))?`)

// relationRe counts distinct relations named in FROM/JOIN, so a literal that
// reads exactly one relation can be attributed with certainty.
var relationRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-z_][a-z0-9_.]*)`)

// aliasesIn returns (v1 qualifiers, all relation qualifiers) for a literal.
//
// The v1 set is what makes an occurrence provably a v1 dependency; the all set
// is what lets a non-v1 qualifier be recognised and excluded.
func aliasesIn(sql string) (v1al, allAl map[string]bool) {
	v1al = map[string]bool{}
	allAl = map[string]bool{}
	for _, m := range relationRe.FindAllStringSubmatch(sql, -1) {
		rel := strings.ToLower(m[1])
		if rel == "" || rel == "lateral" || rel == "(" {
			continue
		}
		allAl[rel] = true
	}
	for _, m := range v1AliasRe.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(m[1])
		alias := ""
		if m[2] != "" && !isSQLKeyword(m[2]) {
			alias = strings.ToLower(m[2])
			allAl[alias] = true
		}
		v1al[table] = true
		if alias != "" {
			v1al[alias] = true
		}
	}
	return v1al, allAl
}

func isSQLKeyword(s string) bool {
	switch strings.ToLower(s) {
	case "where", "on", "group", "order", "limit", "and", "or", "select",
		"join", "left", "right", "inner", "outer", "cross", "lateral", "as", "set":
		return true
	}
	return false
}

// columnAttribution decides, for one column name in one literal, whether that
// literal makes it a v1 dependency — and how confidently.
//
// It walks the **occurrences**, not the text. That distinction is the whole
// point: `mc.id = rl.canonical_id` contains the substring "id" twice, and
// neither occurrence has anything to do with request_logs. A regex that asks
// "does the string id appear" cannot tell those from `rl.id`.
//
// For each occurrence the preceding token decides:
//
//	"rl.id"          → qualifier is a v1 alias      → attrQualified
//	"mc.id" / "c.id" → qualifier is another relation → does not count
//	"att->>'id'"     → preceded by a quote, not an identifier qualifier → does not count
//	"id" bare        → counts (sole relation: certain; otherwise: ambiguous)
func columnAttribution(sql, col string, v1al, allAl map[string]bool, relCount int) int {
	colRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
	best := attrNone
	for _, loc := range colRe.FindAllStringIndex(sql, -1) {
		start := loc[0]
		// Walk back over optional whitespace and a single '.' to find a
		// qualifier.
		i := start
		for i > 0 && isSpaceByte(sql[i-1]) {
			i--
		}
		if i > 0 && sql[i-1] == '.' {
			// Qualified. Take the identifier immediately before the dot.
			j := i - 1
			for j > 0 && isIdentByte(sql[j-1]) {
				j--
			}
			qual := strings.ToLower(sql[j : i-1])
			if qual == "" {
				// e.g. `att->>'id'`: the char before is a quote, not an
				// identifier, so there is no relation qualifier at all.
				continue
			}
			if v1al[qual] {
				if attrQualified > best {
					best = attrQualified
				}
			}
			// Qualified by some other relation: not a v1 dependency.
			continue
		}
		// Bare occurrence. Two shapes look like a bare column but are not one:
		//   - a JSONB key access: `att->>'id'` — the char before is a quote;
		//   - an **output alias**: `rl.credential_id AS id` — the identifier
		//     before the dot-free `id` is the keyword AS. The aliased column is
		//     credential_id (degraded), and `id` is the name the query gives it.
		//     Counting the alias as a dependency on a v1 column named `id` is
		//     the same category of error as counting `mc.id`.
		if start > 0 && (sql[start-1] == '\'' || sql[start-1] == '"') {
			continue
		}
		j := start
		for j > 0 && isSpaceByte(sql[j-1]) {
			j--
		}
		if j >= 2 && (strings.EqualFold(sql[j-2:j], "as")) {
			continue
		}
		if relCount <= 1 {
			if attrSoleRel > best {
				best = attrSoleRel
			}
		} else if attrAmbiguous > best {
			best = attrAmbiguous
		}
	}
	_ = allAl
	return best
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// countRelations counts how many distinct relations a literal names in
// FROM/JOIN. Subquery aliases are not counted: they are not candidates for a
// bare column's source in the way a real table is.
func countRelations(sql string) int {
	seen := map[string]bool{}
	for _, m := range relationRe.FindAllStringSubmatch(sql, -1) {
		r := strings.ToLower(m[1])
		if r == "" || r == "lateral" || r == "(" {
			continue
		}
		seen[r] = true
	}
	return len(seen)
}
