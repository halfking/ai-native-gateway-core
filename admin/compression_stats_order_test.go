package admin

// R78 轮（2026-10-24）：`/api/admin/compression/stats` 的**步骤顺序**守卫。
//
// ★ 为什么要守顺序：生产 252 共享库实测（慢查询日志窗口 4.2h，n=119），
// agg 那条查询的耗时分布是 **p50 ≈ 2.3s / p90 ≈ 21.9s / max ≈ 106s**
// ⇒ 它**经常**吃满端点总预算 ⇒ 排在它后面的 band/bucket **一个都跑不到**
// ⇒ 响应里 hourly_series / token_band 整段缺失。
// ⇒ 而 band/bucket 本身**极便宜**（隔离态实测 40ms / 220ms）
// ⇒ 把它们**前置**，只花掉 ~260ms，agg 仍剩 ~9.7s
// ⇒ **agg 的成功率几乎不变，但必需数据不再被饿死**。
//
// ★ 判据一律断言**真实产物**（compressionStatsSteps 的输出序列），
// 而非源码字面量 ⇒ 否则「换个地方排」就会让门失去牙（R76/R77 轮同款做法）。
//
// ★ 本文件**不动** R77 的三条判据（admin/compression_stats_shape_test.go 一字未改）
// ⇒ R78 只**新增**一条不变量：便宜步必须排在昂贵必需步之前。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// compressionStatsStepIndex 返回步骤名在序列里的下标；找不到返回 -1。
func compressionStatsStepIndex(steps []compressionStatsStep, name string) int {
	for i, s := range steps {
		if s.name == name {
			return i
		}
	}
	return -1
}

// TestR78CompressionStatsCheapStepsMustRunBeforeExpensiveRequired
//
// ★ 核心不变量：**band / bucket 必须排在 agg 之前**。
//
//	① 为什么：agg 的耗时分布（p50 2.3s / p90 21.9s / max 106s）意味着它经常
//	  吃满端点总预算 ⇒ 它后面的步骤**一个都跑不到**；
//	② 为什么 band/bucket 值得前置：它们本身很便宜（40ms / 220ms），
//	  前置只花 ~260ms，agg 仍剩 ~9.7s；
//	③ 为什么不改成「给必需步加独立预算」：那会让必需步**被跳过**
//	  （等于核心数字缺失），与 R77 形状门第②条的立意相反
//	  ⇒ 正确形态是**调顺序**，不是加预算。
func TestR78CompressionStatsCheapStepsMustRunBeforeExpensiveRequired(t *testing.T) {
	steps := (&Handler{}).compressionStatsSteps(compressionStatsProdSQL(), nil, &compressionStatsResult{})
	if len(steps) == 0 {
		t.Fatal("★ 步骤序列是空的")
	}

	aggIdx := compressionStatsStepIndex(steps, "agg")
	bandIdx := compressionStatsStepIndex(steps, "band")
	bucketIdx := compressionStatsStepIndex(steps, "bucket")
	for name, idx := range map[string]int{"agg": aggIdx, "band": bandIdx, "bucket": bucketIdx} {
		if idx < 0 {
			t.Fatalf("★ 步骤序列里找不到 %q，序列=%v", name, compressionStatsStepNames(steps))
		}
	}

	if bandIdx >= aggIdx {
		t.Errorf("★ band（便宜，隔离态 40ms）必须排在 agg（昂贵必需步，p50 2.3s / p90 21.9s）之前，"+
			"否则 agg 吃满预算时 token_band 整段缺失；实际 band=%d agg=%d，序列=%v",
			bandIdx, aggIdx, compressionStatsStepNames(steps))
	}
	if bucketIdx >= aggIdx {
		t.Errorf("★ bucket（便宜，隔离态 220ms）必须排在 agg（昂贵必需步）之前，"+
			"否则 agg 吃满预算时 hourly_series 整段缺失；实际 bucket=%d agg=%d，序列=%v",
			bucketIdx, aggIdx, compressionStatsStepNames(steps))
	}

	// ★ 便宜步**不得**被单独限预算 —— 那会让它们变成「可跳过」，
	//   与 R77 形状门第②条（必需步不得有独立预算上限）同源。
	for _, s := range []compressionStatsStep{steps[bandIdx], steps[bucketIdx]} {
		if s.budget != 0 {
			t.Errorf("★ 便宜步 %q 不得有独立预算上限（实际 %v）—— 它被跳过就等于该段数据缺失",
				s.name, s.budget)
		}
	}
}

// TestR78CompressionStatsSlowAggMustNotStarveCheapSteps（★ 行为腿）
//
// ★ 端点级的真实形状：agg 慢到吃满剩余预算时，
// **先跑的便宜步必须已经拿到数据**，且降级原因必须**只点名 agg**。
//
//	★ 这条腿守的是「响应完整性」：旧顺序下 agg 一慢，
//	  hourly_series / token_band 会**静默整段消失**。
func TestR78CompressionStatsSlowAggMustNotStarveCheapSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var bandRan, bucketRan bool
	steps := []compressionStatsStep{
		{
			name: "band",
			run: func(context.Context) error {
				bandRan = true
				return nil
			},
		},
		{
			name: "bucket",
			run: func(context.Context) error {
				bucketRan = true
				return nil
			},
		},
		{
			// ★ 模拟那条 p90 = 21.9s 的 agg：它会吃满端点剩余预算。
			name: "agg",
			run: func(stepCtx context.Context) error {
				<-stepCtx.Done()
				return stepCtx.Err()
			},
		},
		{
			name:   "estimated-orig",
			budget: compressionStatsOptionalBudget,
			run: func(stepCtx context.Context) error {
				<-stepCtx.Done()
				return stepCtx.Err()
			},
		},
	}

	results := compressionStatsRunSteps(ctx, steps)
	if len(results) != len(steps) {
		t.Fatalf("★ 步骤结果数应等于步骤数，实测 %d", len(results))
	}
	if !bandRan || !bucketRan {
		t.Errorf("★ 便宜步必须先拿到数据（band=%v bucket=%v）—— 否则响应里的 hourly_series / token_band 会整段消失",
			bandRan, bucketRan)
	}

	// agg 失败 ⇒ 端点返回 500（核心数字不可信）；但**便宜步的数据已到手**。
	if results[2].err == nil {
		t.Error("★ 模拟的慢 agg 应当因预算耗尽而失败")
	}
	reason := compressionStatsDegradedReason(results)
	if !strings.Contains(reason, "agg") {
		t.Errorf("★ 降级原因必须点名 agg，实际 %q", reason)
	}
	for _, forbidden := range []string{"band", "bucket"} {
		if strings.Contains(reason, forbidden) {
			t.Errorf("★ 降级原因不应提到 %q（它已经拿到数据），实际 %q", forbidden, reason)
		}
	}
}
