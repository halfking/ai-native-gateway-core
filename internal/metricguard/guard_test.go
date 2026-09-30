package metricguard

// R77 守卫：没有任何 prometheus 指标能处于「已声明但生产代码从不记录」的状态。
//
// 为什么值得设这道门：这类缺陷不表现为崩溃或错值，而表现为**面板与告警看起来
// 存在、实际永远没有数据点**。/metrics 上该 series 恒为 ABSENT（不是 0），
// rate() 告警永不触发，看板显示"无数据"也没人会去翻代码。R77 扫出 295 个指标
// 声明中 16 个如此，其中 domains/session/v2/README.md 明确承诺"启用后可通过
// Prometheus 监控"4 个 sessions_v2_* 指标——而那 6 个全仓每个标识符只有 1 处
// 引用，就是它自己的声明。
//
// 判据是**记录调用**，不是标识符引用：Go 不允许包级变量未被引用，所以
// "有没有被引用"恒为真，问不出任何东西（见 scan.go 的注释）。
//
// 变异验证（本次已做，见 48 号报告）：把任意一个记录调用删掉 ⇒ 本门转红。

import (
	"path/filepath"
	"sort"
	"testing"
)

// knownUnwired 是**已确认存在、但本轮不接线**的指标，逐条写明原因。
//
// 白名单不是免责条款：每一条都要求写清楚"为什么不接"，没有理由的条目一律
// 让门红——否则白名单会变成"新增沉睡指标的默认去处"，那正是本门要消灭的
// 行为模式。接线一个就从这里删一条，并在提交信息里说明。
var knownUnwired = map[string]string{
	// ── sessions_v2 剩余 3 个（写侧 3 个已在本轮接线，见 domains/session/v2/turn_writer.go）
	"SessionsV2CacheHit": "分层缓存命中已有更细粒度的实现（monitoring.StorageMetrics 的 " +
		"RecordL1Hit/RecordL15HitForMode/RecordL2Hit，走 storage 快照端点），而该文件头明写" +
		"「刻意不接入 prometheus registry」；是否再加一个粗粒度的 sessions_v2_cache_* 进 " +
		"Prometheus 属可观测性设计取舍，不在本轮擅自动",
	"SessionsV2CacheMiss": "同 SessionsV2CacheHit：细粒度实现已存在且刻意留在 Prometheus 之外，" +
		"是否补粗粒度聚合需产品确认口径",
	"SessionsV2DualReadDiff": "它要度量的功能尚未实现——domains/session/dual_writer.go 的 " +
		"Write 文档承诺第 3 步「IF v2_dual_read enabled: validate consistency」，但 v2_dual_read " +
		"全仓只出现在那一行注释里，函数没有第 3 步。先实现双读比对再接线，否则接通也是恒 0",

	// ── omnifree：免费 token 池的扫描/注册链路尚未接入生产调度，
	// 三个指标随该链路一起待接线（objective「免费的token资源自动扫描、注册、聚合使用」域）。
	"OmniFreePoolDedupModelsTotal":     "omnifree 池扫描/注册链路未接入生产调度，指标随该链路一起接线",
	"OmniFreePoolDedupPoolsTotal":      "omnifree 池扫描/注册链路未接入生产调度，指标随该链路一起接线",
	"OmniFreePreflightRejectionsTotal": "omnifree 池扫描/注册链路未接入生产调度，指标随该链路一起接线",

	// ── routing health 自愈：自动恢复分支尚未实现，指标先行声明。
	"RoutingHealthAutoRecoverTotal":               "凭据健康自愈的自动恢复分支未实现，指标先行声明待接线",
	"RoutingHealthAutoRecoverTickDurationSeconds": "凭据健康自愈的自动恢复分支未实现，指标先行声明待接线",

	// ── autoroute affinity churn：rank 变动统计待接入 settle worker 的变更检测点。
	"autoRouteAffinityRankChurn": "affinity rank 变动点尚未接入 settle worker 的变更检测",

	// ── request journey recorder：recorder 写入计数待接。
	"journeyRecorderWrittenTotal": "request journey recorder 的写入点尚未接计数",

	// ── credential quota：活跃租约 gauge 待接。
	"metricActive": "credential_client_quota 的活跃租约 gauge 尚未接入采集点",

	// ── dispatch stats drop：统计丢弃计数待接。
	"metricStatsDrop": "dispatch 统计丢弃的计数点尚未接入",

	// ── session inspector：回收计数待接。
	"sessionInspectorRecycleTotal": "session inspector 的回收计数点尚未接入",
}

func TestNoPrometheusMetricIsDeclaredButNeverRecorded(t *testing.T) {
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := repoRootFrom(wd)

	decls, err := CollectDecls(root)
	if err != nil {
		t.Fatalf("collect declarations: %v", err)
	}

	// 扫描器自身必须不腐化：一个「恒返回空」的守卫等于没有守卫。
	// 这一条取自 sql/schema/integration_gate_test.go 的同类教训——那份守卫
	// 第一版用硬编码清单，仓内变化后它悄悄什么都不匹配了。
	if len(decls) < 100 {
		t.Fatalf("只扫到 %d 个 prometheus 指标声明（< 100）：扫描规则可能已失效，"+
			"请检查 prometheusName 是否还能命中真实构造形态", len(decls))
	}
	t.Logf("扫描到 %d 个指标声明", len(decls))

	never, err := NeverRecorded(root, decls)
	if err != nil {
		t.Fatalf("scan recording calls: %v", err)
	}

	var violations []string
	for _, d := range never {
		if reason, ok := knownUnwired[d.Ident]; ok {
			_ = reason // 白名单条目，见下方「白名单不得无理由」子测试
			continue
		}
		violations = append(violations, d.Ident+"  ("+d.Metric+")  声明于 "+d.File)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("指标已声明但生产代码从不记录：%s\n"+
			"  它会出现在 /metrics 上却恒为 ABSENT（不是 0），面板无数据、rate() 告警永不触发。\n"+
			"  要么接线一个记录调用，要么在 knownUnwired 里写明不接线的原因。", v)
	}
}

// TestKnownUnwiredEntriesHaveReasons 防止白名单退化成「沉睡指标的默认去处」。
func TestKnownUnwiredEntriesHaveReasons(t *testing.T) {
	for ident, reason := range knownUnwired {
		if len(reason) < 12 {
			t.Errorf("knownUnwired[%s] 的理由过短（%q）：白名单必须写清为什么不接线，"+
				"否则它会变成新增沉睡指标的默认去处", ident, reason)
		}
	}
}

// TestKnownUnwiredHasNoStaleEntries 防止白名单条目在接线后被遗忘。
//
// 一个已被接线的指标留在白名单里，等于门对它不再有任何约束——
// 下一个人删掉那个记录调用，门依然是绿的。
func TestKnownUnwiredHasNoStaleEntries(t *testing.T) {
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := repoRootFrom(wd)
	decls, err := CollectDecls(root)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.Ident] = true
	}
	never, err := NeverRecorded(root, decls)
	if err != nil {
		t.Fatal(err)
	}
	stillNever := map[string]bool{}
	for _, d := range never {
		stillNever[d.Ident] = true
	}
	for ident := range knownUnwired {
		switch {
		case !declared[ident]:
			t.Errorf("knownUnwired[%s]：仓内已无此指标声明，条目已失效，请删除", ident)
		case !stillNever[ident]:
			t.Errorf("knownUnwired[%s]：该指标现在**已经有**记录调用了，请从白名单删除。\n"+
				"  留着会让门对它失去约束——下一个人删掉那个记录调用，门仍然是绿的", ident)
		}
	}
}
