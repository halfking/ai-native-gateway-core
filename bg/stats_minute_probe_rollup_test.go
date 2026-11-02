package bg

// stats_minute_probe_rollup_test.go —— 探测分钟聚合写入路径的门禁（2026-11-02）。
//
// 钉住的事实：审计发现「探测分钟聚合只挂在 domains/stats.MinuteAccumulator
// 上，而那个累加器全仓无实例化点 ⇒ request_stats_probe_minute 生产恒空」。
//
// ★ 这个形态之所以危险：它同时具备「有写侧代码」和「永不产出」两个特征，
//   比没有更危险 —— 代码在、编译在、测试在，只有产数据的那一环没人接。
//   ⇒ 本门的作用是：一旦探测臂被删掉/被挪回死代码路径，门立刻红。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestProbeMinuteRollupArmIsWired 钉住探测臂接在**生产** rollup 上。
//
// ★ 判据不是「文件存在」，而是「rollupWindow 里真的调用了它」——
//
//	文件在但没接线，和文件不存在是同一种故障。
func TestProbeMinuteRollupArmIsWired(t *testing.T) {
	raw, err := os.ReadFile("stats_minute_rollup.go")
	if err != nil {
		t.Fatalf("read stats_minute_rollup.go: %v", err)
	}
	body := string(raw)

	idx := strings.Index(body, "func (w *StatsMinuteRollup) rollupWindow(")
	if idx < 0 {
		t.Fatal("rollupWindow 不存在 —— 探测臂无处可接")
	}
	rest := body[idx:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "w.rollupProbe(") {
		t.Error("rollupWindow 没有调用 rollupProbe —— 探测分钟聚合不会产数据\n" +
			"  ★ 生产里业务分钟表的真实写入者是 bg.StatsMinuteRollup\n" +
			"    （cmd/gateway/main.go 实例化并 Start）—— 它扫业务视图，\n" +
			"    视图里没有探测行（车道开启后探测不进业务表）\n" +
			"    ⇒ 不在这里加探测臂，request_stats_probe_minute 恒空。")
	}
	if !strings.Contains(rest, "w.retireProbeMinutes(") {
		t.Error("rollupWindow 没有调用 retireProbeMinutes —— 探测桶退役后不会消失\n" +
			"  整桶替换只覆盖本次窗口的键；窗口推进后「已无源数据」的桶会永远留在表里")
	}
}

// TestProbeRollupIsReplaceNotAccumulate 钉住探测臂用**整桶替换**语义。
//
// ★ 为什么这条重要：同一分钟被重复 rollup（重试 / RebuildRangeStats）
//
//	不会重复计数。若写成累加式（+ EXCLUDED），重试一次数字就翻倍。
//	⇒ 这是「可重入」的唯一保证。
func TestProbeRollupIsReplaceNotAccumulate(t *testing.T) {
	raw, err := os.ReadFile("stats_minute_probe_rollup.go")
	if err != nil {
		t.Fatalf("read stats_minute_probe_rollup.go: %v", err)
	}
	body := string(raw)

	// 取探测臂的 SQL 常量。
	//
	// ★ 定位必须跳过「= `」里的那个反引号本身 —— 否则第一个反引号就在
	//   定位串末尾，取到的「SQL」是空串 ⇒ 判据恒红（量具坏了，不是被测对象坏了）。
	const marker = "const rollupProbeStatement = `"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatal("rollupProbeStatement 不存在")
	}
	rest := body[idx+len(marker):]
	end := strings.Index(rest, "`")
	if end < 0 {
		t.Fatal("rollupProbeStatement 的原始字符串未闭合")
	}
	sql := rest[:end]
	if !strings.Contains(sql, "EXCLUDED") {
		t.Fatal("探测臂 SQL 截取异常（不含 EXCLUDED）—— 判据的取串逻辑坏了")
	}

	// 必须有整桶替换的赋值形态。
	if !regexp.MustCompile(`requests\s*=\s*EXCLUDED\.requests`).MatchString(sql) {
		t.Error("探测臂的 requests 必须是 requests = EXCLUDED.requests（整桶替换）\n" +
			"  累加式（requests = request_stats_probe_minute.requests + EXCLUDED.requests）\n" +
			"  会让重试/重建把同一分钟的计数翻倍 ⇒ 不可重入")
	}
	// 必须没有累加形态。
	if regexp.MustCompile(`=\s*request_stats_probe_minute\.\w+\s*\+`).MatchString(sql) {
		t.Error("探测臂出现了累加式 upsert（... + EXCLUDED.x）—— 与整桶替换语义冲突")
	}
}

// TestProbeRollupReadsProbeLaneTable 钉住探测臂的数据源是探测族表。
func TestProbeRollupReadsProbeLaneTable(t *testing.T) {
	raw, err := os.ReadFile("stats_minute_probe_rollup.go")
	if err != nil {
		t.Fatalf("read stats_minute_probe_rollup.go: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "FROM session_turns_probe_hot r") {
		t.Error("探测臂必须从 session_turns_probe_hot 读（探测流量的落点表）")
	}
	if !strings.Contains(body, "INSERT INTO request_stats_probe_minute") {
		t.Error("探测臂必须写 request_stats_probe_minute（探测分钟聚合表）")
	}
	// ★ 不得把探测流量写进业务表 —— 那正是物理隔离要防的事。
	if strings.Contains(body, "INSERT INTO request_stats_minute ") ||
		strings.Contains(body, "INSERT INTO request_stats_minute\n") {
		t.Error("探测臂不得写业务分钟表 request_stats_minute —— 探测流量必须与业务 KPI 物理隔离")
	}
}

// TestProbeRollupGuardedByLaneSwitch 钉住探测臂受车道开关门控。
//
// ★ 为什么这条重要：车道**关闭**时探测流量仍在业务表里，由业务臂的排除
//
//	谓词处理（bg/probe_policy.go 的 ProbeTrafficExclusionPredicateView）。
//	若探测臂无条件执行 ⇒ 同一批探测流量既被业务臂排除、又被探测臂统计
//	⇒ 两个口径同时存在且不一致。
func TestProbeRollupGuardedByLaneSwitch(t *testing.T) {
	raw, err := os.ReadFile("stats_minute_probe_rollup.go")
	if err != nil {
		t.Fatalf("read stats_minute_probe_rollup.go: %v", err)
	}
	body := string(raw)

	idx := strings.Index(body, "func (w *StatsMinuteRollup) rollupProbe(")
	if idx < 0 {
		t.Fatal("rollupProbe 不存在")
	}
	rest := body[idx:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "probelane.PhysicalEnabled()") {
		t.Error("rollupProbe 必须受 probelane.PhysicalEnabled() 门控\n" +
			"  车道关闭时探测流量在业务表里（由业务臂排除谓词处理）\n" +
			"  ⇒ 无条件执行会让同一批流量出现两个口径")
	}
}
