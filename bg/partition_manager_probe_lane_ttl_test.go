package bg

// partition_manager_probe_lane_ttl_test.go — U1 探测族 TTL 清理门。
//
// 钉三条：
//   ① clamp 的 [1,90] 夹取（0/负值绝不能透传成「立刻清空」）
//   ② cleanupOldProbeLaneTables 在 db==nil 时 no-op
//   ③ archiveOldPartitionsIfNeeded 必须调用探测族清理（接线牙）
//   ④ 清理 SQL 目标必须是探测族两表、不得误清业务 hot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClampProbeLaneTTLDays(t *testing.T) {
	cases := []struct {
		raw, want int
	}{
		{14, 14},
		{1, 1},
		{90, 90},
		{0, 1},
		{-3, 1},
		{999, 90},
	}
	for _, tc := range cases {
		if got := clampProbeLaneTTLDays(tc.raw); got != tc.want {
			t.Errorf("clampProbeLaneTTLDays(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestClampProbeLaneTTLDays_NeverZero(t *testing.T) {
	for _, raw := range []int{-1 << 30, -1, 0, 1, 14, 90, 1 << 30} {
		if got := clampProbeLaneTTLDays(raw); got < 1 {
			t.Fatalf("clampProbeLaneTTLDays(%d) = %d, must never be < 1", raw, got)
		}
	}
}

func TestCleanupOldProbeLaneTables_NilPoolIsNoOp(t *testing.T) {
	pm := &PartitionManager{}
	pm.cleanupOldProbeLaneTables(context.Background())
}

func TestArchiveOldPartitionsCallsProbeLaneTTL(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "partition_manager.go"))
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func (pm *PartitionManager) archiveOldPartitionsIfNeeded")
	if idx < 0 {
		t.Fatal("archiveOldPartitionsIfNeeded not found")
	}
	rest := body[idx:]
	end := strings.Index(rest[1:], "\nfunc (")
	if end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "pm.cleanupOldProbeLaneTables(ctx)") {
		t.Error("archiveOldPartitionsIfNeeded 必须调用 cleanupOldProbeLaneTables\n" +
			"  否则 session_turns_probe_hot / request_stats_probe_minute 单调增长（U1）")
	}
}

func TestProbeLaneTTLCleanupTargetsProbeTablesOnly(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "partition_manager_probe_lane_ttl.go"))
	if err != nil {
		t.Fatalf("read probe lane ttl: %v", err)
	}
	body := string(src)
	for _, table := range []string{"session_turns_probe_hot", "request_stats_probe_minute"} {
		if !strings.Contains(body, "DELETE FROM "+table) {
			t.Errorf("cleanup must DELETE FROM %s", table)
		}
	}
	for _, biz := range []string{
		"DELETE FROM session_turns_hot",
		"DELETE FROM session_turns ",
		"DELETE FROM request_stats_minute ",
	} {
		if strings.Contains(body, biz) {
			t.Errorf("probe lane TTL must not touch business table via %q", biz)
		}
	}
}
