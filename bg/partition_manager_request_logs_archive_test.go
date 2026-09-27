package bg

// partition_manager_request_logs_archive_test.go —
// R67 session-storage 审计子任务 7 (handoff §9) 的单测。
//
// 覆盖两条真实行为：
//   ① clampRequestLogsArchiveDays 的 [7,365] 夹取 —— SQL 侧对越界值
//      RAISE EXCEPTION，Go 侧夹取让坏配置退化为安全默认而不是每个 tick
//      整次调用失败。
//   ② archiveOldRequestLogs 在 db == nil 时 no-op 不 panic（同
//      cleanupSessionTurnLogsByTTL / AuditTrimmer.TrimOnce 契约）。
//
// 另有一条「真的按集合返回读」的形状约束：SQL 函数
// archive_request_logs_default RETURNS TABLE(archived_partition,
// rows_archived)，即每个分区一行。必须用 Query 逐行累加；用 QueryRow 只会
// 读到第一个分区并低报工作量 —— 与 scalarResult 注释里记的 42703 事故同源。

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestClampRequestLogsArchiveDays(t *testing.T) {
	cases := []struct {
		name string
		raw  int
		want int
	}{
		{"default 30 passes through", 30, 30},
		{"lower bound 7 is legal", 7, 7},
		{"upper bound 365 is legal", 365, 365},
		{"zero is clamped to the floor", 0, 7},
		{"below floor is clamped", 3, 7},
		{"negative is clamped", -30, 7},
		{"above ceiling is clamped", 1000, 365},
		{"one year is the cap", 365, 365},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampRequestLogsArchiveDays(tc.raw); got != tc.want {
				t.Errorf("clampRequestLogsArchiveDays(%d) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestClampRequestLogsArchiveDays_AlwaysInsideSQLGuard pins the contract with
// the SQL side: any input the Go clamp produces must satisfy the RAISE
// boundaries in archive_request_logs_default, or every tick would abort.
func TestClampRequestLogsArchiveDays_AlwaysInsideSQLGuard(t *testing.T) {
	for _, raw := range []int{-1 << 30, -1, 0, 1, 6, 7, 30, 365, 366, 1 << 30} {
		got := clampRequestLogsArchiveDays(raw)
		if got < 7 || got > 365 {
			t.Fatalf("clampRequestLogsArchiveDays(%d) = %d, outside SQL guard [7,365]", raw, got)
		}
	}
}

func TestArchiveOldRequestLogs_NilPoolIsNoOp(t *testing.T) {
	pm := &PartitionManager{} // db intentionally nil
	pm.archiveOldRequestLogs(context.Background())
}

// TestArchiveOldRequestLogs_SumsEveryPartition guards the result shape. The
// SQL returns one row per archived partition; a QueryRow-style read would take
// only the first and under-report. This is a source-shape assertion because
// PartitionManager.db is a concrete *pgxpool.Pool and cannot be mocked.
func TestArchiveOldRequestLogs_SumsEveryPartition(t *testing.T) {
	src := partitionManagerSource(t)
	if !strings.Contains(src, "FROM archive_request_logs_default($1)") {
		t.Error("archive must call archive_request_logs_default($1)")
	}
	if !strings.Contains(src, "SELECT archived_partition, rows_archived FROM archive_request_logs_default($1)") {
		t.Error("archive must select both columns and iterate rows — the function returns one row per partition, so a single-row read under-reports the work")
	}
	if !strings.Contains(src, "totalRows += archived") {
		t.Error("archive must accumulate rows_archived across all returned partitions")
	}
	if strings.Contains(src, "QueryRow(timeoutCtx, \"SELECT archive_request_logs_default") {
		t.Error("archive must not use QueryRow against a set-returning function — it reads only the first partition")
	}
}

// partitionManagerSource loads the implementation so the shape assertion reads
// shipped code rather than a transcription that can drift. Go runs tests with
// CWD set to the package directory.
func partitionManagerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	return string(b)
}
