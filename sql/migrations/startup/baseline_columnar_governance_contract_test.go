package startup

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Columnar governance functions: the four representations that can drift
// independently are the canonical object file, these three baselines, and the
// live database. baseline_ensure_functions_contract_test.go pins the ensure_*
// family across the three baselines; it does NOT cover the columnar_* family,
// which is how the 2026-10-01 incident shipped with a 21-family whitelist in
// the live DB while the repo canonical said one family.
//
// The functions below decide which partitions may be columnarised
// (enforce_columnar_partition / fn_enforce_columnar_event_trigger), which
// ones must never be (columnar_healthcheck's should_be_heap), and which are
// eligible (columnar_insert_only_parents). A silent drift between the three
// baselines means a fresh install, a 154/245/252 deploy, and the canonical
// snapshot each enforce a DIFFERENT storage policy.

var columnarGovernanceFnRe = regexp.MustCompile(
	`(?s)CREATE (?:OR REPLACE )?FUNCTION public\.(columnar_\w+|auto_rotate_to_columnar|enforce_columnar_partition|fn_enforce_columnar_event_trigger)\(.*?AS \$[a-zA-Z_]*\$.*?\$[a-zA-Z_]*\$;`)

// columnarGovernancePinned is the exact set every baseline must define. A new
// governance function must be added here deliberately, not appear in one
// baseline by accident.
var columnarGovernancePinned = []string{
	"columnar_insert_only_parents",
	"columnar_healthcheck",
	"columnar_heal",
	"columnar_drift_report",
	"auto_rotate_to_columnar",
	"enforce_columnar_partition",
	"fn_enforce_columnar_event_trigger",
}

func extractColumnarGovernance(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range columnarGovernanceFnRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = normalizeSQLWhitespace(m[0])
	}
	return out
}

// TestBaselineColumnarGovernanceThreeWayConsistency pins the columnar_*
// governance family across the three baselines.
func TestBaselineColumnarGovernanceThreeWayConsistency(t *testing.T) {
	extracted := make(map[string]map[string]string, len(baselineEnsureSources))
	for label, rel := range baselineEnsureSources {
		extracted[label] = extractColumnarGovernance(t, readBaseline(t, rel))
	}

	base := extracted["canonical"]
	labels := make([]string, 0, len(extracted))
	for label := range extracted {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, fn := range columnarGovernancePinned {
		ref, ok := base[fn]
		if !ok {
			t.Errorf("canonical baseline missing columnar governance function %s", fn)
			continue
		}
		for _, label := range labels {
			if label == "canonical" {
				continue
			}
			got, ok := extracted[label][fn]
			if !ok {
				t.Errorf("%s baseline missing columnar governance function %s (present in canonical)", label, fn)
				continue
			}
			if got != ref {
				t.Errorf("%s baseline body of %s drifted from canonical\n  want: %.200s\n  got:  %.200s",
					label, fn, ref, got)
			}
		}
	}

	// Unexpected extras: a governance function present in a non-canonical
	// baseline but not pinned/canonical is exactly the "hand-edited one server"
	// shape that caused R17.
	for _, label := range labels {
		if label == "canonical" {
			continue
		}
		for fn := range extracted[label] {
			if !containsString(columnarGovernancePinned, fn) {
				t.Errorf("%s baseline defines unpinned columnar governance function %s; add it to columnarGovernancePinned deliberately", label, fn)
			}
		}
	}
}

// TestColumnarHealthcheckGovernsR17RolledBackFamilies pins the *property* the
// 2026-10-01 healthcheck lacked: the 20 families that the runaway
// enforce_columnar_trigger converted must be classified as must-be-heap, so a
// recurrence shows up as compliant=false instead of expected='unknown'.
//
// This is deliberately a semantic assertion rather than a body-diff: an
// earlier gate compared bodies, which stays green when all three baselines
// drift together — the exact R17 failure mode (the live DB drifted, the repo
// did not; and three baselines could drift as a block just as easily).
func TestColumnarHealthcheckGovernsR17RolledBackFamilies(t *testing.T) {
	canonical := readBaseline(t, baselineEnsureSources["canonical"])
	fns := extractColumnarGovernance(t, canonical)
	healthcheck, ok := fns["columnar_healthcheck"]
	if !ok {
		t.Fatal("canonical baseline missing columnar_healthcheck")
	}

	// The 21-family list that R17 rolled back, minus routing_decision_log
	// (that one IS the canonical should_be_columnar).
	r17Families := []string{
		"sessions", "session_turns", "session_turn_details", "session_bodies",
		"usage_facts", "stats_event_inbox", "credential_model_index",
		"auto_route_selections", "session_memora", "session_censors",
		"system_probe_runs", "credit_ledger", "tool_usage_stats",
		"session_tools", "session_module_executions", "cache_metrics",
		"dashboard_access_events", "model_probe_runs", "handoff_logs",
		"supplier_errors",
	}
	for _, fam := range r17Families {
		if !strings.Contains(healthcheck, "'"+fam+"'") {
			t.Errorf("columnar_healthcheck no longer names %q; a recurrence of the "+
				"2026-10-01 conversion would report expected='unknown' instead of non-compliant", fam)
		}
	}

	// The relkind filter: without it 641 of 754 reported rows were partitioned
	// INDEX children, and columnar_drift_report sums their sizes as if they
	// were table partitions.
	if !strings.Contains(healthcheck, "p.relkind = 'p'") || !strings.Contains(healthcheck, "c.relkind = 'r'") {
		t.Error("columnar_healthcheck lost its relkind filter; pg_inherits children of " +
			"partitioned indexes (relkind 'i') will be reported as table partitions")
	}

	// The eligible set must stay exactly one family. Widening this list is what
	// the enforce_columnar_trigger acted on during the incident.
	if !strings.Contains(fns["columnar_insert_only_parents"], "ARRAY['routing_decision_log']") {
		t.Error("columnar_insert_only_parents canonical body no longer pins the single routing_decision_log family")
	}
}

// TestAutoRotateToColumnarCastsCatalogNameToText pins the fix for a function
// that could never return a row: pg_class.relname and pg_am.amname are type
// `name`, the RETURNS TABLE columns are `text`, so every invocation failed
// with "structure of query does not match function result type ... in column 2"
// — including dry_run := true. Measured on 252, 2026-10-01.
func TestAutoRotateToColumnarCastsCatalogNameToText(t *testing.T) {
	canonical := readBaseline(t, baselineEnsureSources["canonical"])
	fns := extractColumnarGovernance(t, canonical)
	body, ok := fns["auto_rotate_to_columnar"]
	if !ok {
		t.Fatal("canonical baseline missing auto_rotate_to_columnar")
	}
	for _, want := range []string{"parent.relname::text", "child.relname::text", "am.amname::text"} {
		if !strings.Contains(body, want) {
			t.Errorf("auto_rotate_to_columnar missing %q; the function raises "+
				"\"structure of query does not match function result type\" on its first row", want)
		}
	}
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
