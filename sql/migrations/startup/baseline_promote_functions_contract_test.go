package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// migration698Tables lists the nine hot-to-partition promote functions that
// migration 698 pinned to Asia/Shanghai, in the objects/ canonical naming
// scheme (promote_<table>_hot_to_partition_interval_integer.sql).
var migration698Tables = []string{
	"candidate_failure_logs",
	"credential_model_index",
	"credit_ledger",
	"request_logs_bodies",
	"request_logs",
	"request_wal",
	"routing_decision_log",
	"tool_usage_stats",
	"usage_ledger",
}

// promoteFnSigRe matches one promote hot_to_partition signature start; the
// body is located dollar-quote-aware below because promote bodies mix `$$`
// and `$function$` tags (unlike the ensure family, where the fixed `$$;`
// terminator in the ensure contract test is safe).
var promoteFnSigRe = regexp.MustCompile(`CREATE (?:OR REPLACE )?FUNCTION public\.(promote_\w+_hot_to_partition)\s*\(`)
var dollarTagRe = regexp.MustCompile(`AS (\$\w*\$)`)

// normalizeSQLBody strips line comments then collapses whitespace (reusing
// the ensure contract's normalizeSQLWhitespace), so the promote bodies'
// explanatory comment between BEGIN and the 698 pin cannot fake (or mask)
// the pin-is-first-statement contract. Comment stripping mirrors the
// sequence-script clobber scanner's treatment of prose.
func normalizeSQLBody(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return normalizeSQLWhitespace(strings.Join(lines, "\n"))
}

// objectsPromoteDir holds the single-source canonical bodies migration 698
// was generated from; the baselines must agree with them. Relative to this
// package (sql/migrations/startup), ../.. is sql/.
const objectsPromoteDir = "../../objects/functions"

func extractPromoteHotFunctions(t *testing.T, sql, label string) map[string]string {
	t.Helper()
	fns := make(map[string]string)
	type span struct {
		name string
		end  int
	}
	var spans []span
	idx := promoteFnSigRe.FindAllStringSubmatchIndex(sql, -1)
	for _, m := range idx {
		name := sql[m[2]:m[3]]
		if _, dup := fns[name]; dup {
			t.Errorf("%s baseline defines promote function %s more than once", label, name)
			continue
		}
		tagM := dollarTagRe.FindStringSubmatch(sql[m[1]:])
		if tagM == nil {
			t.Fatalf("%s baseline %s: no dollar-quote tag found", label, name)
		}
		bodyStart := m[1] + len(tagM[0])
		end := strings.Index(sql[bodyStart:], tagM[1]+";")
		if end < 0 {
			t.Fatalf("%s baseline %s: unterminated dollar-quoted body", label, name)
		}
		fns[name] = normalizeSQLBody(sql[m[0] : bodyStart+end+len(tagM[1])+1])
		spans = append(spans, span{name: name, end: bodyStart + end})
	}
	_ = spans
	return fns
}

func objectsCanonicalBody(t *testing.T, table string) (string, string) {
	t.Helper()
	name := "promote_" + table + "_hot_to_partition"
	path := filepath.Join(objectsPromoteDir, name+"_interval_integer.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical %s: %v", path, err)
	}
	fns := extractPromoteHotFunctions(t, string(b), "objects/"+table)
	if len(fns) != 1 {
		t.Fatalf("canonical %s must define exactly one function, got %d", path, len(fns))
	}
	body, ok := fns[name]
	if !ok {
		t.Fatalf("canonical %s does not define %s", path, name)
	}
	return name, body
}

// baselinePromoteFunctionsNoObjectsFile are promote functions the Go resident
// scheduler (bg/partition_manager.go promoteSpecs) calls on every tick that
// existed ONLY as out-of-band migrations (534/579/580) or in-place edits —
// never in any baseline and with no sql/objects/functions canonical.
// R16 (2026-09-12): their authoritative-dump bodies (2026-09-12, local
// llm-gateway-pg; promote_session_module_executions / promote_dashboard_access
// already carry the 659-lineage advisory lock + SKIP LOCKED, promote_handoff
// logs is the rewritten hot-drain form with p_batch_size=200) were inserted
// into all three baselines. Three-way body equality is the contract here;
// the objects/ comparison and the 698 pin assertions do NOT apply — these
// bodies were never 698-pinned and pinning them is a deliberate migration
// (next number ≥701), not a baseline edit.
var baselinePromoteFunctionsNoObjectsFile = []string{
	"promote_dashboard_access_events_hot_to_partition",
	"promote_handoff_logs_hot_to_partition",
	"promote_session_module_executions_hot_to_partition",
}

// TestBaselinePromoteHotFunctionsThreeWayConsistency extends the ensure
// contract to the promote side (2026-09-12 audit P2-3 residual): the three
// 01-schema.sql baselines must carry the 698-pinned hot_to_partition bodies —
// identical to the sql/objects/functions canonicals and to each other. The
// promote bodies drifted independently (canonical snapshot was 659-era, the
// deploy/installer mirrors 697-era without the pin), and a baseline-only
// re-import would run unpinned month routing against 694-pinned partition
// bounds — the exact 23514 failure mode migration 698 closed.
func TestBaselinePromoteHotFunctionsThreeWayConsistency(t *testing.T) {
	expected := make(map[string]string, len(migration698Tables))
	for _, table := range migration698Tables {
		name, body := objectsCanonicalBody(t, table)
		expected[name] = body
	}

	extracted := make(map[string]map[string]string, len(baselineEnsureSources))
	for label, rel := range baselineEnsureSources {
		extracted[label] = extractPromoteHotFunctions(t, readBaseline(t, rel), label)
	}

	labels := make([]string, 0, len(extracted))
	for label := range extracted {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		got := extracted[label]
		for name, want := range expected {
			body, ok := got[name]
			if !ok {
				t.Errorf("%s baseline missing promote function %s", label, name)
				continue
			}
			if body != want {
				t.Errorf("%s baseline body drift for %s (must equal objects/ canonical)\n want: %s\n got:  %s",
					label, name, want, body)
				continue
			}
			// 698 contract inside each promote body: Shanghai pin first
			// statement after BEGIN, month derivation strictly after it.
			if !strings.Contains(body, "BEGIN SET LOCAL TIME ZONE 'Asia/Shanghai';") {
				t.Errorf("%s baseline %s: Shanghai pin is not the first statement after BEGIN", label, name)
			}
			if strings.Index(body, "date_trunc(") < strings.Index(body, "SET LOCAL") {
				t.Errorf("%s baseline %s: month grouping evaluated before the timezone pin", label, name)
			}
		}

		// R16: no-objects-file promote functions — presence + three-way
		// equality only (bodies compared across the baselines below).
		for _, name := range baselinePromoteFunctionsNoObjectsFile {
			if _, ok := got[name]; !ok {
				t.Errorf("%s baseline missing promote function %s (no objects/ canonical, dump-lineage)", label, name)
			}
		}
	}

	ref := extracted["canonical"]
	for _, name := range baselinePromoteFunctionsNoObjectsFile {
		want, ok := ref[name]
		if !ok {
			continue // reported above
		}
		for _, label := range labels {
			body, ok := extracted[label][name]
			if !ok {
				continue
			}
			if body != want {
				t.Errorf("%s baseline body drift for %s (dump-lineage, no objects/ canonical)\n canonical: %s\n %s: %s",
					label, name, want, label, body)
			}
		}
	}

	// 695/697 lineage must survive inside every request_logs promote body:
	// the self-heal demote and the three system_fingerprint column lists.
	for _, label := range labels {
		body, ok := extracted[label]["promote_request_logs_hot_to_partition"]
		if !ok {
			continue // reported above
		}
		if got := strings.Count(body, "system_fingerprint"); got != 3 {
			t.Errorf("%s baseline request_logs promote: system_fingerprint columns = %d, want 3 (697)", label, got)
		}
		if !strings.Contains(body, "migration 695 self-heal") {
			t.Errorf("%s baseline request_logs promote lost the 695 self-heal demote", label)
		}
	}
}
