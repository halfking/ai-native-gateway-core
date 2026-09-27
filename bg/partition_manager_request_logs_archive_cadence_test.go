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
