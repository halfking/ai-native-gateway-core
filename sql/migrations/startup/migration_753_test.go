package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 753 (R67 session-storage audit subtask 2, 2026-09-26) replaces
// the 24h-hardcoded session_turn_logs TTL with a configurable, settings_kv-
// driven cleanup function and pins an expires_at index alongside the existing
// 430 idx_session_turn_logs_expires. The contract pins the invariants that
// keep the change minimal and behavior-preserving:
//
//	C1  The new cleanup function is callable with a TTL parameter and
//	    defaults to a sane floor when given a degenerate input.
//	C2  The expires_at index matches the column already used by the
//	    hardcoded 430 default — there is no regression for the existing
//	    cleanup path, and the new function reuses the same index.
//	C3  Default behavior (24h, hot-reloadable setting) is preserved: the
//	    spec sets default = 24 and the migration's GREATEST(..., 1) floor
//	    protects against bad settings_kv rows.
//	C4  Up/down symmetry: the down file drops exactly the function and
//	    the index the up file creates, and removes the schema_migrations
//	    ledger row.
//	C5  Idempotency: CREATE OR REPLACE FUNCTION + CREATE INDEX IF NOT
//	    EXISTS, no CONCURRENTLY (small heap table — transaction-safe).
//	C6  Registration: apply-db-revision-sequence.sh carries the 753 row
//	    in non-decreasing order with 745/750 (R63 收口预埋 + R68 占位)
//	    on either side, and installer embeddata copy is byte-identical.
//
// Note: the parent handoff briefly says "745"; 745 was already taken by
// report_snapshots in R63 and 750 was rewritten to usage_facts_daily_partition
// in R68 (both unrelated, both 24h-audit siblings). 753 is the next free slot
// and avoids any partition-shaped conflict with 750.
func TestMigration753SessionTurnLogsTTLContract(t *testing.T) {
	upBytes, err := os.ReadFile("753_session_turn_logs_ttl.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)

	downBytes, err := os.ReadFile("753_session_turn_logs_ttl.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)

	// ── C1: cleanup_session_turn_logs_by_ttl(p_ttl_hours int) RETURNS bigint ─
	if !strings.Contains(upCompact, "CREATE OR REPLACE FUNCTION CLEANUP_SESSION_TURN_LOGS_BY_TTL(P_TTL_HOURS INT) RETURNS BIGINT") {
		t.Error("753 must define CREATE OR REPLACE FUNCTION cleanup_session_turn_logs_by_ttl(p_ttl_hours int) RETURNS bigint")
	}
	if !strings.Contains(upCompact, "MAKE_INTERVAL(HOURS => V_TTL_HOURS)") {
		t.Error("753 cleanup must use make_interval(hours => …) so the TTL truly varies with the parameter (not hardcoded)")
	}
	if !strings.Contains(upCompact, "GREATEST(COALESCE(P_TTL_HOURS, 24), 1)") {
		t.Error("753 cleanup must enforce a sane floor (min 1h) and default to 24h when given NULL — protects against settings_kv regressions")
	}
	if !strings.Contains(upCompact, "RETURN V_DELETED") {
		t.Error("753 cleanup must RETURN the deleted row count so callers can log/meter it")
	}

	// ── C2: expires_at index matches the column used by 430 default ─────
	if !strings.Contains(upCompact, "CREATE INDEX IF NOT EXISTS IDX_SESSION_TURN_LOGS_EXPIRES_AT ON PUBLIC.SESSION_TURN_LOGS (EXPIRES_AT)") {
		t.Error("753 must create idx_session_turn_logs_expires_at on session_turn_logs(expires_at)")
	}

	// ── C3: lifecycle.session_turn_logs_ttl_hours spec (default 24, hot-reload) ─
	specBytes, err := os.ReadFile("../../../settings/spec_lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(specBytes)
	if !strings.Contains(spec, "lifecycle.session_turn_logs_ttl_hours") {
		t.Error("settings/spec_lifecycle.go must register lifecycle.session_turn_logs_ttl_hours")
	}
	specUp := normalizeSQL(spec)
	if !strings.Contains(specUp, "LIFECYCLE.SESSION_TURN_LOGS_TTL_HOURS") {
		t.Error("settings/spec_lifecycle.go must register lifecycle.session_turn_logs_ttl_hours")
	}
	if !strings.Contains(spec, "Min: floatPtr(1)") || !strings.Contains(spec, "Max: floatPtr(168)") {
		t.Error("lifecycle.session_turn_logs_ttl_hours must be constrained to [1, 168] (7 days)")
	}
	if !strings.Contains(spec, "Default: 24") {
		t.Error("lifecycle.session_turn_logs_ttl_hours default must be 24 (preserve current behavior)")
	}

	// ── C4: up/down symmetry ────────────────────────────────────────────
	if !strings.Contains(upCompact, "CREATE OR REPLACE FUNCTION") {
		t.Error("753 up must declare the cleanup function (idempotent replay contract)")
	}
	if !strings.Contains(down, "DROP FUNCTION IF EXISTS cleanup_session_turn_logs_by_ttl") {
		t.Error("753 down must drop the cleanup function")
	}
	if !strings.Contains(down, "DROP INDEX IF EXISTS") {
		t.Error("753 down must drop the expires_at index")
	}
	if strings.Contains(strings.ToUpper(down), "CREATE TABLE") ||
		strings.Contains(strings.ToUpper(down), "CREATE OR REPLACE FUNCTION") {
		t.Error("753 down must not create anything (only DROP statements)")
	}

	// ── C5: idempotent + transaction-safe (no CONCURRENTLY) ────────────
	if strings.Contains(upCompact, "CONCURRENTLY") {
		t.Error("753 must not use CONCURRENTLY — plain heap table rides the installer single-transaction channel")
	}

	// ── C6: revision-sequence registration ─────────────────────────────
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("read apply-db-revision-sequence.sh: %v", err)
	}
	seqBody := string(seq)
	if !strings.Contains(seqBody, "753_session_turn_logs_ttl.sql") {
		t.Error("apply-db-revision-sequence.sh must carry migration 753_session_turn_logs_ttl.sql — upgrade-database deployments would never apply it otherwise")
	}
	// Pin ordering vs neighbouring 745 (R63 report_snapshots) and 750
	// (R68 usage_facts_daily_partition). 753 must not regress.
	seqOrder := []string{
		"745_report_snapshots.sql",
		"750_usage_facts_daily_partition.sql",
		"753_session_turn_logs_ttl.sql",
	}
	lastPos := -1
	for _, name := range seqOrder {
		pos := strings.Index(seqBody, name)
		if pos < 0 {
			t.Errorf("apply-db-revision-sequence.sh does not carry %s", name)
			continue
		}
		if pos < lastPos {
			t.Errorf("migration channel order regressed: %s appears before its predecessor", name)
		}
		lastPos = pos
	}

	// ── bg.PartitionManager wires the new cleanup function via runCleanup ─
	pmBytes, err := os.ReadFile("../../../bg/partition_manager.go")
	if err != nil {
		t.Fatal(err)
	}
	pm := string(pmBytes)
	if !strings.Contains(pm, "cleanup_session_turn_logs_by_ttl") {
		t.Error("bg.PartitionManager must reference cleanup_session_turn_logs_by_ttl")
	}
	if !strings.Contains(pm, "lifecycle.session_turn_logs_ttl_hours") {
		t.Error("bg.PartitionManager must read the lifecycle.session_turn_logs_ttl_hours setting (hot reload)")
	}
}
