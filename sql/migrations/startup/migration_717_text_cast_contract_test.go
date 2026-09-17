package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration717ShapePins pins the structure of migration 717 as of the
// R37 SQL-focused round's live-DB-hardened v3 ("视图依赖捕获重建"):
//
//  1. View dependency capture/rebuild: request_logs_hot columns are depended
//     on by wrapper views (680 chain) and routing_analytics_source; a bare
//     ALTER COLUMN TYPE on any view-projected column dies with 0A000 — even
//     a type-identical no-op. The file must capture the live view bodies
//     (pg_get_viewdef) BEFORE dropping them, and recreate from the captured
//     bodies AFTER the alters.
//  2. Per-column information_schema type guards: only drifted columns are
//     altered, so on aligned (fresh-install) baselines the file is a no-op —
//     a bare unguarded whole-batch ALTER is the R36 Rev-1/Rev-2 failure mode
//     (42883 on bigint customer_id, flagged by e0f94a799's commit message).
//  3. The customer_id regex clause may only run under a text-family guard
//     and must read the column through ::text (bare `customer_id ~` is 42883
//     on the bigint column 574 + the baseline already guarantee).
func TestMigration717ShapePins(t *testing.T) {
	body, err := os.ReadFile("717_request_logs_hot_column_alignment.sql")
	if err != nil {
		t.Fatalf("read 717: %v", err)
	}
	sql := string(body)

	mustContain := []string{
		"pg_get_viewdef", // capture live view bodies before dropping
		"DROP VIEW IF EXISTS public.request_logs_with_current_month CASCADE",
		"DROP VIEW IF EXISTS public.routing_analytics_source CASCADE",
		"CREATE VIEW public.request_logs_with_current_month AS ' || v_final", // rebuild from captured body
		"information_schema.columns",                                         // per-column drifted-only guards
		"customer_id::text ~ '^[0-9]+$'",                                     // bigint-safe regex operand
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("717 lost a live-validated invariant: %q not found", want)
		}
	}

	// Bare text-operator on customer_id is 42883 on bigint (the original
	// fresh-install blocker); the guarded clause must keep the ::text operand.
	for _, banned := range []string{
		"WHEN customer_id ~",
		"THEN customer_id::bigint",
	} {
		if strings.Contains(sql, banned) {
			t.Errorf("717 regressed to the bare customer_id operator: %q — 42883 on the bigint column 574 + the baseline guarantee", banned)
		}
	}

	// Every guarded column must be wrapped in a type check so aligned
	// installs skip (count the guard queries: ≥10 guarded columns).
	if got := strings.Count(sql, "SELECT data_type INTO v_type"); got < 10 {
		t.Errorf("717 has %d per-column type guards, want ≥10 (agent_name/agent_type/api_key_fingerprint/task_id/customer_id/content_safety_score/dlp_violations/protocol_conversion/ir_extensions/sanitizer_mutations)", got)
	}

	// Known residue, kept visible: the jsonb pre-guard char class [0-9tfn-]
	// admits words like "not-json" and then fails the ::jsonb parse it guards
	// (22P02) — tolerated by the live-validated v3 because the writer only
	// emits valid JSON; registered in the R37 round doc §四 with the patch
	// shape (explicit true|false|null alternation) if it ever bites.
	if !strings.Contains(sql, "0-9tfn-") {
		t.Errorf("717 jsonb pre-guard changed: update the R37 round doc §四 residue note before removing this check")
	}
}
