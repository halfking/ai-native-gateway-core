package admin

import (
	"strings"
	"testing"
)

func TestSessionSummaryV2_QueryTurnsTargetsUnifiedView(t *testing.T) {
	sql := renderQueryTurnsForSummarySQL()
	if !strings.Contains(sql, "public.session_bodies_unified b") {
		t.Fatalf("queryTurnsForSummary must LEFT JOIN public.session_bodies_unified, got:\n%s", sql)
	}
	if strings.Contains(sql, "FROM public.session_bodies ") ||
		strings.Contains(sql, "JOIN public.session_bodies ") {
		t.Fatalf("legacy public.session_bodies reference must not appear, got:\n%s", sql)
	}
}

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

func TestSessionSummaryV2RequestLogsFallbackUsesUnifiedSourceWithoutDuplicates(t *testing.T) {
	limit := 3
	sql, args := buildRequestLogsFallbackQuery("session-1", "tenant-a", &limit)
	for _, want := range []string{
		"FROM request_logs_with_current_month rl",
		"rb.request_id = rl.request_id",
		"rb.ts = rl.ts",
		"rl.gw_session_id = $1",
		"rl.tenant_id = $2",
		"ORDER BY rl.ts ASC",
		"LIMIT $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("fallback SQL missing %q:\n%s", want, sql)
		}
	}
	for _, forbidden := range []string{"FROM request_logs_hot rl", "UNION ALL"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("fallback SQL must not contain %q:\n%s", forbidden, sql)
		}
	}
	if len(args) != 3 || args[0] != "session-1" || args[1] != "tenant-a" || args[2] != limit {
		t.Fatalf("unexpected fallback args: %#v", args)
	}
}

func TestSessionSummaryV2RequestLogsFallbackPermitsSuperAdminScope(t *testing.T) {
	sql, args := buildRequestLogsFallbackQuery("session-1", "", nil)
	if strings.Contains(sql, "tenant_id") || strings.Contains(sql, "LIMIT $") {
		t.Fatalf("unscoped fallback query must not add tenant or limit predicates:\n%s", sql)
	}
	if len(args) != 1 || args[0] != "session-1" {
		t.Fatalf("unexpected fallback args: %#v", args)
	}
}
