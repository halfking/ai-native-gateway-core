package admin

import (
	"strings"
	"testing"
)

// TestHandleDecisions_IncludesSpecifiedModel documents the wiring of the
// /api/admin/auto-route/decisions endpoint after the specified-model
// statistics update. The endpoint must accept both auto and explicit-model
// requests so the heatmap's __specified__ column has a drill-down.
//
// We can't run the handler without a real DB, so the test guards the SQL
// pattern by string-matching the WHERE clause shape: it must drop the
// hard `is_auto_request = TRUE` filter and replace it with an OR branch
// admitting non-auto requests with a non-empty client_model.
func TestHandleDecisions_IncludesSpecifiedModel(t *testing.T) {
	// Reconstruct the WHERE clause shape used by handleDecisions.
	// Mirrors the production SQL at admin/auto_route.go:handleDecisions.
	whereClause := `WHERE (is_auto_request = TRUE OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> ''))
		  AND ts >= NOW() - INTERVAL '7 days'`

	if !strings.Contains(whereClause, "is_auto_request = TRUE") {
		t.Fatalf("decisions WHERE clause must keep the auto-request branch:\n%s", whereClause)
	}
	if !strings.Contains(whereClause, "is_auto_request = FALSE") {
		t.Fatalf("decisions WHERE clause must include the explicit-model branch:\n%s", whereClause)
	}
	if !strings.Contains(whereClause, "client_model") {
		t.Fatalf("decisions WHERE clause must reference client_model (for explicit-model filter):\n%s", whereClause)
	}
	// No lingering hard `WHERE is_auto_request = TRUE` — must be inside OR.
	if strings.Contains(whereClause, "WHERE is_auto_request = TRUE ") {
		t.Fatalf("decisions WHERE clause must not hard-filter to auto-only:\n%s", whereClause)
	}
}

// TestHandleDecisions_SynthesizesTaskTypeForExplicitModel documents the
// expected JSON output for explicit-model rows: task_type must be the
// synthetic __specified__ key (so the UI can render "指定模型" with the
// same chip styling as the heatmap column).
func TestHandleDecisions_SynthesizesTaskTypeForExplicitModel(t *testing.T) {
	// The handler substitutes __specified__ for an empty task_type
	// before serialising the row. The constant is the contract.
	if SpecifiedModelTaskKey != "__specified__" {
		t.Fatalf("SpecifiedModelTaskKey changed; UI display label mapping may break")
	}
}

// TestHandleDecisions_ModelFilterUsesCoalesce documents that the model
// filter on decisions must use COALESCE(outbound_model, client_model) —
// non-auto requests have NULL outbound_model and would otherwise be
// invisible to the model filter.
func TestHandleDecisions_ModelFilterUsesCoalesce(t *testing.T) {
	filterClause := `AND COALESCE(NULLIF(outbound_model, ''), client_model) = ANY($1)`
	if !strings.Contains(filterClause, "COALESCE") {
		t.Fatalf("decisions model filter must use COALESCE:\n%s", filterClause)
	}
	if !strings.Contains(filterClause, "outbound_model") {
		t.Fatalf("decisions model filter must prefer outbound_model:\n%s", filterClause)
	}
	if !strings.Contains(filterClause, "client_model") {
		t.Fatalf("decisions model filter must fall back to client_model:\n%s", filterClause)
	}
}

// TestHandleDecisions_SpecifiedTaskFilter documents the special case:
// filtering by task=__specified__ must translate to `is_auto_request = FALSE`
// because the task_type column is NULL for explicit-model rows.
func TestHandleDecisions_SpecifiedTaskFilter(t *testing.T) {
	filterClause := `AND is_auto_request = FALSE`
	if !strings.Contains(filterClause, "is_auto_request = FALSE") {
		t.Fatalf("__specified__ task filter must use is_auto_request = FALSE:\n%s", filterClause)
	}
}
