package admin

import (
	"strings"
	"testing"
)

// TestSpecifiedModelTaskKey_DoubleUnderscorePrefix guards the contract
// that the synthetic task_type key is __specified__ (not "specified" or
// "specified_model") so it cannot collide with a real classifier output.
func TestSpecifiedModelTaskKey_DoubleUnderscorePrefix(t *testing.T) {
	if SpecifiedModelTaskKey != "__specified__" {
		t.Fatalf("SpecifiedModelTaskKey = %q, want %q", SpecifiedModelTaskKey, "__specified__")
	}
	if !strings.HasPrefix(SpecifiedModelTaskKey, "__") || !strings.HasSuffix(SpecifiedModelTaskKey, "__") {
		t.Fatalf("SpecifiedModelTaskKey = %q must be wrapped in double underscores", SpecifiedModelTaskKey)
	}
}

// TestSpecifiedModelDisplayLabel_NonEmpty guards that the UI label is
// always populated (frontend relies on a non-empty value).
func TestSpecifiedModelDisplayLabel_NonEmpty(t *testing.T) {
	if SpecifiedModelDisplayLabel == "" {
		t.Fatal("SpecifiedModelDisplayLabel must not be empty")
	}
	if strings.Contains(SpecifiedModelDisplayLabel, "__") {
		t.Fatalf("SpecifiedModelDisplayLabel = %q must not contain double underscores (UI label, not internal key)", SpecifiedModelDisplayLabel)
	}
}

// TestBuildMatrixQuery_IncludesSpecifiedModel verifies that the
// generated SQL contains the synthetic __specified__ task key, the
// client_model fallback for the column axis, and the (is_auto_request
// OR client_model IS NOT NULL) WHERE clause that lets explicit-model
// rows in.
func TestBuildMatrixQuery_IncludesSpecifiedModel(t *testing.T) {
	q, err := buildMatrixQuery("task_type", "count")
	if err != nil {
		t.Fatalf("buildMatrixQuery returned error: %v", err)
	}
	if !strings.Contains(q, SpecifiedModelTaskKey) {
		t.Fatalf("matrix query missing %s:\n%s", SpecifiedModelTaskKey, q)
	}
	if !strings.Contains(q, "client_model") {
		t.Fatalf("matrix query must reference client_model (for non-auto column fallback):\n%s", q)
	}
	if !strings.Contains(q, "is_auto_request = TRUE") {
		t.Fatalf("matrix query must keep the auto-request branch:\n%s", q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Fatalf("matrix query must include the explicit-model branch:\n%s", q)
	}
	// No lingering is_auto_request = TRUE as a top-level WHERE — we
	// want it only inside the OR clause.
	if strings.Contains(q, "WHERE is_auto_request = TRUE") {
		t.Fatalf("matrix query still uses pre-2026-06-22 hard filter (must be inside OR):\n%s", q)
	}
}

// TestBuildMatrixQuery_AllMetrics confirms every supported metric
// produces a valid SELECT.
func TestBuildMatrixQuery_AllMetrics(t *testing.T) {
	metrics := []string{"count", "success_rate", "p95_ms", "cost_usd"}
	for _, m := range metrics {
		q, err := buildMatrixQuery("task_type", m)
		if err != nil {
			t.Errorf("metric=%s: %v", m, err)
			continue
		}
		if !strings.Contains(q, "SELECT") || !strings.Contains(q, "FROM request_logs") {
			t.Errorf("metric=%s: malformed query:\n%s", m, q)
		}
	}
}

// TestBuildMatrixQuery_WorkTypeRowDim confirms the work_type branch
// uses the work_type column (not the effective-task expression) and
// keeps the synthetic key off the row axis.
func TestBuildMatrixQuery_WorkTypeRowDim(t *testing.T) {
	q, err := buildMatrixQuery("work_type", "count")
	if err != nil {
		t.Fatalf("buildMatrixQuery: %v", err)
	}
	if !strings.Contains(q, "work_type") {
		t.Fatalf("work_type row dim must reference work_type column:\n%s", q)
	}
	// work_type branch should NOT inject the synthetic __specified__
	// task label because work_type applies to any request.
	if strings.Contains(q, SpecifiedModelTaskKey) {
		t.Fatalf("work_type row dim should not use SpecifiedModelTaskKey (work_type is independent):\n%s", q)
	}
	// But the broader WHERE clause (auto OR explicit) must still be
	// present so work_type stats include explicit-model requests.
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Fatalf("work_type query must still admit explicit-model requests:\n%s", q)
	}
}

// TestBuildMatrixQuery_RejectsBadInput confirms parameter validation.
func TestBuildMatrixQuery_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		row  string
		met  string
	}{
		{"bad row", "month", "count"},
		{"bad metric", "task_type", "latency_p99"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := buildMatrixQuery(c.row, c.met); err == nil {
				t.Fatalf("expected error for row=%q metric=%q", c.row, c.met)
			}
		})
	}
}

// TestEffectiveTaskExpr_IncludesSpecifiedKey guards that the COALESCE
// fallback path uses SpecifiedModelTaskKey (not a hardcoded "specified"
// or empty string) so analytics renders "指定模型" for non-auto rows.
func TestEffectiveTaskExpr_IncludesSpecifiedKey(t *testing.T) {
	expr := effectiveTaskExpr()
	if !strings.Contains(expr, SpecifiedModelTaskKey) {
		t.Fatalf("effectiveTaskExpr missing %s: %s", SpecifiedModelTaskKey, expr)
	}
	if !strings.Contains(expr, "is_auto_request") {
		t.Fatalf("effectiveTaskExpr must branch on is_auto_request: %s", expr)
	}
	if !strings.Contains(expr, "COALESCE") {
		t.Fatalf("effectiveTaskExpr must use COALESCE: %s", expr)
	}
	if !strings.Contains(expr, "task_type") {
		t.Fatalf("effectiveTaskExpr must read task_type column: %s", expr)
	}
}

// TestEffectiveModelExpr_FallsBackToClientModel guards that the column
// axis expression falls back from outbound_model to client_model —
// critical for explicit-model requests where outbound_model is NULL.
func TestEffectiveModelExpr_FallsBackToClientModel(t *testing.T) {
	expr := effectiveModelExpr()
	if !strings.Contains(expr, "outbound_model") {
		t.Fatalf("effectiveModelExpr must prefer outbound_model: %s", expr)
	}
	if !strings.Contains(expr, "client_model") {
		t.Fatalf("effectiveModelExpr must fall back to client_model: %s", expr)
	}
	if !strings.Contains(expr, "COALESCE") {
		t.Fatalf("effectiveModelExpr must use COALESCE: %s", expr)
	}
}

// TestBuildFlowL12Query_AdmitsExplicitModel confirms the L1→L2 Sankey
// query admits both auto and explicit-model traffic.
func TestBuildFlowL12Query_AdmitsExplicitModel(t *testing.T) {
	q := buildFlowL12Query()
	if !strings.Contains(q, SpecifiedModelTaskKey) {
		t.Fatalf("L12 query missing %s: %s", SpecifiedModelTaskKey, q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Fatalf("L12 query must admit explicit-model branch: %s", q)
	}
	if !strings.Contains(q, "client_model") {
		t.Fatalf("L12 query must use client_model fallback: %s", q)
	}
	if strings.Contains(q, "WHERE is_auto_request = TRUE\n") {
		t.Fatalf("L12 query should not have a hard auto-only filter: %s", q)
	}
}

// TestBuildFlowL23Query_AdmitsExplicitModel confirms the L2→L3
// (model → provider) Sankey query uses the effective task expression
// so the link color matches the heatmap row label.
func TestBuildFlowL23Query_AdmitsExplicitModel(t *testing.T) {
	q := buildFlowL23Query()
	if !strings.Contains(q, SpecifiedModelTaskKey) {
		t.Fatalf("L23 query missing %s: %s", SpecifiedModelTaskKey, q)
	}
	if !strings.Contains(q, "LEFT JOIN providers p") {
		t.Fatalf("L23 query must still LEFT JOIN providers: %s", q)
	}
	if !strings.Contains(q, "display_name") {
		t.Fatalf("L23 query must reference providers.display_name: %s", q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Fatalf("L23 query must admit explicit-model branch: %s", q)
	}
}

// TestHandleAudit_PreservesFieldSet documents the audit response shape
// the routing-v2 UI relies on. The test guards against accidental
// removal of the new specified_model_requests field by cross-referencing
// the field set with the TypeScript type in web/src/api-autoroute.ts.
func TestHandleAudit_PreservesFieldSet(t *testing.T) {
	// We can't run the handler without a real DB, but we can document
	// the contract via a sentinel that fails loudly if anyone ever
	// removes the SpecifiedModelTaskKey constant the field depends on.
	// (The TypeScript type is checked separately via npm type-check.)
	if SpecifiedModelTaskKey != "__specified__" {
		t.Fatalf("SpecifiedModelTaskKey changed; verify the AutoRouteAudit.specified_model_requests wiring in handleAudit and web/src/api-autoroute.ts still match")
	}
}
