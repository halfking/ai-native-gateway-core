package bg

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The export script and the health check answer two different questions about
// the same rows ("has it happened yet" vs "has it already happened"), so their
// SQL is allowed to differ in exactly one respect: the time window. Everything
// that decides *which rows count as damaged* must be identical, or the export
// under-reports what the check reports and the operator decides on a smaller
// problem than the one they have.
//
// This is a source-shape ratchet, not a behavioral test: it compares the two
// artifacts as text. It is deliberately specific about the three load-bearing
// tokens instead of grepping for something generic like "cost_usd", because a
// generic match passes under every mutation that matters.

const exportScriptPath = "../scripts/export-negative-cost-rows.sh"

func readRecordedCostCheckSQL(t *testing.T) string {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == "recorded_cost_is_negative" {
			return c.Query
		}
	}
	// NOT a skip. If the check is renamed or removed, this file's claims about
	// it become unverifiable, and a silent skip turns the ratchet into a
	// constant-green assertion.
	t.Fatalf("check recorded_cost_is_negative not found in AllHealthChecks() "+
		"(%d checks registered); this test cannot verify what it claims to", len(AllHealthChecks()))
	return ""
}

func readExportScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(exportScriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", exportScriptPath, err)
	}
	return string(b)
}

// groupingColumns returns the model-ish columns each artifact groups by. This
// is the assertion that carries the most weight: request_logs has six
// model-ish columns and raw_model_name is empty on every damaged row in the
// real database, so grouping by it turns "which credential and model is
// broken" into "which credentials have a blank model".
func groupingColumns(sql string) map[string]bool {
	out := map[string]bool{}
	for _, col := range []string{
		"client_model", "raw_model_name", "canonical_model",
		"model_chosen", "outbound_model", "provider_model",
	} {
		if regexp.MustCompile(`(?m)(GROUP\s+BY[^;]*|\bORDER\s+BY[^;]*)\b` + col + `\b`).
			MatchString(sql) {
			out[col] = true
		}
	}
	return out
}

func TestNegativeCostExportParity(t *testing.T) {
	checkSQL := readRecordedCostCheckSQL(t)
	script := readExportScript(t)

	// 1. Same table. The export additionally reads the ledger and the rollups
	//    (that is the whole point of it), so this checks presence, not equality.
	for name, src := range map[string]string{"health check": checkSQL, "export script": script} {
		if !strings.Contains(src, "public.request_logs") {
			t.Errorf("%s: must read public.request_logs to find the traceable tail; "+
				"pointing it at the rollups alone would report damage with no source rows",
				name)
		}
	}

	// 2. Same sign predicate. An export that finds "cost_usd > 0" would produce
	//    a confident, completely inverted report.
	for name, src := range map[string]string{"health check": checkSQL, "export script": script} {
		if !strings.Contains(src, "cost_usd < 0") {
			t.Errorf("%s: must select rows with cost_usd < 0; a different sign "+
				"inverts the finding while looking equally authoritative", name)
		}
	}

	// 3. Same grouping column, and it must be the populated one.
	checkGroups := groupingColumns(checkSQL)
	scriptGroups := groupingColumns(script)
	if !checkGroups["outbound_model"] {
		t.Errorf("health check: must group by outbound_model, got %v. "+
			"raw_model_name is empty on every damaged row in the real database, so "+
			"that grouping collapses distinct models into blank-model groups", keysOf(checkGroups))
	}
	if !scriptGroups["outbound_model"] {
		t.Errorf("export script: must group by outbound_model, got %v", keysOf(scriptGroups))
	}
	for _, col := range []string{"client_model", "canonical_model", "model_chosen", "provider_model"} {
		if checkGroups[col] || scriptGroups[col] {
			t.Errorf("neither artifact may group by %q (health check %v, export %v): "+
				"it is a different question than the one this check asks", col,
				keysOf(checkGroups), keysOf(scriptGroups))
		}
	}

	// 4. The two artifacts must not have drifted into different notions of
	//    "damaged": if either one starts grouping by a model column the other
	//    does not, the export's combo list stops matching the check's entities.
	for col := range checkGroups {
		if !scriptGroups[col] {
			t.Errorf("health check groups by %q but the export script does not; "+
				"the export would report a different set of damaged combinations", col)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
