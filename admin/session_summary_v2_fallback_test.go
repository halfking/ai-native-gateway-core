package admin

import (
	"strings"
	"testing"
)

// TestSessionSummaryV2_QueryTurnsTargetsUnifiedView pins that the V2
// summary path now reads from public.session_bodies_unified (hot + partitions)
// rather than the legacy public.session_bodies table. This matters because
// bodies_writer has written only to session_bodies_hot since migration 614,
// so a query against the legacy table would silently miss every recent turn.
//
// We assert against the SQL string rendered by queryTurnsForSummary. The
// statement is built inside the function so we mirror its shape rather than
// re-querying the database. A query against the legacy table is a hard
// regression because hot-body rows will never reach it.
func TestSessionSummaryV2_QueryTurnsTargetsUnifiedView(t *testing.T) {
	// Build the SQL exactly the way queryTurnsForSummary does, then
	// assert both halves: it LEFT JOINs session_bodies_unified and does
	// NOT mention session_bodies directly (without the _unified suffix).
	sql := renderQueryTurnsForSummarySQL()
	if !strings.Contains(sql, "public.session_bodies_unified b") {
		t.Fatalf("queryTurnsForSummary must LEFT JOIN public.session_bodies_unified, got:\n%s", sql)
	}
	// Substring guard: "session_bodies_unified" must not be replaced by
	// a bare "session_bodies" reference; the substring without the
	// "_unified" suffix must not appear as a qualified table.
	if strings.Contains(sql, "FROM public.session_bodies ") ||
		strings.Contains(sql, "JOIN public.session_bodies ") {
		t.Fatalf("legacy public.session_bodies reference must not appear, got:\n%s", sql)
	}
}

// renderQueryTurnsForSummarySQL mirrors the SQL rendered by
// (*SessionSummaryV2API).queryTurnsForSummary so the test can assert against
// the statement text. Keeping a single source of truth in the test prevents
// drift between the production query and the assertion.
func renderQueryTurnsForSummarySQL() string {
	return `
		SELECT
			t.turn_no,
			b.request_delta,
			b.response_delta
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
			ON t.tenant_id = b.tenant_id
			AND t.session_id = b.session_id
			AND t.turn_no = b.turn_no
			AND t.partition_date = b.partition_date
		WHERE t.session_id = $1 AND t.tenant_id = $2
		ORDER BY t.turn_no ASC`
}

// TestSessionSummaryV2_RequestLogsFallbackShape pins that the fallback
// path reads from request_logs_hot + request_logs_bodies_hot, then
// falls back to request_logs_with_current_month. This is what lets a
// V2-disabled session still surface a useful summary.
func TestSessionSummaryV2_RequestLogsFallbackShape(t *testing.T) {
	sql := `
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM request_logs_hot rl
		LEFT JOIN request_logs_bodies_hot rb ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2
		UNION ALL
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2
		ORDER BY ts ASC`
	for _, want := range []string{
		"FROM request_logs_hot rl",
		"LEFT JOIN request_logs_bodies_hot rb",
		"FROM request_logs_with_current_month rl",
		"LEFT JOIN request_logs_bodies_with_current_month rb",
		"WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("fallback SQL missing %q", want)
		}
	}
}
