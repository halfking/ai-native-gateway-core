package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBoardReadFacesExcludeProbeTraffic is the regression gate for the dashboard
// board's 总请求数 / 成功率.
//
// Before the fix, probe traffic was folded into the board's request counters.
// Probes fail far more often than real user traffic, so the board reported a
// success rate that no operator could reproduce from the request-log page.
//
// The minute-rollup read faces (queryBoardSummary / queryBoardTrends /
// queryDimPie) read request_stats_minute, which has NO probe column — the
// exclusion must therefore happen in the WRITER (bg/stats_minute_rollup.go),
// and this test asserts that writer carries it so the read faces can't silently
// regress to "count everything".
func TestBoardReadFacesExcludeProbeTraffic(t *testing.T) {
	rollup, err := os.ReadFile(filepath.Join("..", "bg", "stats_minute_rollup.go"))
	if err != nil {
		t.Fatalf("read bg/stats_minute_rollup.go: %v", err)
	}
	retire, err := os.ReadFile(filepath.Join("..", "bg", "stats_minute_rollup_retire.go"))
	if err != nil {
		t.Fatalf("read bg/stats_minute_rollup_retire.go: %v", err)
	}

	// Every statement that writes OR retires a rollup row must carry the
	// exclusion. A predicate missing from the retire statements leaves probe-only
	// keys classified as "still produced" and permanently stale.
	for name, tc := range map[string]struct {
		src  []byte
		want int
	}{
		"rollup writers":    {src: rollup, want: 4},
		"rollup retire SQL": {src: retire, want: 3},
	} {
		if got := strings.Count(string(tc.src), "ProbeTrafficExclusionPredicateView"); got < tc.want {
			t.Errorf("%s: expected at least %d probe-exclusion predicates, found %d.\n"+
				"  Probe traffic must never reach request_stats_minute / request_stats_dim_minute /\n"+
				"  request_stats_error_drill_minute — those tables have no probe column, so no\n"+
				"  read-time WHERE can undo it.", name, tc.want, got)
		}
	}

	// The live in-memory path (accumulator + Redis board delta) must agree.
	entry, err := os.ReadFile(filepath.Join("..", "domains", "stats", "minute_entry.go"))
	if err != nil {
		t.Fatalf("read domains/stats/minute_entry.go: %v", err)
	}
	if !strings.Contains(string(entry), "IsProbeTraffic(entry)") {
		t.Error("FromTelemetryEntry must call IsProbeTraffic; otherwise probe rows reach\n" +
			"  request_stats_minute and the Redis board delta even though the SQL writers are fixed.")
	}

	// The log-based read faces share one WHERE builder; the predicate must be there.
	where, err := os.ReadFile("board_time_range.go")
	if err != nil {
		t.Fatalf("read admin/board_time_range.go: %v", err)
	}
	if body := goFuncBodyStrict(string(where), "func boardLogsWhere("); !strings.Contains(body, "ProbeTrafficExclusionPredicateView") {
		t.Error("boardLogsWhere must carry the probe exclusion inside its own body; it is the\n" +
			"  shared WHERE for fallbackBoardSummary, fallbackBoardTrends, fallbackBoardPies\n" +
			"  and fillOverviewCountsFromLogs — all of which feed the board's request counts.")
	}

	// fallbackErrorDrill builds its own WHERE (it uses requestLogsFromClause, not
	// boardLogsWhere, because it takes days-relative windows), so the shared
	// builder does not cover it. Without the predicate the error drill returns
	// probe failures the probe-free error pie never showed.
	fallback, err := os.ReadFile("dashboard_board_fallback.go")
	if err != nil {
		t.Fatalf("read admin/dashboard_board_fallback.go: %v", err)
	}
	if body := goFuncBodyStrict(string(fallback), "func (h *Handler) fallbackErrorDrill("); !strings.Contains(body, "ProbeTrafficExclusionPredicateView") {
		t.Error("fallbackErrorDrill must carry the probe exclusion; it is the only board\n" +
			"  read face that does not share boardLogsWhere.")
	}

	// 总积分消耗 must come from probe-filtered logs, not the billing ledger.
	// maas_credit_consumption_buckets stores charged credits with no probe
	// markers, so routing the board back to queryTotalCreditsCharged would
	// silently reintroduce probe charges into the board while the request and
	// success counters next to it stayed probe-free.
	queries, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read admin/dashboard_board_queries.go: %v", err)
	}
	// Bound to fallbackBoardSummary's BODY, not to the file: the helper is
	// still *defined* after a regression, so a whole-file symbol check passes
	// even when the call site was reverted to the billing ledger.
	//
	// Comments are stripped first (same helper degrade_marker_test.go uses):
	// this file's prose deliberately names queryTotalCreditsCharged to explain
	// why it is NOT used, and a raw-slice Contains would match that sentence.
	fbSummary := goFuncBodyStrict(stripGoCommentsKeepLines(string(queries)),
		"func (h *Handler) fallbackBoardSummary(")
	if fbSummary == "" {
		t.Fatal("fallbackBoardSummary not found in admin/dashboard_board_queries.go")
	}
	if !strings.Contains(fbSummary, "queryBoardCreditsExcludingProbes") {
		t.Error("fallbackBoardSummary must call queryBoardCreditsExcludingProbes; reverting to\n" +
			"  queryTotalCreditsCharged re-reads maas_credit_consumption_buckets, which has no\n" +
			"  probe markers and cannot be filtered after the fact — probe charges would\n" +
			"  reappear on the board next to already-probe-free request counters.")
	}
	if strings.Contains(fbSummary, "queryTotalCreditsCharged") {
		t.Error("fallbackBoardSummary still calls queryTotalCreditsCharged (the billing ledger).")
	}

	// Billing and the usage page must stay untouched: the exclusion is a
	// dashboard-view concern, so the shared helper keeps its other caller.
	credits, err := os.ReadFile("usage_credits.go")
	if err != nil {
		t.Fatalf("read admin/usage_credits.go: %v", err)
	}
	if strings.Contains(string(credits), "ProbeTrafficExclusionPredicateView") {
		t.Error("usage_credits.go must NOT gain a probe exclusion: queryTotalCreditsCharged is\n" +
			"  shared with the usage page (admin/usage.go:150) and reports actual charged\n" +
			"  credits. Filtering it would make the usage page disagree with real billing.")
	}
	if !strings.Contains(string(credits), "queryTotalCreditsCharged") {
		t.Error("queryTotalCreditsCharged disappeared; the usage page still needs it.")
	}
}

// goFuncBodyStrict returns the source of a single top-level function declaration,
// from its signature through its closing brace.
//
// Bounding matters, and the obvious bound is still wrong. Cutting at the NEXT
// declaration is not enough: a package-level `var _ = bg.SomeConst` sitting
// between two functions lands inside the slice and satisfies a
// Contains(…, "SomeConst") check, so deleting the predicate from the target
// function still goes green. Cutting at the next column-0 "}" (gofmt guarantees
// only the function's own closing brace sits at column 0) is what actually
// pins the assertion to the function it cares about.
//
// Named ...Strict to avoid colliding with the looser goFuncBody in
// dashboard_board_fallback_parity_test.go, which cuts at the next top-level
// declaration; both stay in package admin, so exactly one may own this name.
func goFuncBodyStrict(src, signature string) string {
	start := strings.Index(src, signature)
	if start < 0 {
		return ""
	}
	rest := src[start:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end+2]
	}
	return rest
}
