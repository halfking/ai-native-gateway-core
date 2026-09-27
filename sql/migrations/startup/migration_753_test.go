package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 753 (R67 session-storage audit subtask 2, 2026-09-26; semantics
// corrected by the 2026-09-27 critical audit; batched by the R72 audit
// round) replaces the 24h-hardcoded session_turn_logs TTL with a
// configurable, settings_kv-driven cleanup function. The contract pins the
// invariants that keep it safe:
//
//	C1  The cleanup function is callable with (p_ttl_hours, p_batch_size),
//	    deletes on a plain expiry check (expires_at < NOW() — the TTL is
//	    already baked into expires_at at write time), validates p_ttl_hours
//	    against [1,168] with a fail-closed RAISE, and returns the deleted
//	    row count.
//	C1b One call deletes ONE bounded batch (LIMIT p_batch_size on the
//	    primary key). The backlog is drained by the Go caller looping; an
//	    unbounded single-statement DELETE would hold row locks and emit a
//	    WAL spike across the whole first sweep, and a statement timeout
//	    would roll the entire progress back (R72 audit round).
//	C2  No index is created: 430:275 already has
//	    idx_session_turn_logs_expires on the same column, and the draft's
//	    duplicate was pure write amplification.
//	C3  Default behavior (24h, hot-reloadable setting) is preserved: the
//	    spec sets default = 24 with a [1,168] Min/Max; the Go writer bakes
//	    expires_at from the setting, so no SQL-side floor exists (out of
//	    range raises instead — there is no GREATEST(...,1) in the final
//	    function).
//	C4  Up/down symmetry: the down file drops exactly the function the up
//	    file creates (both the batched and any pre-batch signature) and
//	    removes the schema_migrations ledger row.
//	C5  Idempotency: CREATE OR REPLACE FUNCTION, no CONCURRENTLY (small
//	    heap table — transaction-safe), no CREATE TABLE.
//	C6  Registration: apply-db-revision-sequence.sh carries the 753 row
//	    in non-decreasing order with 745/750 (R63 收口预埋 + R68 占位)
//	    on either side, and installer embeddata copy is byte-identical.
//
// Note: the parent handoff briefly says "745"; 745 was already taken by
// report_snapshots in R63 and 750 was rewritten to usage_facts_daily_partition
// in R68 (both unrelated, both 24h-audit siblings). 753 is the next free slot
// and avoids any partition-shaped conflict with 750.
// stripGoLineComments removes `//` line comments from Go source so a
// negative assertion ("must no longer contain X") does not match the doc
// comment that explains why X was removed. Mirrors stripSQLComments for
// the Go side of the contract.
func stripGoLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestMigration753SessionTurnLogsTTLContract(t *testing.T) {
	upBytes, err := os.ReadFile("753_session_turn_logs_ttl.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)
	// upCode is the same, but with `--` prose removed. The 753 header
	// deliberately documents the first draft's buggy predicate and the
	// duplicate index verbatim, so any assertion of the form "must not
	// contain X" has to run against code only — otherwise the audit
	// narrative makes its own regression test fail.
	upCode := normalizeSQL(stripSQLComments(up))

	downBytes, err := os.ReadFile("753_session_turn_logs_ttl.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)

	// ── C1: cleanup_session_turn_logs_by_ttl(p_ttl_hours int, p_batch_size int) RETURNS bigint ─
	if !strings.Contains(upCompact, "CREATE OR REPLACE FUNCTION CLEANUP_SESSION_TURN_LOGS_BY_TTL(") ||
		!strings.Contains(upCompact, "P_TTL_HOURS INT") || !strings.Contains(upCompact, "P_BATCH_SIZE INT DEFAULT 10000") ||
		!strings.Contains(upCompact, "RETURNS BIGINT") {
		t.Error("753 must define CREATE OR REPLACE FUNCTION cleanup_session_turn_logs_by_ttl(p_ttl_hours int, p_batch_size int DEFAULT 10000) RETURNS bigint")
	}
	// Semantics corrected by the 2026-09-27 critical audit. expires_at is
	// baked at write time (write time + TTL), so subtracting the TTL again
	// in the predicate yielded "row age > 2x TTL" — a silent doubling of
	// retention. The sweep must be a plain expiry check.
	if !strings.Contains(upCode, "WHERE EXPIRES_AT < NOW()") {
		t.Error("753 cleanup must delete on a plain expiry check (expires_at < NOW()); subtracting the TTL again doubles retention")
	}
	if strings.Contains(upCode, "NOW() - MAKE_INTERVAL") || strings.Contains(upCode, "NOW()-MAKE_INTERVAL") {
		t.Error("753 cleanup must NOT subtract the TTL from NOW() — expires_at already encodes the TTL, so that is a 2x-retention bug")
	}
	// p_ttl_hours no longer drives the predicate; it is a fail-closed
	// interlock so a bad settings value cannot silently nuke or never
	// sweep the table.
	if !strings.Contains(upCode, "RAISE EXCEPTION") {
		t.Error("753 cleanup must RAISE EXCEPTION on an out-of-range p_ttl_hours (fail-closed interlock)")
	}
	if !strings.Contains(upCode, "P_TTL_HOURS < 1 OR P_TTL_HOURS > 168") {
		t.Error("753 cleanup must validate p_ttl_hours against [1,168] to match the spec Min/Max")
	}
	if !strings.Contains(upCode, "RETURN V_DELETED") {
		t.Error("753 cleanup must RETURN the deleted row count so callers can log/meter it")
	}

	// ── C1b: one call deletes ONE bounded batch, drained by the caller ──
	// An unbounded single-statement DELETE made the first sweep on a
	// backlog-carrying database a long transaction (row locks + WAL spike),
	// and a statement timeout rolled the entire progress back with the next
	// retry 24h away — the sweep would never converge. The batch is selected
	// by primary key and bounded by LIMIT p_batch_size.
	if !strings.Contains(upCode, "LIMIT P_BATCH_SIZE") {
		t.Error("753 cleanup DELETE must be bounded (LIMIT p_batch_size) — an unbounded DELETE turns the first sweep into a long transaction that a timeout rolls back whole")
	}
	if !strings.Contains(upCode, "WHERE ID IN (") {
		t.Error("753 cleanup batch must be selected by primary key (WHERE id IN (SELECT id ... LIMIT ...))")
	}

	// ── C2: no duplicate index on expires_at ───────────────────────────
	// 430:275 already creates idx_session_turn_logs_expires on
	// session_turn_logs(expires_at). A second index on the same column is
	// pure write amplification on a table that gets ~7 INSERTs per turn.
	if strings.Contains(upCode, "CREATE INDEX IF NOT EXISTS IDX_SESSION_TURN_LOGS_EXPIRES_AT") {
		t.Error("753 must NOT create idx_session_turn_logs_expires_at — 430:275 already indexes expires_at; a same-column duplicate is write amplification")
	}
	if strings.Contains(normalizeSQL(stripSQLComments(up)), "ON PUBLIC.SESSION_TURN_LOGS (EXPIRES_AT)") {
		t.Error("753 must not add any index on session_turn_logs(expires_at) — migration 430:275 already has one")
	}

	// ── C2b: the real hardcode must be gone from the writer ────────────
	// Retention is only configurable if the writer stops baking a literal
	// 24h. This is the assertion that makes the subtask meaningful; without
	// it the setting would look wired while changing nothing.
	writerBytes, err := os.ReadFile("../../../domains/session/v2/turn_logs_writer.go")
	if err != nil {
		t.Fatal(err)
	}
	// Strip Go line comments for the same reason as upCode: the writer's
	// doc comment names the old literal to explain why it was removed.
	writer := stripGoLineComments(string(writerBytes))
	if strings.Contains(writer, "time.Now().Add(24*time.Hour)") {
		t.Error("turn_logs_writer.go must not hardcode time.Now().Add(24*time.Hour) — that literal is the live TTL and bypasses the setting")
	}
	if !strings.Contains(writer, "lifecycle.session_turn_logs_ttl_hours") {
		t.Error("turn_logs_writer.go must read lifecycle.session_turn_logs_ttl_hours when baking expires_at")
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
	// The final migration creates no index, so its down must not try to
	// drop one. (The first draft did create a duplicate index; the critical
	// audit removed it. A down that still drops it would be harmless but
	// would signal the wrong contract.)
	//
	// Checked against comment-stripped SQL: the down file deliberately
	// MENTIONS that DROP in prose, as remediation guidance for any
	// environment that already applied the draft. Grepping the raw file
	// would false-positive on that sentence.
	if strings.Contains(stripSQLComments(down), "DROP INDEX") {
		t.Error("753 down must not drop any index — 753 no longer creates one (430:275 already covers expires_at)")
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

	// ── bg.PartitionManager wires the new cleanup function, batched ─────
	// The cleanup runs on the 24h archiveOldPartitionsIfNeeded tick (not the
	// 1h runCleanup goroutine), and drains the backlog as a loop of bounded
	// batches rather than one call.
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
	if !strings.Contains(pm, "sessionTurnLogsTTLCleanupBatchSize") {
		t.Error("bg.PartitionManager must drain the TTL cleanup in bounded batches (loop over sessionTurnLogsTTLCleanupBatchSize) — a single call cannot converge a large backlog under the statement timeout")
	}
}
