package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 2026-10-31 实施包审计：可选信号降级的判据。
// 注意：这里钉的是 SQL 形态与状态机；"降级 SQL 在真库上可执行、函数缺失时候选仍可返回"
// 由 candidate_optional_signal_realdb_test.go 在有 DSN 时实测。

func TestCandidateCacheSignalCallShapeIsUnique(t *testing.T) {
	full := candidateQuerySQL()
	if n := strings.Count(full, candidateCacheSignalCallSQL); n != 1 {
		t.Fatalf("chr 调用形态在候选查询中出现 %d 次，必须恰为 1；模板被改写时降级会静默失效", n)
	}
}

func TestCandidateQuerySQLWithoutCacheSignal_RemovesOnlyTheChrCall(t *testing.T) {
	full := candidateQuerySQL()
	degraded := candidateQuerySQLWithoutCacheSignal()

	if strings.Contains(degraded, "recent_cache_hit_rate(") {
		t.Fatal("降级 SQL 仍然调用 recent_cache_hit_rate")
	}
	if !strings.Contains(degraded, candidateCacheSignalAbsentSQL) {
		t.Fatal("降级 SQL 缺少 chr 中性投影")
	}
	// 只允许这一处差异：把降级投影还原回调用形态，必须逐字等于完整 SQL。
	if restored := strings.Replace(degraded, candidateCacheSignalAbsentSQL, candidateCacheSignalCallSQL, 1); restored != full {
		t.Fatal("降级 SQL 与完整 SQL 的差异不止 chr 一处")
	}
	// 可选维度之外的硬依赖（rsr，硬过滤信号）必须原样保留。
	if !strings.Contains(degraded, "recent_success_rate(c.id, mo.raw_model_name, 50)") {
		t.Fatal("降级不得移除 recent_success_rate（它是硬过滤信号，不是可选维度）")
	}
}

func TestIsCacheSignalAbsent(t *testing.T) {
	undefinedChr := &pgconn.PgError{
		Code:    "42883",
		Message: "function recent_cache_hit_rate(bigint, text, integer) does not exist",
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"chr 函数缺失", undefinedChr, true},
		{"包装后的 chr 函数缺失", fmt.Errorf("query candidates failed: %w", undefinedChr), true},
		{"其它函数缺失（rsr 是硬信号，不降级）", &pgconn.PgError{
			Code: "42883", Message: "function recent_success_rate(bigint, text, integer) does not exist"}, false},
		{"同名但非 42883（表缺失）", &pgconn.PgError{
			Code: "42P01", Message: "relation recent_cache_hit_rate does not exist"}, false},
		{"纯文本含函数名但无 SQLSTATE", errors.New("recent_cache_hit_rate does not exist"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCacheSignalAbsent(tc.err); got != tc.want {
				t.Fatalf("isCacheSignalAbsent=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCacheSignalDegrade_WindowBoundary(t *testing.T) {
	var d cacheSignalDegrade
	now := time.Unix(1_700_000_000, 0)
	if d.degradedAt(now) {
		t.Fatal("零值不得处于降级态")
	}
	d.markDegraded(now)
	if !d.degradedAt(now.Add(candidateCacheSignalRecheck - time.Second)) {
		t.Fatal("窗口内应处于降级态")
	}
	if d.degradedAt(now.Add(candidateCacheSignalRecheck + time.Second)) {
		t.Fatal("窗口到期后必须重新探测全量 SQL（函数补齐后自愈）")
	}
}
