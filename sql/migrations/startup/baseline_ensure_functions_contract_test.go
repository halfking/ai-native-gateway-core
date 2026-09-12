package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// baselineEnsureSources lists the three final-state 01-schema.sql baselines
// that must agree on every partition ensure function:
//
//   - sql/schema/01-schema.sql            — canonical snapshot (db-changelog source)
//   - deploy/sql/schemas/baseline/...     — deploy-track mirror shipped to 154/245/252
//   - installer/.../embeddata/01-schema.sql — fresh-install snapshot
//
// 2026-09-12 audit (R15): all three had drifted independently — the two 334/335
// ensure functions were absent everywhere (breaking the promote chain's PERFORM
// calls on fresh installs, because migrations <478 never replay), and the
// installer copy still carried pre-689/694 bodies (candidate RETURNing void,
// bodies/WAL-next columnar). This contract pins the ensure-function surface so
// the next baseline regeneration cannot regress it silently.
var baselineEnsureSources = map[string]string{
	"canonical":       "../../schema/01-schema.sql",
	"deploy-baseline": "../../../deploy/sql/schemas/baseline/01-schema.sql",
	"installer-embed": "../../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
}

// baselineEnsureFnRe spans one CREATE [OR REPLACE] FUNCTION public.ensure_*(...)
// definition through its closing "$$;". The ensure bodies never nest dollar
// quotes, so the first "$$;" terminates the definition.
var baselineEnsureFnRe = regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION public\.(ensure_\w+)\(.*?\$\$;`)

// normalizeSQLWhitespace collapses all whitespace runs so the installer copy's
// historical line-ending/indent style cannot mask (or fake) agreement.
func normalizeSQLWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func readBaseline(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rel))
	if err != nil {
		t.Fatalf("read baseline %s: %v", rel, err)
	}
	return string(b)
}

func extractEnsureFunctions(t *testing.T, sql string) map[string]string {
	t.Helper()
	fns := make(map[string]string)
	seen := make(map[string]int)
	for _, m := range baselineEnsureFnRe.FindAllStringSubmatch(sql, -1) {
		name := m[1]
		seen[name]++
		fns[name] = normalizeSQLWhitespace(m[0])
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("baseline defines ensure function %s %d times", name, n)
		}
	}
	return fns
}

// TestBaselineEnsureFunctionsThreeWayConsistency asserts that the three
// 01-schema.sql baselines define the same set of ensure_* partition functions
// with identical (whitespace-normalized) bodies, that every 694 function is
// present and Shanghai-pinned, and that the 687-era storage policy survived:
// hot/promote-facing partitions heap, archive tiers columnar.
func TestBaselineEnsureFunctionsThreeWayConsistency(t *testing.T) {
	extracted := make(map[string]map[string]string, len(baselineEnsureSources))
	for label, rel := range baselineEnsureSources {
		extracted[label] = extractEnsureFunctions(t, readBaseline(t, rel))
	}

	labels := make([]string, 0, len(extracted))
	for label := range extracted {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	// 1. Same function set everywhere. Fail with the exact per-file delta.
	base := extracted["canonical"]
	for _, label := range labels {
		other := extracted[label]
		for _, fn := range migration694EnsureFunctions {
			if _, ok := base[fn]; !ok {
				continue // reported below
			}
			if _, ok := other[fn]; !ok {
				t.Errorf("%s baseline missing ensure function %s (present in canonical)", label, fn)
			}
		}
	}
	for _, fn := range migration694EnsureFunctions {
		if _, ok := base[fn]; !ok {
			t.Errorf("canonical baseline missing ensure function %s", fn)
		}
	}

	// 2. Identical normalized bodies for every function all three define.
	for _, fn := range migration694EnsureFunctions {
		ref, ok := base[fn]
		if !ok {
			continue
		}
		for _, label := range labels {
			got, ok := extracted[label][fn]
			if !ok {
				continue
			}
			if got != ref {
				t.Errorf("%s baseline body drift for %s:\n canonical: %s\n %s: %s", label, fn, ref, label, got)
			}
		}
	}

	// 3. 694 contract holds inside each baseline: Shanghai pin as the first
	// body statement (the pre-694 DECLARE initializers evaluated before any
	// in-body SET LOCAL could take effect — same trap the 694 migration fixed).
	// Normalization keeps punctuation attached to tokens, so the pin reads
	// "BEGIN SET LOCAL TIME ZONE 'Asia/Shanghai';".
	for _, label := range labels {
		for _, fn := range migration694EnsureFunctions {
			body, ok := extracted[label][fn]
			if !ok {
				continue
			}
			if !strings.Contains(body, "BEGIN SET LOCAL TIME ZONE 'Asia/Shanghai';") {
				t.Errorf("%s baseline %s: Shanghai pin is not the first statement after BEGIN", label, fn)
				continue
			}
			if strings.Index(body, "date_trunc(") < strings.Index(body, "SET LOCAL") {
				t.Errorf("%s baseline %s: calendar derivation evaluated before the timezone pin", label, fn)
			}
		}
	}

	// 4. Storage policy (562/687/689 lineage): request_logs_bodies and both
	// request_wal ensure functions create heap partitions; the three archive
	// tiers stay columnar.
	heapMust := []string{
		"ensure_candidate_failure_logs_partition",
		"ensure_request_logs_bodies_partition",
		"ensure_request_wal_partition",
		"ensure_next_month_request_wal_partition",
	}
	columnarMust := []string{
		"ensure_next_month_archive_partition",
		"ensure_next_month_cmi_archive_partition",
		"ensure_next_month_routing_archive_partition",
	}
	for _, label := range labels {
		for _, fn := range heapMust {
			body, ok := extracted[label][fn]
			if !ok {
				continue
			}
			if strings.Contains(body, "USING columnar") || strings.Contains(body, "PERFORM enforce_columnar_partition") {
				t.Errorf("%s baseline %s must create heap partitions (689)", label, fn)
			}
		}
		for _, fn := range columnarMust {
			body, ok := extracted[label][fn]
			if !ok {
				continue
			}
			if !strings.Contains(body, "USING columnar") {
				t.Errorf("%s baseline %s must keep the archive tier columnar", label, fn)
			}
		}
	}
}
