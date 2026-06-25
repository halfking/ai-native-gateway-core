package admin

import (
	"strings"
	"testing"
)

// TestFunnelFallbackQuery_AdmitsSpecifiedModel guards the SQL contract
// shared by the approximate and mixed branches of handleFunnel.
//
// Pre-update: both branches used `WHERE is_auto_request = TRUE` and
// therefore missed any explicit-model traffic in the funnel display.
// After the fix, the helper funnelRequestLogsFallbackQuery must include
// the (is_auto_request = FALSE AND client_model IS NOT NULL AND ...)
// branch so the L2 funnel shows non-zero data for models reached via
// the explicit-model path.
func TestFunnelFallbackQuery_AdmitsSpecifiedModel(t *testing.T) {
	q := funnelRequestLogsFallbackQuery()

	if !strings.Contains(q, "is_auto_request = TRUE") {
		t.Errorf("funnel fallback must keep the auto-request branch:\n%s", q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Errorf("funnel fallback must include the explicit-model branch:\n%s", q)
	}
	if !strings.Contains(q, "client_model IS NOT NULL") {
		t.Errorf("funnel fallback must guard against NULL client_model (non-auto requests with no model set):\n%s", q)
	}
	// The previous bug was a hard `WHERE is_auto_request = TRUE\n` filter.
	if strings.Contains(q, "WHERE is_auto_request = TRUE\n") {
		t.Errorf("funnel fallback must not hard-filter to auto-only:\n%s", q)
	}
}

// TestFunnelFallbackQuery_StableShape pins the column list and FROM
// target so a refactor cannot silently drop the credential_id or
// success counts. The funnel UI depends on these specific column
// positions to match its Scan() call.
func TestFunnelFallbackQuery_StableShape(t *testing.T) {
	q := funnelRequestLogsFallbackQuery()
	wantFragments := []string{
		"COUNT(*)::int",
		"COUNT(*) FILTER (WHERE credential_id IS NOT NULL)::int",
		"COUNT(*) FILTER (WHERE success IS TRUE)::int",
		"FROM request_logs",
		"outbound_model = ANY($2)",
		"NOW() - $1::interval",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(q, frag) {
			t.Errorf("funnel fallback missing required fragment %q:\n%s", frag, q)
		}
	}
}
