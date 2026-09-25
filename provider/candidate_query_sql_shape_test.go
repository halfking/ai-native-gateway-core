package provider

import (
	"strings"
	"testing"
)

// TestCandidateQuerySQL_Shape pins the 2026-09-25 audit round 8 (D10) rewrite
// of the candidate-build SQL: the model-match half must be materialized
// FIRST (WITH matched AS MATERIALIZED) so the planner cannot re-flatten it
// into the old "join the full binding set, then filter" shape, and the
// dead sibling gate must not come back (it const-folded to TRUE and only
// cost parse+plan time).
//
// Background (252 real-db EXPLAIN, 纪律⑳): model_offers and
// v_routable_credential_models are both views over credential_model_bindings
// × provider_models. The pre-rewrite shape joined the full 1,831-row set
// against itself before the $1 match predicate pruned it (exec 220-390ms,
// planning 195-260ms, production mean 3.3s under load). The rewrite keeps
// the same result set (equivalence verified on 252 across 8 model/modality
// combos incl. empty-set and vision paths) and runs 61-62ms.
func TestCandidateQuerySQL_Shape(t *testing.T) {
	q := candidateQuerySQL()

	if !strings.Contains(q, "WITH matched AS MATERIALIZED (") {
		t.Fatal("model-match half must be a MATERIALIZED CTE named 'matched' — without MATERIALIZED the planner re-flattens the join and the D10 fix is void")
	}
	// The match predicate (5 arms) must live INSIDE the CTE, not outside.
	cteEnd := strings.Index(q, ")\n\t\t)\nSELECT")
	if cteEnd < 0 {
		t.Fatal("CTE body terminator not found — expected the matched CTE to close before the outer SELECT")
	}
	cte := q[:cteEnd]
	for _, arm := range []string{
		"mo.canonical_raw_name = $1",
		"mo.standardized_name = $1",
		"mnm.standardized_name = $1",
		"lower(mc.canonical_name) = $1",
	} {
		if !strings.Contains(cte, arm) {
			t.Errorf("match arm %q must be inside the matched CTE", arm)
		}
	}
	if !strings.Contains(q, "FROM matched mo") {
		t.Fatal("outer query must read FROM matched mo")
	}
	// P4 endpoint projection belongs to the post-match provider rows. It must
	// not expand the materialized model-match CTE or duplicate candidates.
	outer := q[strings.Index(q, "FROM matched mo"):]
	if !strings.Contains(q, "pep_lateral.endpoints AS native_endpoints") {
		t.Error("P4 native endpoint projection missing from candidate SELECT")
	}
	for _, fragment := range []string{
		"FROM provider_endpoint_protocols pep",
		"WHERE pep.provider_id = p.id AND pep.enabled = TRUE",
		") pep_lateral ON TRUE",
	} {
		if !strings.Contains(outer, fragment) {
			t.Errorf("P4 endpoint projection missing from outer candidate query: %q", fragment)
		}
	}
	if strings.Contains(stripSQLComments(q[:strings.Index(q, "FROM matched mo")]), "provider_endpoint_protocols") {
		t.Error("endpoint aggregation must not be placed inside the matched CTE")
	}
	// The dead sibling gate (AND FALSE since 2026-08-27) must stay deleted:
	// it const-folded to TRUE so it never executed, but its ~70 lines cost
	// parse+plan time on every call (SimpleProtocol = no plan cache).
	// Check with SQL comment lines stripped — the audit note left in the text
	// mentions 'AND FALSE' in prose.
	executable := stripSQLComments(q)
	if strings.Contains(executable, "AND FALSE") || strings.Contains(executable, "mo_sibling") {
		t.Fatal("dead sibling gate leaked back into candidateQuerySQL — it was removed in round 8 (D10); see git history if the fail-open gate is ever needed again")
	}
	// v_routable join and the probe guard must survive the rewrite.
	for _, must := range []string{
		"LEFT JOIN v_routable_credential_models v",
		"broken_confirmed",
		"recent_success_rate(c.id, mo.raw_model_name, 50)",
		"v.is_routable = TRUE",
	} {
		if !strings.Contains(q, must) {
			t.Errorf("rewrite dropped required fragment %q", must)
		}
	}
	// context_window must resolve through the CTE-carried canonical columns.
	if strings.Contains(q, "mc.context_window") && strings.Contains(q, "FROM matched mo") {
		// outer scope no longer joins models_canonical; only the CTE may reference mc.*
		outer := q[strings.Index(q, "FROM matched mo"):]
		if strings.Contains(outer, "mc.context_window") || strings.Contains(outer, "mc.id") {
			t.Error("outer query still references mc.* — the canonical columns must be carried via the matched CTE (_mc_id/_mc_cw_override/_mc_cw)")
		}
	}
}

// stripSQLComments removes full-line and trailing "--" comments so shape
// assertions test executable SQL only.
func stripSQLComments(sqlText string) string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
