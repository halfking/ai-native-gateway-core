package stats

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestDailyMonthlyRollup_StatementTimeoutAndPeerLockPinnedInsideTx pins the
// R168 audit fix (252 SQL log round, 2026-10-27). Production evidence: the
// role-level statement_timeout=30s killed this hourly rebuild 293 times in 7
// days (flat across hours — intrinsic, not load spikes alone), 76% of all
// attempts, each killed attempt rolling back and redoing the identical 3-day
// window on the next tick; and both gateway instances run the same rebuild
// with no coordination. The tx must therefore SET LOCAL statement_timeout
// (aligned with rollupRefreshBudget, promote/analyze precedent) and take the
// single-runner advisory try-lock BEFORE the heavy DELETE/INSERT — in that
// order. Text-scoped like bg's archive-cadence pins: deleting either pin
// turns this red.
func TestDailyMonthlyRollup_StatementTimeoutAndPeerLockPinnedInsideTx(t *testing.T) {
	b, err := os.ReadFile("daily_monthly_rollup.go")
	if err != nil {
		t.Fatalf("read daily_monthly_rollup.go: %v", err)
	}
	i := strings.Index(string(b), "func (r *DailyMonthlyRollup) Refresh(")
	if i < 0 {
		t.Fatal("Refresh not found")
	}
	rest := string(b)[i:]
	end := strings.Index(rest, "\nconst dailyInsertSQL")
	if end > 0 {
		rest = rest[:end]
	}

	begin := strings.Index(rest, "r.db.Begin(")
	setLocal := strings.Index(rest, "SET LOCAL statement_timeout = '")
	lock := strings.Index(rest, "pg_try_advisory_xact_lock")
	del := strings.Index(rest, "DELETE FROM stats_usage_daily")
	commit := strings.Index(rest, "tx.Commit(")

	if begin < 0 {
		t.Fatal("Refresh must open an explicit tx (r.db.Begin) — a bare Exec leaves the rebuild under the role-level statement_timeout")
	}
	if setLocal < 0 {
		t.Error("Refresh must SET LOCAL statement_timeout before the rebuild — 252's role-level 30s killed 293 attempts in 7 days and every kill redid the identical window")
	}
	if lock < 0 {
		t.Error("Refresh must take the stats-rollup advisory try-lock — both gateway instances tick hourly against the same database with no coordination")
	}
	if del < 0 {
		t.Fatal("Refresh must still clear the daily buckets")
	}
	if commit < 0 {
		t.Fatal("Refresh must still commit")
	}
	if !(begin < setLocal && setLocal < lock && lock < del && del < commit) {
		t.Errorf("ordering broken: Begin(%d) < SET LOCAL(%d) < advisory lock(%d) < DELETE(%d) < Commit(%d) required — the lock must precede the heavy DELETE/INSERT or the loser races through real work before discovering the peer",
			begin, setLocal, lock, del, commit)
	}
	// The timeout ceiling must stay derived from the Go-side budget — a
	// hand-written literal drifts silently when the budget changes.
	if !strings.Contains(rest, "rollupRefreshBudget.Milliseconds()") {
		t.Error("SET LOCAL statement_timeout must be derived from rollupRefreshBudget (single source of truth for the ceiling)")
	}

	// refreshRecent must hand CloseEligibleMonths its own budget: sharing the
	// refresh budget means a rebuild that eats it starves the month close.
	ri := strings.Index(string(b), "func (r *DailyMonthlyRollup) refreshRecent(")
	if ri < 0 {
		t.Fatal("refreshRecent not found")
	}
	rrest := string(b)[ri:]
	rend := strings.Index(rrest, "\nfunc ")
	if rend > 0 {
		rrest = rrest[:rend]
	}
	if !strings.Contains(rrest, "rollupCloseBudget") {
		t.Error("refreshRecent must give CloseEligibleMonths its own rollupCloseBudget — a rebuild that eats the shared budget starves the month close")
	}
}

// TestStatsRollupLockKeyDerivation pins the advisory-key derivation contract:
// same seed string, same FNV-1a derivation as bg's promoteLockKey, so every
// gateway instance computes the same key. If the seed changes, two deployed
// instances temporarily disagree and the lock stops excluding anything.
func TestStatsRollupLockKeyDerivation(t *testing.T) {
	if statsRollupLockKey == 0 {
		t.Fatal("statsRollupLockKey must not be 0 (degenerate FNV input)")
	}
	if statsRollupLockSeed != "llm-gateway:stats-daily-rollup:" {
		t.Fatalf("lock seed drifted: %q — changing it splits deployed instances across two keys", statsRollupLockSeed)
	}
	if rollupRefreshBudget != 2*time.Minute {
		t.Errorf("rollupRefreshBudget = %v, want 2m (the SET LOCAL ceiling is aligned to it)", rollupRefreshBudget)
	}
	if rollupCloseBudget != 30*time.Second {
		t.Errorf("rollupCloseBudget = %v, want 30s", rollupCloseBudget)
	}
}
