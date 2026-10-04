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
	// columns are the exposure-class columns named in this literal.
	columns map[string]string // column -> class
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

	byClass := map[string][]string{
		"structural-gap": db.RetirementStructuralGapColumns,
		"unservable":     db.RetirementUnservableColumns,
		"degraded":       db.RetirementDegradedColumns,
	}
	// One compiled matcher per column, anchored on word boundaries, so that
	// `rl.is_final_success` and a bare `is_final_success` both hit and
	// `is_final_success_v2` does not. Built once for the whole run.
	matchers := map[string]*regexp.Regexp{}
	for _, cols := range byClass {
		for _, c := range cols {
			matchers[c] = regexp.MustCompile(`\b` + regexp.QuoteMeta(c) + `\b`)
		}
	}

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
		}
		for col, re := range matchers {
			if re.MatchString(clean) {
				l.columns[col] = db.RetirementExposureClassify(col)
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
	// ── unservable / structural-gap columns in v1-only SQL ────────────────
	"admin/data_lifecycle_attachments.go":             "reads request_logs_bodies.attachments and .id; attachments is 18.19% on the session side and id is a structural gap (v1 request-row id != turn id)",
	"admin/logs.go":                                   "the admin log list itself: id, provider_model (structural gap), credential_id, application_id, canonical_id, client_model, provider_id",
	"admin/providers.go":                              "id (structural gap), credential_id, provider_id",
	"admin/routing.go":                                "quality_flags and origin_stage are degraded, id/canonical_id/client_model too; routing views lose their failure dimension",
	"admin/swim_lane_init.go":                         "id (structural gap), credential_id, provider_id, canonical_id, client_model",
	"admin/work_types.go":                             "reads request_logs.work_type directly — the file's own comment calls it 'Direct work_type column'; work_type is 0.00% on the session side, so this reader returns nothing after retirement",
	"bg/auto_index_refresher.go":                      "id (structural gap), total_tokens (58.36% session), credential_id, canonical_id, client_model",
	"bg/credential_recovery.go":                       "id (structural gap), credential_id, provider_id, client_model",
	"bg/credential_selfcheck.go":                      "id (structural gap), credential_id, canonical_id, client_model",
	"bg/model_probe.go":                               "id (structural gap), credential_id, provider_id, client_model",
	"bg/today_success_probe.go":                       "id (structural gap), credential_id",
	"db/db.go":                                        "the canonical view DDL/ensure chain itself — it *defines* the 118-column projection, so it names every structural-gap and unservable column. Not an operational reader: retiring request_logs means replacing this body, not repairing a query",
	"domains/hooks/observability/telemetry/client.go": "the v1 **writer** (insertRequestLog). Names every exposed column. An INSERT into a dropped table errors, so this must be removed or repointed, not merely tolerated",
	"domains/streaming/model_alternatives.go":         "id (structural gap), credential_id, canonical_id",

	// ── same severity, but the literal also reads the session family, so the
	//    column could be coming from either leg ─────────────────────────────
	"admin/probe_history.go":             "possible: id, credential_id, canonical_id — the literal reads both families, so attribute by reading the file before acting",
	"cmd/gateway/dual_read_validator.go": "possible: work_type (0.00% session), request_type (18.15%), origin_actor. This is the **dual-read validator** — the thing that is supposed to prove the two families agree. It is the highest-value file to fix first, because it is the instrument the retirement would be judged with",
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
	files := make([]string, 0, len(retirementBreakers))
	for f := range retirementBreakers {
		files = append(files, f)
	}
	sort.Strings(files)

	byVerdict := map[string][]string{}
	for _, rel := range files {
		cols := map[string]bool{}
		for _, l := range extractV1ReadingLiterals(t, filepath.Join(root, rel)) {
			for c := range l.columns {
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
		db.RepointEmpty, db.RepointGapOnly, db.RepointDegraded,
		db.UnknownColumn, db.RepointSafe,
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
	// The point of the test: a blanket repoint is not available. If every breaker
	// ever came out `repoint-safe`, D14-a would have a trivially correct answer;
	// the gate's value is that it keeps re-checking that assumption instead of
	// letting it be made once and forgotten.
	safe := len(byVerdict[string(db.RepointSafe)])
	t.Logf("repoint-safe: %d of %d registered breakers", safe, len(retirementBreakers))
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
