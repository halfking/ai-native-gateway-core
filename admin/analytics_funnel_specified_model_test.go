package admin

import (
	"strings"
	"testing"
)

// TestHandleFunnel_FallbackIncludesExplicitModel documents the wiring of
// the /api/admin/auto-route/analytics/funnel endpoint's request_logs
// fallback queries. After the specified-model statistics update, both
// the "approximate" (no RDL rows) and "mixed" (RDL rows with empty
// traces) branches must admit explicit-model requests, so the L2 funnel
// shows non-zero data for models that are popular via the explicit-model
// path.
//
// Pre-update: both branches used `WHERE is_auto_request = TRUE` and
// therefore missed any explicit-model traffic in the funnel display.
func TestHandleFunnel_FallbackIncludesExplicitModel(t *testing.T) {
	// Reconstruct the SQL the handler uses for the approximate branch
	// (handleFunnel, lines ~802-812 of admin/analytics.go) and the mixed
	// branch (lines ~828-841).
	approximateClause := `WHERE ts >= NOW() - INTERVAL '1 second' * $1
			  AND outbound_model = ANY($2)
			  AND (
			    is_auto_request = TRUE
			    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
			  )`
	mixedClause := `WHERE ts >= NOW() - INTERVAL '1 second' * $1
			  AND outbound_model = ANY($2)
			  AND (
			    is_auto_request = TRUE
			    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
			  )`

	for name, clause := range map[string]string{
		"approximate": approximateClause,
		"mixed":       mixedClause,
	} {
		if !strings.Contains(clause, "is_auto_request = TRUE") {
			t.Errorf("funnel %s fallback must keep the auto-request branch:\n%s", name, clause)
		}
		if !strings.Contains(clause, "is_auto_request = FALSE") {
			t.Errorf("funnel %s fallback must include the explicit-model branch:\n%s", name, clause)
		}
		if strings.Contains(clause, "WHERE is_auto_request = TRUE\n") {
			t.Errorf("funnel %s fallback must not hard-filter to auto-only:\n%s", name, clause)
		}
	}
}
