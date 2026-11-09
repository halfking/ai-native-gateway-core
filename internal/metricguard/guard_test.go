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
	"os"
	"path/filepath"
	"strings"
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
	// R78 订正：原理由写的是「未接采集点」，那是**未核实的假设**——实测下来
	// Service 里其实早就有内存态的租约计数（service.go 的 s.active，Acquire/Release
	// 各维护一次），只是没喂给 gauge。真正的原因是**整包停放**：
	// domains/credentialquota 在生产侧零 importer（db/db.go 的注释与
	// AUDIT_24H_20260817.md B1 明确登记为有意停放，等 dispatch 路径接上再解封）。
	// 该包还用的是 prometheus.NewXxx 而非 promauto，需要显式 Register 才有���
	// 挂到 /metrics，而 Register/MustRegister 同样零调用方——所以它的 4 个指标
	// （含已被记录的那 3 个）目前一个都不在 /metrics 上。这是停放的**症状**，
	// 不是独立缺陷：全仓只有这一个包用「非 promauto + 显式 Register」模式，
	// 其余全部用 promauto 自动注册。
	"metricActive": "整包停放：domains/credentialquota 生产侧零 importer（AUDIT_24H_20260817 " +
		"B1 登记为有意停放，等 dispatch 路径接上再解封）。解封后该 gauge 需接 Service " +
		"已有的内存态 s.active（Acquire/Release 已在维护该计数）",

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

	// ★★★ 读数的归属（★ 第五十轮续记之七 ★）：★★ ★
	//
	//	★ 这道门扫的是【★ 工作树 ★】⇒ ★ 而【★ 同一条命令在两棵树上会给出两个答案 ★】
	//	★ ⇒ ★ ⇒ ★ **⇒ 【★ 所以【★ 这道门【★ 必须能回答「★ 它扫的是哪棵树 ★】★★ ★】
	//	★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 【★ 【★ 红的读数 ★】★★ ★ —— ★ ★ 【★ 违规消息本身 ★】
	//	★ 【★ 无条件带根 ★】（★ 见 FormatViolation ★）★★ ★ ⇒ ★ ⇒ ★
	//	★ **⇒ 【★ 而【★ 【★ 绿的读数 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 【★ 它【★ 不需要归属 ★】★★ ★
	//	★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 归属说明【★ 每次运行也 t.Logf 一次 ★】★★ ★ —— ★ ★
	//	★ 【★ 那在 -v 模式下可见 ★】（★ Go 1.24+ 在非 -v 下【★ 丢弃通过的测试日志 ★】★★ ★】★★ ★**
	t.Logf("%s", ScanRootNote(root))

	res, err := CollectViolations(root, knownUnwired)
	if err != nil {
		t.Fatalf("collect violations: %v", err)
	}

	// 扫描器自身必须不腐化：一个「恒返回空」的守卫等于没有守卫。
	// 这一条取自 sql/schema/integration_gate_test.go 的同类教训——那份守卫
	// 第一版用硬编码清单，仓内变化后它悄悄什么都不匹配了。
	if res.DeclCount < 100 {
		t.Fatalf("只扫到 %d 个 prometheus 指标声明（< 100）：扫描规则可能已失效，"+
			"请检查 prometheusName 是否还能命中真实构造形态", res.DeclCount)
	}
	t.Logf("扫描到 %d 个指标声明", res.DeclCount)

	for _, v := range res.Violations {
		t.Errorf("指标已声明但生产代码从不记录：%s", v)
	}
}

// TestMetricGuardViolationMessageCarriesScanRoot 是本轮（★ 第五十轮续记之七 ★）
// 新增的护栏：★ **违规消息【★ 必须带扫描根 ★】**。
//
// ★ ★★ 为什么【★ 必须有这道护栏 ★】★★ ★：
//
//	★ 消息里带根【★ 是【★ 读数归属 ★】的唯一载体 ★】⇒ ★ ⇒ ★
//	★ **⇒ 【★ 而【★ 将来有人【★ 只改【★ 调用点 ★】、★ 不改【★ 消息构造函数 ★】★★ ★
//	★ ⇒ ★ ⇒ ★ **⇒ 【★ 那条消息【★ 就不带根了 ★】★★ ★ ⇒ ★ ⇒ ★
//	★ **⇒ 【★ 而【★ 那【★ 是个【★ 静默的降级 ★】★★ ★ ⇒ ★ ⇒ ★
//	★ **⇒ 【★ 所以【★ 这里【★ 必须【★ 有牙 ★】★★ ★】★★ ★**
//
// ★ ★★ 而【★ 判别力怎么保证 ★】★★ ★：★ 用【★ 合成树 ★】⇒ ★ 造一个【★ 声明了指标、
// ★ 而生产代码从不记录它 ★】的包 ⇒ ★ ⇒ ★ **⇒ 【★ 判据【★ 必须【★ 在【★ 同一条管线 ★】
// ★ 上判出它 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 判出来的消息【★ 必须含 root ★】★★ ★**】★★ ★
func TestMetricGuardViolationMessageCarriesScanRoot(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// ★ 合成夹具：★ 声明一个 gauge、★ 而全仓【★ 从不记录它 ★】★★ ★
	src := `package fake

import "github.com/prometheus/client_golang/prometheus"

var sleeping = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "fake_sleeping_gauge",
	Help: "A gauge that nobody ever records.",
})
`
	if err := os.WriteFile(filepath.Join(pkg, "metrics.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	res, err := CollectViolations(dir, nil)
	if err != nil {
		t.Fatalf("collect violations: %v", err)
	}
	if len(res.Violations) == 0 {
		t.Fatalf("★ 合成树上没有判出违规 ⇒ ★ 判据面空了 ⇒ ★ 这道门恒绿 ★")
	}
	for _, v := range res.Violations {
		if !strings.Contains(v, dir) {
			t.Fatalf("★ 违规消息里【★ 没有扫描根 ★】%q：\n%s", dir, v)
		}
		if !strings.Contains(v, ScanRootNote(dir)) {
			t.Fatalf("★ 违规消息里【★ 没有完整的扫描根说明 ★】：\n%s", v)
		}
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
