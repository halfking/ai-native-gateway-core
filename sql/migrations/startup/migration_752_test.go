package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 752 (Mock Probe 生产入口收口轮, 2026-09-27) canonicalizes the
// mock_probe_history DDL that lived as the hand-run migrations/036 file. The
// contract pins the two hardenings relative to 036 plus the operational
// skeleton — the same class of bugs migration 750's R69 revision documented
// (对偶 C9):
//
//	C1  Function-level timezone pinning: the partition function declares
//	    `SET timezone = 'Asia/Shanghai'` as proconfig. Without it the
//	    boundary math follows the session timezone and a UTC session yields
//	    partitions misaligned 8h from the Shanghai day — which then overlaps
//	    the neighbors on ATTACH and never self-heals.
//	C2  Shanghai-calendar derivation: the day parameter comes from
//	    `(now() AT TIME ZONE 'Asia/Shanghai')::date`. `current_date` is
//	    forbidden in code — it evaluates in the session timezone (the 036
//	    bug).
//	C3  move-then-attach skeleton: the daily partition is created as a
//	    standalone LIKE…INCLUDING INDEXES table, the DEFAULT partition is
//	    locked ACCESS EXCLUSIVE, the day's rows are moved in, and only then
//	    is the table ATTACHed. CREATE TABLE … PARTITION OF would be killed
//	    by the DEFAULT constraint check once rows landed there before the
//	    partition existed (boot order makes that window real).
//	C4  Idempotency: CREATE TABLE/INDEX IF NOT EXISTS + CREATE OR REPLACE
//	    FUNCTION + pg_inherits short-circuit checked again after the
//	    advisory lock — replaying on a store that already has the 036-era
//	    table (252) must converge, not fail.
//	C5  Caller wiring: internal/mockprobe must invoke
//	    mock_probe_history_daily_partition() from both the startup
//	    bootstrap and the writeLoop daily ensure tick, otherwise the
//	    "tomorrow" partition is never created by a long-running process.
//	C6  Registration: apply-db-revision-sequence.sh carries the 752 row.
func TestMigration752MockProbeHistoryContract(t *testing.T) {
	upBytes, err := os.ReadFile("752_mock_probe_history.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)
	// Code-only view: the header narrates the 036 bug verbatim (including
	// the forbidden current_date), so negative assertions must run against
	// comment-stripped SQL only.
	upCode := normalizeSQL(stripSQLComments(up))

	// ── C1: function-level timezone pinning (proconfig) ─────────────────
	if !strings.Contains(upCode, "LANGUAGE PLPGSQL SET TIMEZONE = 'ASIA/SHANGHAI'") {
		t.Error("752 partition function must pin SET timezone = 'Asia/Shanghai' as proconfig — session-timezone-dependent boundary math yields UTC-misaligned partitions that overlap on ATTACH and never self-heal")
	}

	// ── C2: Shanghai-calendar derivation, no current_date ───────────────
	if !strings.Contains(upCode, "(NOW() AT TIME ZONE 'ASIA/SHANGHAI')::DATE") {
		t.Error("752 must derive the partition day from (now() AT TIME ZONE 'Asia/Shanghai')::date — the Shanghai calendar is the partition contract")
	}
	if strings.Contains(upCode, "CURRENT_DATE") {
		t.Error("752 must not use current_date — it evaluates in the session timezone, which is the 036 bug the pinning exists to fix")
	}

	// ── C3: move-then-attach skeleton ───────────────────────────────────
	if !strings.Contains(upCode, "(LIKE MOCK_PROBE_HISTORY INCLUDING DEFAULTS INCLUDING INDEXES)") {
		t.Error("752 must create the daily partition as a standalone LIKE … INCLUDING DEFAULTS INCLUDING INDEXES table so ATTACH matches indexes by metadata")
	}
	if !strings.Contains(upCode, "LOCK TABLE MOCK_PROBE_HISTORY_DEFAULT IN ACCESS EXCLUSIVE MODE") {
		t.Error("752 must lock the DEFAULT partition before moving rows — concurrent writes landing in DEFAULT during the move window make ATTACH fail its constraint check")
	}
	if !strings.Contains(upCode, "DELETE FROM MOCK_PROBE_HISTORY_DEFAULT") ||
		!strings.Contains(upCode, "RETURNING *") {
		t.Error("752 must move the day's rows out of DEFAULT (DELETE … RETURNING * + INSERT) before ATTACH — otherwise the DEFAULT constraint check kills the ATTACH")
	}
	if !strings.Contains(upCode, "ATTACH PARTITION") ||
		!strings.Contains(upCode, "FOR VALUES FROM (") {
		t.Error("752 must ATTACH the standalone table as the day's range partition (move-then-attach)")
	}

	// ── C4: idempotency (safe replay over a 036-era store) ─────────────
	if !strings.Contains(upCompact, "CREATE TABLE IF NOT EXISTS MOCK_PROBE_HISTORY") {
		t.Error("752 must use CREATE TABLE IF NOT EXISTS — 252 already has the table from the hand-run 036")
	}
	if !strings.Contains(upCompact, "CREATE OR REPLACE FUNCTION MOCK_PROBE_HISTORY_DAILY_PARTITION()") {
		t.Error("752 must CREATE OR REPLACE the partition function — replay upgrades 036's unpinned version to the pinned one")
	}
	if !strings.Contains(upCode, "PG_INHERITS") {
		t.Error("752 must short-circuit on pg_inherits before creating a day's partition (idempotent ensure)")
	}
	if !strings.Contains(upCode, "PG_ADVISORY_XACT_LOCK") {
		t.Error("752 must serialize concurrent ensure (advisory lock) — boot and the writeLoop tick can race the same day")
	}
	// Re-check after the lock: the double-check is what closes the race the
	// advisory lock only narrows.
	if strings.Count(upCode, "PG_INHERITS") < 2 {
		t.Error("752 must check pg_inherits both before and after taking the advisory lock — the post-lock recheck is what closes the race")
	}

	// ── C5: caller wiring (bootstrap + daily ensure tick) ───────────────
	runnerBytes, err := os.ReadFile("../../../internal/mockprobe/runner.go")
	if err != nil {
		t.Fatal(err)
	}
	runner := string(runnerBytes)
	if strings.Count(runner, "mock_probe_history_daily_partition()") < 2 {
		t.Error("internal/mockprobe must call mock_probe_history_daily_partition() from BOTH the startup bootstrap and the writeLoop daily ensure tick — a long-running process otherwise never creates tomorrow's partition")
	}

	// ── C6: revision-sequence registration ──────────────────────────────
	seqBytes, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatal(err)
	}
	seq := string(seqBytes)
	if !strings.Contains(seq, "752_mock_probe_history.sql") {
		t.Error("apply-db-revision-sequence.sh must carry migration 752_mock_probe_history.sql — upgrade-database deployments would never apply it otherwise")
	}
}
