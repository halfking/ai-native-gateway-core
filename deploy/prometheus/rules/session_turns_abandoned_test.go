package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Gates for the session_turns abandoned landing pad's alerts (migration 820,
// audit §9.92). These replaced the request-abandoned alert gates, which
// guarded the 819 design the user overturned.
//
// The point of the third alert is the one that is easy to get wrong: the
// landing pad is written from the synchronous update path while the terminal
// turn arrives through the async outbox, so `mark_no_row` is the *normal*
// outcome, not a fault. An alert that treats it as a fault trains people to
// ignore it; an alert that omits it leaves a silently under-marking pad that
// still looks authoritative.

const abandonedRulesFile = "session-turns-abandoned.yml"

type alertGroupFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert string            `yaml:"alert"`
			Expr  string            `yaml:"expr"`
			For   string            `yaml:"for"`
			Label map[string]string `yaml:"labels"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func loadAbandonedRules(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(abandonedRulesFile)
	require.NoError(t, err, "read %s", abandonedRulesFile)

	var file alertGroupFile
	require.NoError(t, yaml.Unmarshal(data, &file), "parse %s", abandonedRulesFile)
	require.Len(t, file.Groups, 1, "expected exactly one group")

	byAlert := map[string]string{}
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.NotEmpty(t, r.For, "alert %s has no for: window — it fires on a single scrape", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		require.NotContains(t, byAlert, r.Alert, "duplicate alert name %s", r.Alert)
		byAlert[r.Alert] = r.Expr
	}
	require.NotEmpty(t, byAlert, "the file parsed to zero alerts")
	return byAlert
}

// TestAbandonedTurnAlertsCoverTheThreeFailureModes pins that all three bad
// states have an alert, and that the names are the ones the runbook refers to.
func TestAbandonedTurnAlertsCoverTheThreeFailureModes(t *testing.T) {
	byAlert := loadAbandonedRules(t)

	for _, name := range []string{
		"AbandonedTurnPadNeverWritten",         // ① never written
		"AbandonedTurnPadWritesFailing",        // ② written but failing
		"AbandonedTurnPadMostlyMissingItsRows", // ③ written but mostly hits no row
	} {
		require.Contains(t, byAlert, name, "no alert for failure mode %s", name)
	}
	require.Len(t, byAlert, 3, "an extra alert nobody has justified is drift")
}

// TestNeverWrittenAlertUsesAbsentNotARateZero pins the ① predicate shape.
//
// promtool scenario D forced this: with `rate(...) == 0` (or the `or 0 * …`
// variant), an instance whose mark series EXISTS but has no increments also
// evaluates to zero, so the alert fires on a healthy host. Scenarios A (no
// series ⇒ must fire) and D (series with zero increments ⇒ must not fire)
// have to pass together; only `absent()` separates them.
func TestNeverWrittenAlertUsesAbsentNotARateZero(t *testing.T) {
	expr := loadAbandonedRules(t)["AbandonedTurnPadNeverWritten"]

	// `unless`, not `absent`. Both were tried against promtool:
	//   absent() does not accept a `by` clause (parse error), and — the real
	//   blocker — it yields an EMPTY label set, so
	//   `absent(...) and on(job,instance) (up == 1)` never matches and the
	//   alert is silent in the case it exists for (scenario A returned got=[]).
	require.Regexp(t, `unless`, expr,
		"the never-written alert must use `unless`: rate(...)==0 cannot distinguish "+
			"\"no series\" (must fire) from \"series present, zero increments\" "+
			"(must not fire) — promtool scenario D covers the second case")
	require.NotRegexp(t, `rate\([^)]*op="mark"[^)]*\]\[[0-9a-z]+\]\)\)\s*\n?\s*==\s*0`,
		"the never-written alert must not compare a mark rate to 0; that is the "+
			"form that fires on a healthy host with a flat counter")
	require.NotRegexp(t, `absent`, expr,
		"absent() returns an empty label set, so the `and on(job,instance)` guard "+
			"below can never match it — measured, not assumed")
	require.Contains(t, expr, `up{job=~"llm-gateway.*"}`,
		"the never-written alert must be guarded by `up`, otherwise a dead process "+
			"reports 'pad not wired' — two different faults, one misleading alert")
}

// TestMissingRowsAlertUsesARatioNotAnAbsoluteCount pins the ③ threshold shape.
//
// The failure it must catch is "the pad is systematically missing its rows",
// whose absolute magnitude scales with deployment traffic. An absolute-count
// threshold would either be permanently red on a busy host or permanently
// silent on a quiet one.
func TestMissingRowsAlertUsesARatioNotAnAbsoluteCount(t *testing.T) {
	expr := loadAbandonedRules(t)["AbandonedTurnPadMostlyMissingItsRows"]

	require.Contains(t, expr, "mark_no_row",
		"the ③ alert must be built on mark_no_row — that is the only signal that "+
			"distinguishes 'the pad is under-marking' from 'the pad is fine'")
	// Match against a **whitespace-collapsed** copy. These are YAML block
	// scalars, so the author may wrap the expression anywhere; a line break
	// between ">" and "0.9 *" is the same expression. The first draft matched
	// raw text and therefore failed on a correctly-wrapped rule — a gate that
	// reports formatting as a missing threshold teaches people to reflow YAML
	// instead of fixing the threshold.
	flat := strings.Join(strings.Fields(expr), " ")
	require.Regexp(t, `0\.9 \*`, flat,
		"the ③ alert must compare against a ratio (0.9 * …), not an absolute count")
	require.NotRegexp(t, `llm_gateway_[a-z_]+_total > [0-9]{2,}`, flat,
		"the ③ alert must not use an absolute-count threshold on any counter")
}

// TestAlertsDoNotReadTheRemoved819Metric pins that no rule still points at the
// metric the overturned 819 design produced.
//
// A stale rule does not fail loudly: it keeps scraping a name nothing exports,
// evaluates to empty, and nobody notices until the failure mode it was written
// for happens.
func TestAlertsDoNotReadTheRemoved819Metric(t *testing.T) {
	byAlert := loadAbandonedRules(t)

	for name, expr := range byAlert {
		require.NotContains(t, expr, "request_abandoned_marker_ops_total",
			"alert %s still reads the 819 metric; nothing exports it any more, so the "+
				"rule is permanently silent", name)
	}
}

// TestAbandonedTurnMetricIsProducedInCode closes the loop the other direction:
// a metric named in a rule but never produced is a decoration in the opposite
// direction (§9.37's "a metric with no alert is decoration" has a twin here).
func TestAbandonedTurnMetricIsProducedInCode(t *testing.T) {
	src, err := os.ReadFile(
		"../../../domains/hooks/observability/telemetry/abandoned_turn_metrics.go")
	require.NoError(t, err)
	require.Contains(t, string(src), "llm_gateway_abandoned_turn_ops_total",
		"the metric the rules read is not declared in code")

	// Every op the rules filter on must be a label value the code can emit.
	for _, op := range []string{"mark", "mark_failed", "mark_no_row"} {
		require.Contains(t, string(src), `"`+op+`"`,
			"op %q is filtered on by an alert but never produced in code", op)
	}
}

// TestRulesStateTheUncalibratedThreshold is an honesty gate.
//
// The 0.9 threshold is derived from the design, not measured: migration 820 is
// not deployed to 252. A future reader must not mistake it for a calibrated
// value, because "we picked 0.9 because the number looked reasonable" is
// exactly the kind of unexamined constant that survives for years.
func TestRulesStateTheUncalibratedThreshold(t *testing.T) {
	data, err := os.ReadFile(abandonedRulesFile)
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "0.9")
	require.Contains(t, text, "未经生产实测",
		"the 0.9 threshold must be labelled as uncalibrated in the file itself")
	// Match the idea, not one exact wording: the file says 「回归校准」.
	require.Regexp(t, "校准", text,
		"the file must record that the threshold needs recalibration after first deploy")
}
