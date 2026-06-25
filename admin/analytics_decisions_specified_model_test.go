package admin

import (
	"strings"
	"testing"
)

// TestBuildDecisionsQuery_DefaultAdmitsSpecifiedModel checks the base
// query (no caller filters) admits both auto and explicit-model
// requests — the precondition for the heatmap's __specified__ column.
func TestBuildDecisionsQuery_DefaultAdmitsSpecifiedModel(t *testing.T) {
	q, _, err := buildDecisionsQuery(decisionsFilters{Limit: 10})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	if !strings.Contains(q, "is_auto_request = TRUE") {
		t.Errorf("base query must keep the auto-request branch:\n%s", q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE") {
		t.Errorf("base query must include the explicit-model branch:\n%s", q)
	}
	if !strings.Contains(q, "client_model") {
		t.Errorf("base query must reference client_model (for explicit-model filter):\n%s", q)
	}
	// No hard is_auto_request = TRUE as a top-level WHERE — must be inside OR.
	if strings.Contains(q, "WHERE is_auto_request = TRUE ") {
		t.Errorf("base query must not hard-filter to auto-only:\n%s", q)
	}
}

// TestBuildDecisionsQuery_SpecifiedTaskFilter pins the contract that
// filtering by task=__specified__ must translate to is_auto_request = FALSE
// (because the task_type column is NULL for explicit-model rows).
func TestBuildDecisionsQuery_SpecifiedTaskFilter(t *testing.T) {
	q, args, err := buildDecisionsQuery(decisionsFilters{
		Task:  SpecifiedModelTaskKey,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	if !strings.Contains(q, " AND is_auto_request = FALSE") {
		t.Errorf("task=__specified__ must emit is_auto_request = FALSE:\n%s", q)
	}
	// No $N placeholder should be bound for the __specified__ case.
	for _, a := range args {
		if s, ok := a.(string); ok && s == SpecifiedModelTaskKey {
			t.Errorf("__specified__ must NOT be bound as a parameter (would scan NULL and miss):\n%s", q)
		}
	}
}

// TestBuildDecisionsQuery_RealTaskFilter checks the normal task_type
// path: the value is bound and the SQL references $N.
func TestBuildDecisionsQuery_RealTaskFilter(t *testing.T) {
	q, args, err := buildDecisionsQuery(decisionsFilters{
		Task:  "code",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	if !strings.Contains(q, " AND task_type = $") {
		t.Errorf("real task filter must bind as a parameter:\n%s", q)
	}
	if strings.Contains(q, " AND is_auto_request = FALSE") {
		t.Errorf("real task filter must NOT inject is_auto_request = FALSE:\n%s", q)
	}
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == "code" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'code' in args, got %v", args)
	}
}

// TestBuildDecisionsQuery_ModelFilterSplit covers the OR-of-ANY model
// filter that lets a single canonical name match both auto (outbound_model)
// and specified-model (client_model) requests.
func TestBuildDecisionsQuery_ModelFilterSplit(t *testing.T) {
	q, args, err := buildDecisionsQuery(decisionsFilters{
		Model: []string{"gpt-4o"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	// Splits the predicate so PG can use the partial index on
	// (outbound_model) for the auto path and
	// idx_request_logs_explicit_model for the specified-model path.
	if !strings.Contains(q, "outbound_model = ANY($") {
		t.Errorf("model filter must include outbound_model = ANY:\n%s", q)
	}
	if !strings.Contains(q, "is_auto_request = FALSE AND client_model = ANY($") {
		t.Errorf("model filter must split the specified-model branch:\n%s", q)
	}
	// Both ANY clauses must point at the same parameter.
	if strings.Count(q, "outbound_model = ANY($") != 1 {
		t.Errorf("model filter must reuse the same $N for both branches (one param binding), got:\n%s", q)
	}
	// Arg must be bound exactly once.
	count := 0
	for _, a := range args {
		if sl, ok := a.([]string); ok && len(sl) == 1 && sl[0] == "gpt-4o" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one []string{gpt-4o} binding, got %d (args=%v)", count, args)
	}
}

// TestBuildDecisionsQuery_ProfileFilterAdmitsSpecified documents the
// OR-clause that lets profile=foo also return non-auto rows. Without
// this, callers filtering by profile would silently drop the entire
// specified-model population (whose auto_profile is NULL).
func TestBuildDecisionsQuery_ProfileFilterAdmitsSpecified(t *testing.T) {
	q, _, err := buildDecisionsQuery(decisionsFilters{
		Profile: "smart",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	if !strings.Contains(q, " AND (auto_profile = $") {
		t.Errorf("profile filter must bind auto_profile:\n%s", q)
	}
	if !strings.Contains(q, " OR is_auto_request = FALSE)") {
		t.Errorf("profile filter must OR in is_auto_request = FALSE to admit specified-model rows (whose auto_profile is NULL):\n%s", q)
	}
}

// TestBuildDecisionsQuery_AllFiltersComposed confirms that all four
// optional filters compose into one query with the correct placeholder
// ordering (no off-by-one regressions).
func TestBuildDecisionsQuery_AllFiltersComposed(t *testing.T) {
	q, args, err := buildDecisionsQuery(decisionsFilters{
		Task:     "code",
		WorkType: "wt1",
		Model:    []string{"gpt-4o", "gpt-4o-mini"},
		Profile:  "smart",
		Limit:    25,
	})
	if err != nil {
		t.Fatalf("buildDecisionsQuery: %v", err)
	}
	// Expected: task=$1, work_type=$2, model=$3 (used twice in OR), profile=$4, limit=$5.
	want := []string{
		"task_type = $1",
		"work_type = $2",
		"outbound_model = ANY($3)",
		"client_model = ANY($3)",
		"auto_profile = $4",
		"LIMIT $5",
	}
	for _, frag := range want {
		if !strings.Contains(q, frag) {
			t.Errorf("expected %q in query, got:\n%s", frag, q)
		}
	}
	if len(args) != 5 {
		t.Errorf("expected 5 bound args, got %d: %v", len(args), args)
	}
}

// TestBuildDecisionsQuery_RejectsBadLimit documents input validation.
func TestBuildDecisionsQuery_RejectsBadLimit(t *testing.T) {
	cases := []int{0, -1, 501, 10000}
	for _, lim := range cases {
		if _, _, err := buildDecisionsQuery(decisionsFilters{Limit: lim}); err == nil {
			t.Errorf("expected error for limit=%d", lim)
		}
	}
}
