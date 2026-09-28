package bg

// partition_manager_request_logs_archive_cadence_test.go —
// 第三轮批判式审计（handoff §18）。
//
// 审计发现两件事，都不是「写错了」而是「承诺与实现不符」：
//
//	F-11 本迁移是**摘要抽取**不是数据搬移 —— 函数内没有任何 DELETE，主表
//	     并不会变小。命名与 handoff 标题都容易被读成「旧数据离开主表」。
//	F-12 SQL 无「已归档」标记，每次调用都要把每个过期分区的全部行再走一遍
//	     ON CONFLICT 插入路径；挂在每小时 tick 上就是永不收敛的全表重扫。
//	     修法：Go 侧限到每日一次（shouldRunRequestLogsArchive）。
//
// 本文件钉住节奏函数（纯函数，可变异）与「永不 DELETE」这一不变量。

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestShouldRunRequestLogsArchive_OnlyOneHourPerDay(t *testing.T) {
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.Local)

	hits := 0
	for h := 0; h < 24; h++ {
		now := base.Add(time.Duration(h) * time.Hour)
		if shouldRunRequestLogsArchive(now) {
			hits++
			if h != requestLogsArchiveHourOfDay {
				t.Errorf("shouldRunRequestLogsArchive(hour=%d) = true, want only hour %d", h, requestLogsArchiveHourOfDay)
			}
		}
	}
	if hits != 1 {
		t.Errorf("archive sweep fired %d times in 24h, want exactly 1 (F-12: the SQL has no already-archived marker, so every run re-scans all past-due partitions)", hits)
	}
}

// TestShouldRunRequestLogsArchive_MinuteInsensitive pins that the gate keys on
// the hour only. The cleanup ticker fires at an arbitrary minute offset, so a
// gate that also matched minutes would skip the day entirely.
func TestShouldRunRequestLogsArchive_MinuteInsensitive(t *testing.T) {
	h := time.Date(2026, 9, 27, requestLogsArchiveHourOfDay, 0, 0, 0, time.Local)
	if !shouldRunRequestLogsArchive(h) {
		t.Error("must fire at the start of the archive hour")
	}
	if !shouldRunRequestLogsArchive(h.Add(59 * time.Minute)) {
		t.Error("must still fire at :59 of the archive hour — the cleanup ticker's minute offset is arbitrary")
	}
	if shouldRunRequestLogsArchive(h.Add(time.Hour)) {
		t.Error("must not fire in the hour after the archive hour")
	}
}

// TestMigration754_NeverDeletesFromSource pins F-11. If anyone ever adds a
// DELETE to the archive function, request_logs rows would disappear — and with
// R68 forbidding DROP of the parent's monthly partitions, that would be a data
// -loss event no test elsewhere would catch.
//
// Read with comments stripped: the migration's own header explains this
// invariant in prose, and a raw grep would count that prose. (That exact
// mistake was made while writing the header — see §18.)
func TestMigration754_NeverDeletesFromSource(t *testing.T) {
	b, err := os.ReadFile("../sql/migrations/startup/754_archive_request_logs_default.sql")
	if err != nil {
		t.Fatalf("read migration 754: %v", err)
	}
	code := stripLineComments(string(b))
	if strings.Contains(strings.ToUpper(code), "DELETE") {
		t.Error("754 must never DELETE from request_logs — it is summary extraction; the source partitions stay intact (R68 forbids dropping them)")
	}
	if strings.Contains(strings.ToUpper(code), "TRUNCATE") {
		t.Error("754 must never TRUNCATE — same invariant as the DELETE check")
	}
}

// TestArchiveOldRequestLogs_GateIsActuallyInvoked closes the gap that made the
// first version of this file vacuous: the two tests above exercise the pure
// helper, so deleting the gate's *call* from archiveOldRequestLogs left them
// all green while the sweep silently went back to running hourly. Same failure
// class as the C7 assertion in migration_754_test.go — an assertion that can
// only see the definition, not the wiring.
func TestArchiveOldRequestLogs_GateIsActuallyInvoked(t *testing.T) {
	b, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	// Scope to the archiveOldRequestLogs body: the gate must be inside the
	// function that does the sweep, not merely present in the file.
	i := strings.Index(string(b), "func (pm *PartitionManager) archiveOldRequestLogs(")
	if i < 0 {
		t.Fatal("archiveOldRequestLogs not found")
	}
	rest := string(b)[i:]
	end := strings.Index(rest, "\nfunc ")
	if end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "shouldRunRequestLogsArchive(") {
		t.Error("archiveOldRequestLogs must call shouldRunRequestLogsArchive — otherwise the daily gate is defined but never applied and the sweep runs hourly forever")
	}
}

// TestArchiveOldRequestLogs_StatementTimeoutPinnedInsideTx pins the R73-audit
// residual fix: the set-returning archive function is ONE driver-level
// statement — the 1000-row cursor batches inside it do not dilute
// statement_timeout. On 252 the role-level 30s default would kill a large
// first-run backlog mid-function and roll the whole call back (daily retry,
// livelock — same diagnosis as R72 F2 for 753). The caller must therefore
// BEGIN, SET LOCAL statement_timeout='30min' (aligned with its own 30-minute
// context budget, promote/analyze precedent), and Commit — in that order,
// inside the function body. Text-scoped like the gate test above so deleting
// the pin turns this red.
func TestArchiveOldRequestLogs_StatementTimeoutPinnedInsideTx(t *testing.T) {
	b, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	i := strings.Index(string(b), "func (pm *PartitionManager) archiveOldRequestLogs(")
	if i < 0 {
		t.Fatal("archiveOldRequestLogs not found")
	}
	rest := string(b)[i:]
	end := strings.Index(rest, "\nfunc ")
	if end > 0 {
		rest = rest[:end]
	}
	begin := strings.Index(rest, "pm.db.Begin(")
	setLocal := strings.Index(rest, "SET LOCAL statement_timeout = '30min'")
	call := strings.Index(rest, "archive_request_logs_default($1)")
	commit := strings.Index(rest, "tx.Commit(")
	if begin < 0 {
		t.Error("archiveOldRequestLogs must open an explicit tx (pm.db.Begin) — a bare pm.db.Query leaves the whole set-returning call under the role-level statement_timeout")
	}
	if setLocal < 0 {
		t.Error("archiveOldRequestLogs must SET LOCAL statement_timeout='30min' before the sweep — 252's role-level 30s kills a large first backlog and rolls it all back")
	}
	if call < 0 {
		t.Fatal("archiveOldRequestLogs must still call archive_request_logs_default")
	}
	if !(begin < setLocal && setLocal < call && call < commit) {
		t.Errorf("ordering broken: Begin(%d) must precede SET LOCAL(%d) must precede the archive call(%d) must precede Commit(%d)",
			begin, setLocal, call, commit)
	}
}

// TestTurnLogsBacklogGaugeWiredIntoHourlyCleanupLoop pins the R73-audit N2
// fix: the turn-logs backlog gauges (llm_gateway_session_turn_logs_backlog_*)
// must actually be refreshed — a registered-but-never-called gauge reads 0
// forever, which is indistinguishable from "healthy" and precisely the
// "assertion that can only see the definition, not the wiring" failure class
// the gate test above documents. Scope to the runCleanup body: the refresh
// must ride the 1h tick (the same loop the archive sweep rides), not the
// 24h phase-locked ticker.
func TestTurnLogsBacklogGaugeWiredIntoHourlyCleanupLoop(t *testing.T) {
	b, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	i := strings.Index(string(b), "func (pm *PartitionManager) runCleanup(")
	if i < 0 {
		t.Fatal("runCleanup not found")
	}
	rest := string(b)[i:]
	end := strings.Index(rest, "\nfunc ")
	if end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "pm.refreshTurnLogsBacklogGauge(ctx)") {
		t.Error("runCleanup must call pm.refreshTurnLogsBacklogGauge(ctx) — otherwise the backlog gauges are registered but never refreshed and read 0 forever")
	}
}

// stripLineComments removes `--` comments so assertions target executable SQL.
func stripLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if idx := strings.Index(l, "--"); idx >= 0 {
			lines[i] = l[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// TestArchiveOldRequestLogs_CalledFromHourlyCleanupLoop pins the *wiring* half
// of the E1a fix. The test above only sees the gate inside the sweep function;
// moving the call back to the 24h phased run() loop would still leave both
// existing tests green while the phase-locked bug (sweep only runs when the
// process happened to start inside 03:00–03:59) silently returns. R73 A-1.
func TestArchiveOldRequestLogs_CalledFromHourlyCleanupLoop(t *testing.T) {
	b, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	src := string(b)

	body := func(name string) string {
		i := strings.Index(src, "func (pm *PartitionManager) "+name+"(")
		if i < 0 {
			return ""
		}
		rest := src[i:]
		if end := strings.Index(rest, "\nfunc "); end > 0 {
			rest = rest[:end]
		}
		return rest
	}

	cleanup := body("runCleanup")
	if cleanup == "" {
		t.Fatal("runCleanup not found")
	}
	if !strings.Contains(cleanup, "pm.archiveOldRequestLogs(") {
		t.Error("archiveOldRequestLogs must be called from runCleanup (the 1h ticker loop) — anywhere else re-creates the phase-locked daily-gate failure (16th audit E1a)")
	}
	for _, name := range []string{"run", "archiveOldPartitionsIfNeeded"} {
		b2 := body(name)
		if b2 == "" {
			t.Fatalf("%s not found", name)
		}
		if strings.Contains(b2, "pm.archiveOldRequestLogs(") {
			t.Errorf("%s must NOT call archiveOldRequestLogs — the 24h phased ticker's phase is process start time, so the 03:00–03:59 gate would only fire for processes started inside that hour", name)
		}
	}
}
