package rules_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Gates for the session_turns abandoned landing pad's alerts (migration 821,
// audit §9.92/§9.93). These replaced the request-abandoned alert gates, which
// guarded the 819 design the user overturned, and were rewritten again in
// §9.93 when the pad moved out of the synchronous telemetry path.
//
// That relocation inverted the meaning of the third alert, which is the one
// easy to get wrong in both directions:
//
//   before: the pad was written synchronously while the terminal turn arrived
//           through the async outbox, so `mark_no_row` was the NORMAL outcome
//           and its high share was not a fault;
//   now:    the pad runs immediately after w.Write in the same goroutine, so
//           `mark_no_row`'s healthy value is ≈0 and a high share means the pad
//           is systematically not landing the flag.
//
// An alert that keeps the old semantics fires on a healthy system; one that
// drops the alert entirely leaves a silently under-marking pad that still
// looks authoritative. Both are worse than the pad not existing.

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
		"AbandonedTurnPadWritesFailing",        // ② written but not landing
		"AbandonedTurnPadMostlyMissingItsRows", // ③ mostly hitting no row
	} {
		require.Contains(t, byAlert, name, "no alert for failure mode %s", name)
	}
	require.Len(t, byAlert, 3, "an extra alert nobody has justified is drift")
}

// TestNeverWrittenAlertUsesAbsentNotARateZero pins the ① predicate shape.
//
// promtool scenario D forces this: with `rate(...) == 0` (or the `or 0 * …`
// variant), an instance whose mark series EXISTS but has no increments also
// evaluates to zero, so the alert fires on a healthy host. Scenarios A (no
// series ⇒ must fire) and D (series with zero increments ⇒ must not fire)
// have to pass together; only `unless` separates them.
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
// The failure it must catch is "the pad is systematically not landing its
// flags", whose absolute magnitude scales with deployment traffic. An
// absolute-count threshold would either be permanently red on a busy host or
// permanently silent on a quiet one.
func TestMissingRowsAlertUsesARatioNotAnAbsoluteCount(t *testing.T) {
	expr := loadAbandonedRules(t)["AbandonedTurnPadMostlyMissingItsRows"]

	require.Contains(t, expr, "mark_no_row",
		"the ③ alert must be built on mark_no_row — that is the only signal that "+
			"distinguishes 'the pad is under-marking' from 'the pad is fine'")
	// Match against a **whitespace-collapsed** copy. These are YAML block
	// scalars, so the author may wrap the expression anywhere; a line break
	// between ">" and "0.5 *" is the same expression. The first draft matched
	// raw text and therefore failed on a correctly-wrapped rule — a gate that
	// reports formatting as a missing threshold teaches people to reflow YAML
	// instead of fixing the threshold.
	flat := strings.Join(strings.Fields(expr), " ")
	require.Regexp(t, `0\.\d+ \*`, flat,
		"the ③ alert must compare against a ratio (0.5 * …), not an absolute count")
	require.NotRegexp(t, `llm_gateway_[a-z_]+_total > [0-9]{2,}`, flat,
		"the ③ alert must not use an absolute-count threshold on any counter")
}

// TestMissingRowsThresholdStaysBelowTheBrokenImplementation pins the *value*.
//
// §9.93 moved the pad to just after w.Write, which inverted what mark_no_row
// means. The old threshold (0.9) was calibrated to "the async race is normal,
// so only an almost-total miss is a fault" — under the new placement that
// reading is wrong twice over: the healthy value is ≈0, and 0.9 is high enough
// that the failure it must catch (the first implementation, which missed
// essentially every row) only fires at 0.99.
//
// ⚠ This is a deliberately pinned constant, not a hand-counted number: it is
// a design decision, and changing it is a decision that must be accompanied by
// re-running the promtool scenarios (C asserts the firing side, G asserts the
// non-firing side at 0.4). Both scenarios must be revisited with it.
func TestMissingRowsThresholdStaysBelowTheBrokenImplementation(t *testing.T) {
	expr := loadAbandonedRules(t)["AbandonedTurnPadMostlyMissingItsRows"]
	flat := strings.Join(strings.Fields(expr), " ")

	i := strings.Index(flat, "0.")
	require.NotEqual(t, -1, i, "no ratio literal found in the ③ expression")
	j := i
	for j < len(flat) && (flat[j] == '0' || flat[j] == '.' || (flat[j] >= '0' && flat[j] <= '9')) {
		j++
	}
	lit := flat[i:j]
	threshold, err := strconv.ParseFloat(lit, 64)
	require.NoError(t, err, "could not parse the ③ ratio literal %q", lit)

	require.LessOrEqual(t, threshold, 0.5,
		"the ③ threshold is %s. Under the §9.93 placement the healthy mark_no_row "+
			"share is ≈0, and a threshold above 0.5 would not fire on a moderately "+
			"broken pad. If you are raising it deliberately, re-run promtool "+
			"scenarios C and G in rule_tests/session-turns-abandoned_test.yml.",
		lit)
}

// TestWritesFailingAlertCoversTheNoPoolState pins that ② reads the op that
// means "never even attempted".
//
// mark_no_pool was introduced by the §9.93 relocation: with the pad running
// after w.Write, a nil mirror-outbox pool produces neither `mark` nor
// `mark_failed`. An alert that filters only on mark_failed is then blind to a
// state where the pad never runs, and the metrics look clean — "a metric with
// no alert is decoration" in the other direction.
func TestWritesFailingAlertCoversTheNoPoolState(t *testing.T) {
	expr := loadAbandonedRules(t)["AbandonedTurnPadWritesFailing"]
	require.Regexp(t, `op=~"mark_failed\|mark_no_pool"`, expr,
		"the ② alert must include mark_no_pool: a nil mirror-outbox pool produces "+
			"neither mark nor mark_failed, so filtering on mark_failed alone leaves "+
			"「the pad never ran」 invisible (§9.93)")
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
//
// ⚠ The path is the one §9.93 moved the pad to. Pinning the OLD path would have
// kept this gate green against a file that no longer exists — `os.ReadFile`
// fails loudly there, but only after someone had already moved the code.
func TestAbandonedTurnMetricIsProducedInCode(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..",
		"internal/sessionv2mirror/abandoned_turn.go"))
	require.NoError(t, err, "the pad moved to internal/sessionv2mirror in §9.93; "+
		"this gate must follow it or it guards a file that is gone")
	require.Contains(t, string(src), "llm_gateway_abandoned_turn_ops_total",
		"the metric the rules read is not declared in code")

	// Every op the rules filter on must be a label value the code can emit.
	for _, op := range []string{"mark", "mark_failed", "mark_no_row", "mark_no_pool"} {
		require.Contains(t, string(src), `"`+op+`"`,
			"op %q is filtered on by an alert but never produced in code", op)
	}
}

// TestRunbookDoesNotPointAtTheDeletedTelemetryPad pins that the runbook's
// first diagnostic step still names a file that exists.
//
// The §9.93 relocation deleted domains/hooks/observability/telemetry/abandoned_turn.go.
// A runbook that says "grep -c markAbandonedTurn in …/telemetry/" sends an
// operator to a path that no longer contains the pad, and grep exits 1 — which
// looks like "the wiring is missing", i.e. the opposite of the truth.
func TestRunbookDoesNotPointAtTheDeletedTelemetryPad(t *testing.T) {
	data, err := os.ReadFile(abandonedRulesFile)
	require.NoError(t, err)
	text := string(data)

	require.NotContains(t, text, "domains/hooks/observability/telemetry/abandoned_turn",
		"the runbook points at the deleted §9.93 telemetry-side pad file")
	require.Contains(t, text, "markAbandonedTurnIfT0Missing",
		"the runbook's wiring check must name the function that actually exists "+
			"(markAbandonedTurnIfT0Missing in internal/sessionv2mirror/)")
}

// TestRulesStateTheUncalibratedThreshold is an honesty gate.
//
// The 0.5 threshold is derived from the structure (healthy mark_no_row ≈ 0),
// not measured: migration 821 is not deployed to 252. A future reader must not
// mistake it for a calibrated value, because "we picked a number because it
// looked reasonable" is exactly the kind of unexamined constant that survives
// for years.
func TestRulesStateTheUncalibratedThreshold(t *testing.T) {
	data, err := os.ReadFile(abandonedRulesFile)
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "0.5")
	require.Contains(t, text, "未经生产实测",
		"the threshold must be labelled as uncalibrated in the file itself")
	// Match the idea, not one exact wording: the file says 「回归校准」.
	require.Regexp(t, "校准", text,
		"the file must record that the threshold needs recalibration after first deploy")
}

// TestRulesUseTheCurrentMigrationNumber guards the 820→821 renumber.
//
// §9.92.2 moved the migration to 821 after upstream 93cbce8a3 took 820, but the
// alert text still said 820 in several places. An operator following the runbook
// would look for a migration that was never shipped and conclude the column
// was never added.
//
// ⚠ The pattern avoids lookbehind/lookahead: this is Go's RE2, which does not
// support them, and `regexp.MustCompile` inside a test **panics** rather than
// failing cleanly. The boundary is expressed as consuming alternatives instead.
func TestRulesUseTheCurrentMigrationNumber(t *testing.T) {
	data, err := os.ReadFile(abandonedRulesFile)
	require.NoError(t, err)
	text := string(data)

	stale820 := regexp.MustCompile(`(?m)(^|[^0-9])820($|[^0-9])`)
	require.NotRegexp(t, stale820, text,
		"the rules still refer to migration 820; the pad is migration 821 "+
			"(820 was taken by upstream 93cbce8a3's audio backfill)")
}
