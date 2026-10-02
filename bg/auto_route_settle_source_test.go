package bg

import (
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// settleBatch 数据源切换的门（审计 §9.43）。
//
// # 这道门在防什么
//
// §9.43 把三条腿（outcome join / LATERAL / loadTaskBaselines）接到同一个
// `settleSourceSpec` 上，按 S4 写门在 `request_logs_hot` 与 `session_turns_hot`
// 之间切换。三个具体风险：
//
//  1. **三条腿不同源**。outcome 读会话族而 baseline 读 v1（或反过来）时，
//     p95/p75 基线与被它归一化的 latency 不在同一批行上算——那比缺数据更隐蔽，
//     因为数字都"有值"。
//  2. **默认值方向搞反**。`settings.GetPlatformBool` 在存储未初始化时默认 true
//     （写门=开着）。若代码默认走会话族，就会在**任何**配置下悄悄改源。
//  3. **切换不可见**。运维需要能在 /metrics 上立刻看到源从 v1 变成 session。

// TestSettleSourceForDefaultsToV1 钉住 2：默认必须是 v1（与今天逐字相同）。
func TestSettleSourceForDefaultsToV1(t *testing.T) {
	src := settleSourceFor(true)
	if src.Family != settleFamilyV1 {
		t.Errorf("写门开着时 family = %q，期望 %q", src.Family, settleFamilyV1)
	}
	if src.TurnsTable != "request_logs_hot" {
		t.Errorf("写门开着时 TurnsTable = %q，期望 request_logs_hot（与移植前逐字相同）", src.TurnsTable)
	}
	if src.SessionKeyCol != "gw_session_id" {
		t.Errorf("写门开着时 SessionKeyCol = %q，期望 gw_session_id", src.SessionKeyCol)
	}
}

// TestSettleSourceForSessionSide 钉住停写后的目标族与会话键列名。
func TestSettleSourceForSessionSide(t *testing.T) {
	src := settleSourceFor(false)
	if src.Family != settleFamilySession {
		t.Errorf("写门关闭时 family = %q，期望 %q", src.Family, settleFamilySession)
	}
	if src.TurnsTable != "session_turns_hot" {
		t.Errorf("写门关闭时 TurnsTable = %q，期望 session_turns_hot", src.TurnsTable)
	}
	// v1 叫 gw_session_id，会话族叫 session_id。写错会得到恒不匹配的 LATERAL
	// ——而那不会报错，只会让 retry 信号变成 unavailable（§9.42 的三态能看见，
	// 但根因在这里就该挡住）。
	if src.SessionKeyCol != "session_id" {
		t.Errorf("写门关闭时 SessionKeyCol = %q，期望 session_id", src.SessionKeyCol)
	}
}

// TestSettleLegsAllUseTheSameSource 钉住 1：三条腿必须共用同一个规格。
//
// 判据是「SQL 里出现的是 src.TurnsTable 而不是字面量表名」。字面量表名出现在
// 任何一条腿上，就说明有人绕过规格直接写了表名 —— 那正是三条腿不同源的起点。
//
// §9.44：判据的文件范围改由 settleSQLFiles 给出（SQL 已搬进
// auto_route_settle_sql.go）。若这里继续只扫 worker 文件，搬动之后这道门会
// 立刻变红——那是**正确**的信号（它数的是"三处都用规格"），而同一批里那道
// 扫 710 视图的门当时是绿的，因为它对着空文件断言。两者行为不一致本身就是
// 「判据钉在文件位置上」的证据。
func TestSettleLegsAllUseTheSameSource(t *testing.T) {
	raw := settleSQLSourceText(t)
	sql := settleSQLLiteralText(t)

	// 硬编码的表名（作为 SQL 字面量文本出现）= 绕过规格。
	for _, banned := range []string{"FROM request_logs_hot r2", "JOIN request_logs_hot rl"} {
		if strings.Contains(sql, banned) {
			t.Errorf("SQL 里出现了硬编码表名 %q。三条腿必须共用 settleSourceSpec，\n"+
				"  否则会出现「outcome 读会话族、baseline 读 v1」——基线与被归一化的\n"+
				"  latency 不在同一批行上算，数字都有值但毫无意义。", banned)
		}
	}
	// 三处都必须出现：outcome join、LATERAL、baseline。
	// outcome join 与 LATERAL 各一处 TurnsTable，baseline 一处，共 3。
	if n := strings.Count(raw, "src.TurnsTable"); n < 3 {
		t.Errorf("src.TurnsTable 只出现 %d 次，期望至少 3 次"+
			"（outcome join / LATERAL / loadTaskBaselines 各一处）", n)
	}
	// LATERAL 的会话键必须也来自规格。
	if !strings.Contains(raw, "src.SessionKeyCol") {
		t.Error("LATERAL 没有用 src.SessionKeyCol —— 会话键列名必须随族切换")
	}
}

// TestSettleWorkerDelegatesToTheSQLBuilders 钉住 §9.44 的「唯一定义」。
//
// 集成测试 TestAutoRouteSettleSessionSourceMatchesV1OnIdenticalRows 跑的是
// settleBaselinesSQL / settlePendingSQL 的返回值。若 worker 之后又把 SQL 内联
// 回自己体内，那道集成测试就变成在验证一份**没人用的副本**——它照样绿，而线上
// 跑的是另一段文本。这是「测试守住的是副本」的标准形态。
//
// 可证的一侧只有「worker 不再持有 SQL 字面量」；反过来（「builder 存在就不许
// 有人写别的」）证伪不了，所以只断言前者。
func TestSettleWorkerDelegatesToTheSQLBuilders(t *testing.T) {
	workerLits := strings.Join(sqlLiteralsOfSettleFile(t, "auto_route_settle_worker.go"), "\n")
	for _, banned := range []string{
		"LEFT JOIN LATERAL",
		"percentile_cont(0.95)",
		"GROUP BY task_type",
	} {
		if strings.Contains(workerLits, banned) {
			t.Errorf("auto_route_settle_worker.go 里又出现了 SQL 形状 %q。\n"+
				"  §9.44 已把两条查询抽成 settleBaselinesSQL / settlePendingSQL；\n"+
				"  worker 必须调用它们，否则集成测试验证的是一份没人用的副本。", banned)
		}
	}
	// 正向：worker 确实在调用这两个构造器。
	raw := readFileForSettleGate(t, "auto_route_settle_worker.go")
	for _, want := range []string{"settlePendingSQL(src)", "settleBaselinesSQL(src)"} {
		if !strings.Contains(raw, want) {
			t.Errorf("auto_route_settle_worker.go 没有调用 %s —— 两条腿必须走同一个 SQL 构造器", want)
		}
	}
}

// TestSettleBaselineCohortCountIsConsumed 钉住 §9.37：新返回值不许是装饰。
//
// loadTaskBaselines 的第二个返回值（cohort 行数）是 §9.44 新加的。若没人读它，
// 它在事实层面就是装饰——而且是最坏的一种：函数签名让人以为 cohort 大小被
// 监控了，实际只有告警文案在说这件事。
func TestSettleBaselineCohortCountIsConsumed(t *testing.T) {
	raw := readFileForSettleGate(t, "auto_route_settle_worker.go")

	// 刻意**不**先写一条 `strings.Contains(raw, "autoRouteSettleBaselineCohortRows")`。
	// 变异验证时它被判为通过——而命中的是 loadTaskBaselines 文档注释里的那句
	// 「see autoRouteSettleBaselineCohortRows」，不是任何代码。子串判据被注释喂饱
	// 是本审计最常见的假阳性面，所以这里只留下面这条要求完整调用形状的断言：
	// 它同时钉住标识符、WithLabelValues 与被 Set 的那个值，注释喂不饱它。
	if !regexp.MustCompile(`autoRouteSettleBaselineCohortRows\s*\n?\s*\.WithLabelValues\([^)]*\)\s*\n?\s*\.Set\(float64\(cohortRows\)\)`).
		MatchString(raw) {
		t.Error("autoRouteSettleBaselineCohortRows 没有被 Set 成 loadTaskBaselines 返回的 cohortRows —— " +
			"loadTaskBaselines 的 cohort 行数返回值无人消费，指标与它声称测量的量脱钩了（§9.37：没有门/告警读的字段是装饰）")
	}
	// 反向：那个返回值也不能在别处被丢在一个没人读的变量上。上面的正则已经要求
	// cohortRows 出现在 Set 的实参里，这里补一条「sweep 必须真的接住了第二个返回值」。
	if !strings.Contains(raw, "baselines, cohortRows, err := w.loadTaskBaselines(sweepCtx)") {
		t.Error("sweep 没有接住 loadTaskBaselines 的第二个返回值 —— " +
			"签名与调用点脱节，cohort 大小实际上无人测量")
	}
	// 空 cohort 必须在日志里说一次。空 map 不是 error，原有的 err != nil 分支
	// 永远不会为它触发（这正是 §9.44 的发现）。
	if !strings.Contains(raw, "baselines EMPTY") {
		t.Error("worker 里没有针对空 baseline map 的告警日志 —— " +
			"空 map 不是 error，原有分支不会触发，切换源族后会静默把每条 reward 的延迟/成本项塌成 0.5")
	}
}

// TestSettleSourceMetricIsRegistered 钉住 3：切换在 /metrics 上可见。
func TestSettleSourceMetricIsRegistered(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found *dto.MetricFamily
	for _, mf := range families {
		if mf.GetName() == "llmgw_autoroute_settle_source_total" {
			found = mf
			break
		}
	}
	if found == nil {
		t.Fatal("/metrics 里没有 llmgw_autoroute_settle_source_total —— " +
			"源族切换将不可见，运维只能在「settle 变慢了」之后才发现")
	}
	got := map[string]bool{}
	for _, m := range found.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "family" {
				got[lp.GetValue()] = true
			}
		}
	}
	for _, f := range []string{settleFamilyV1, settleFamilySession} {
		if !got[f] {
			t.Errorf("指标里缺少 family=%q 序列（预初始化没做，切换前会缺一条）", f)
		}
	}
	if len(got) != 2 {
		t.Errorf("family 标签值集合 = %v，期望恰好两个闭集值", got)
	}
}
