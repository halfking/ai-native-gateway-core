package bg

// 2026-10-06（审计 §10.53）：analyze 互斥的判据。
//
// 背景：promote 路径早有 promoteLockKey + pg_try_advisory_xact_lock，
// 注释明确「两台实例共享同一个 PG」，抢不到就跳过该表；
// analyze 此前**完全没有互斥**，于是 154 与 245 各跑一遍全量 44 表
// （实测 7+7=14 与 pss Δcalls 闭合）。

import (
	"os"
	"strings"
	"testing"
)

// 键必须在两台实例上算出同一个值，否则互斥形同虚设。
// FNV-1a 是纯函数，所以「同一输入 → 同一输出」由确定性保证；
// 这里钉的是「常量前缀不被误改」——改了前缀两台会立刻不再互斥。
func TestAnalyzeLockKeyIsStableAndDistinctFromPromote(t *testing.T) {
	want := analyzeLockKey()
	if got := analyzeLockKey(); got != want {
		t.Fatalf("analyzeLockKey 不稳定：%d → %d", want, got)
	}
	// 与 promote 的键空间必须分离：共用键会让 promote 持锁时 analyze 被跳过。
	if analyzeLockKey() == promoteLockKey("session_turns_hot") {
		t.Fatal("analyzeLockKey 与 promoteLockKey 撞键了 —— 两个独立作业会互相跳过")
	}
}

// 接线门：只测键算得对，测不到「analyze 到底有没有去抢锁」。
// 若有人只提交 analyzeLockKey 而忘了在 analyzePartitionStats 里调用，
// 上面那条判据仍全绿，而生产行为一点没变。
func TestAnalyzePathActuallyTakesTheLock(t *testing.T) {
	src, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	s := string(src)

	// 定位 analyzePartitionStats 函数体，确认取锁语句在它里面。
	start := strings.Index(s, "func (pm *PartitionManager) analyzePartitionStats(")
	if start < 0 {
		t.Fatal("未找到 analyzePartitionStats")
	}
	end := strings.Index(s[start:], "\nfunc ")
	if end < 0 {
		end = len(s) - start
	}
	body := s[start : start+end]

	if !strings.Contains(body, "analyzeLockKey()") {
		t.Error("analyzePartitionStats 内没有调用 analyzeLockKey() —— " +
			"分析轮次仍然没有任何互斥，两台实例会各跑一遍全量")
	}
	if !strings.Contains(body, "pg_try_advisory_xact_lock") {
		t.Error("analyzePartitionStats 内没有 pg_try_advisory_xact_lock —— " +
			"必须用 try 锁：阻塞锁会让输家一路等到 statement_timeout")
	}
	// 抢不到必须跳过，不能继续跑 ANALYZE。
	if !strings.Contains(body, "if !locked {") {
		t.Error("未处理 `!locked` 分支 —— 抢不到锁时若仍继续 ANALYZE，互斥等于没有")
	}
	if !strings.Contains(body, "analyze skipped (peer holds lock)") {
		t.Error("缺少「因对端持锁而跳过」的显式日志 —— " +
			"静默跳过会让「本轮到底谁做的」无法从日志证伪")
	}
	// 取锁必须在 SET LOCAL 之后、真正调用 analyze 之前。
	lockAt := strings.Index(body, "pg_try_advisory_xact_lock")
	setAt := strings.Index(body, "SET LOCAL statement_timeout")
	callAt := strings.Index(body, "analyze_llm_gateway_table_stats")
	if !(setAt >= 0 && lockAt > setAt && callAt > lockAt) {
		t.Errorf("语句顺序不对：SET LOCAL@%d 取锁@%d 调用 analyze@%d，应为 SET LOCAL → 取锁 → 调用",
			setAt, lockAt, callAt)
	}
}
