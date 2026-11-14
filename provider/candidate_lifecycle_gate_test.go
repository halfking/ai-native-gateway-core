package provider

import (
	"strings"
	"testing"
)

// TestCandidateQuerySQL_ModelLifecycleGate pins the 2026-11-14 model-lifecycle
// gate added by migration 966: stale models (released_at older than
// model_lifecycle.stale_threshold, default 2 years) must be excluded from the
// routing candidate pool, while three cases stay INCLUDED.
//
// This is a shape gate. The behaviour gate (which rows actually survive) lives
// in TestModelLifecyclePredicate_RealDB; both are needed because a shape gate
// cannot see a NULL-propagation bug and a real-db gate is skipped when no DSN
// is configured.
//
// Background — the first cut of this gate shipped two defects that this test
// now guards against:
//
//  1. COALESCE was placed INSIDE the threshold subquery. When the settings row
//     is absent the subquery returns no row, the age comparison against NULL
//     evaluates to NULL, the OR chain is NULL, and the row is DROPPED. That
//     inverted the intended fail-open semantics and would silently empty the
//     pool on any host that had not applied the settings insert.
//  2. The date was read as value::text, which only parses because PostgreSQL
//     tolerates the surrounding quotes of a jsonb string. (value #>> '{}')
//     states the intent (unwrap the jsonb scalar) instead of relying on that.
func TestCandidateQuerySQL_ModelLifecycleGate(t *testing.T) {
	q := candidateQuerySQL()

	// Locate the gate by the threshold setting name in the SQL body, not in the
	// comment above it. Use the LAST occurrence: the comment mentions the name
	// first, the SQL references it after. Then back up to the opening AND ( of
	// the enclosing group.
	gateStart := strings.LastIndex(q, "model_lifecycle.stale_threshold")
	if gateStart < 0 {
		t.Fatal("lifecycle gate missing: candidate SQL does not reference model_lifecycle.stale_threshold")
	}
	open := strings.LastIndex(q[:gateStart], "AND (")
	if open < 0 {
		t.Fatal("could not find the 'AND (' that opens the lifecycle gate")
	}
	gateStart = open

	// The gate must live in the outer candidate query, not in the materialized
	// model-match CTE (the CTE is keyed on the model name only; aging is a
	// property of the canonical row and belongs with the other outer filters).
	outerMarker := strings.Index(q, "FROM matched mo")
	if outerMarker < 0 {
		t.Fatal("outer query marker 'FROM matched mo' not found")
	}
	if gateStart < outerMarker {
		t.Error("lifecycle gate must be in the outer candidate query, after 'FROM matched mo'")
	}

	// Scope the structural assertions to the gate region itself. The candidate
	// query legitimately contains "SELECT COALESCE(" elsewhere (the endpoint
	// projection aggregates with jsonb_agg), so a whole-query Contains check
	// would either false-positive or force a misleading negation.
	gateEnd := strings.Index(q[gateStart:], "ORDER BY")
	if gateEnd < 0 {
		t.Fatal("could not delimit the lifecycle gate region (no ORDER BY after it)")
	}
	gate := q[gateStart : gateStart+gateEnd]

	// --- released_at must be read from the CTE projection, never from `mc`:
	//     the outer query is `FROM matched mo`, where mc is out of scope. Using
	//     mc here fails PREPARE on the real schema (verified on 211).
	if strings.Contains(gate, "mc.released_at") {
		t.Error("gate must read mo._mc_released_at (CTE projection), not mc.released_at — " +
			"mc is only in scope inside the matched CTE and the outer query will fail PREPARE")
	}

	// --- The three inclusion arms must all be present. ---
	for _, arm := range []struct{ name, frag string }{
		{"manual priority bypass", "COALESCE(mo.manual_priority, 0) > 0"},
		{"unknown age fail-open", "mo._mc_released_at IS NULL"},
		{"age comparison", "age(now(), mo._mc_released_at)"},
	} {
		if !strings.Contains(gate, arm.frag) {
			t.Errorf("%s arm missing from lifecycle gate: %q", arm.name, arm.frag)
		}
	}

	// --- COALESCE must wrap the whole threshold expression (outer), not sit
	//     inside the subquery where it cannot rescue a missing row. ---
	if !strings.Contains(gate, "<= COALESCE(") {
		t.Error("threshold must be `age(...) <= COALESCE(<subquery>, '2 years')` — " +
			"an inner-only COALESCE leaves the comparison NULL when the settings row is absent, " +
			"which drops every dated model (inverted fail-open)")
	}
	if strings.Contains(gate, "SELECT COALESCE(") {
		t.Error("COALESCE must NOT be inside the threshold subquery — it cannot rescue a " +
			"no-row result; it must wrap the subquery result")
	}
	if !strings.Contains(gate, "'2 years'::interval") {
		t.Error("default threshold '2 years'::interval missing — a missing setting must fall back to the default")
	}

	// --- jsonb unwrap must be #>> '{}' (intent-revealing), not ::text (works
	//     only by quote tolerance). ---
	if !strings.Contains(gate, "(value #>> '{}')::interval") {
		t.Error("threshold must be read as (value #>> '{}')::interval — value::text only parses " +
			"by relying on PostgreSQL tolerating the jsonb string's quotes")
	}
	if strings.Contains(gate, "(value::text)::interval") {
		t.Error("value::text::interval is the quote-tolerant form this gate replaced; do not reintroduce it")
	}
}
