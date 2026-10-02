package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestAutoRouteSettleBaselineRulesCoverEveryRegisteredMetric 钉住
// auto-route-settle-baseline.yml 与 bg/auto_route_settle_baseline_metrics.go +
// bg/auto_route_settle_source.go 的一一对应。
//
// 方向与 s4_scan_skip_test.go 一致：只断言「注册了的指标必须有告警消费」，
// 不反过来断言「没被用的指标该删掉」——后者证伪不了（指标也可以只服务面板）。
func TestAutoRouteSettleBaselineRulesCoverEveryRegisteredMetric(t *testing.T) {
	data, err := os.ReadFile("auto-route-settle-baseline.yml")
	require.NoError(t, err)

	var file struct {
		Groups []struct {
			Name  string `yaml:"name"`
			Rules []struct {
				Alert string            `yaml:"alert"`
				Expr  string            `yaml:"expr"`
				For   string            `yaml:"for"`
				Label map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "auto_route_settle_source_and_baseline", file.Groups[0].Name)

	byAlert := make(map[string]string)
	waits := make(map[string]string)
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.Equal(t, "bg", r.Label["component"], "alert %s must be attributed to component=bg", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		require.NotContains(t, byAlert, r.Alert, "duplicate alert name %s", r.Alert)
		byAlert[r.Alert] = r.Expr
		waits[r.Alert] = r.For
	}
	exprs := strings.Join(mapValues(byAlert), "\n")

	// 三个注册的指标，每个都必须有告警在消费。少一条 = 那个指标是装饰。
	registered := map[string]string{
		"llmgw_autoroute_settle_source_total":           "AutoRouteSettleSourceSwitched",
		"llmgw_autoroute_settle_baseline_cohort_rows":   "AutoRouteSettleBaselineCohortEmpty",
		"llmgw_autoroute_settle_baseline_neutral_total": "AutoRouteSettleBaselineNeutralDominant",
	}
	for metric, alert := range registered {
		require.Contains(t, byAlert, alert, "no alert named %s for metric %s", alert, metric)
		require.Contains(t, exprs, metric,
			"metric %s is registered in bg/ but no rule references it — a metric with no consumer is decoration (§9.37)", metric)
	}
	require.Len(t, byAlert, len(registered), "a rule referencing an unregistered metric was added")

	// 源族切换是**事件**，不能有 for: 抑制——它一生就发生一次，被抑制就等于没有。
	require.Empty(t, waits["AutoRouteSettleSourceSwitched"],
		"a source switch happens once per gate flip; a for: delay would swallow the one occurrence that matters")
	// R33（2026-10-02）：原版 changes(...)>0 对单调计数器 = 「窗口内结算过任意
	// 一条」，每个 sweep 都 firing，事件被噪声淹没（promtool 实证）。判据换成
	// 「同一实例窗口内**两族都有**增量」——settleBatch 每 sweep 只走一族，两族
	// 同涨只出现在横跨切换的窗口。求值语义由
	// rule_tests/auto-route-settle-baseline_test.yml 的 promtool 单测钉住。
	require.Contains(t, byAlert["AutoRouteSettleSourceSwitched"], "count by (job, instance)",
		"the switch alert must count active families per instance; a bare increase()/changes() on "+
			"one family fires on every sweep (always-firing noise, R33)")
	require.Contains(t, byAlert["AutoRouteSettleSourceSwitched"], "increase(llmgw_autoroute_settle_source_total",
		"the switch alert must be window-increase-based (an event), not a level")

	// cohort 归零必须带**活动守卫**。gauge 只在活跃族上 Set，切换后另一族的序列
	// 会停更并冻结在旧值——对一个「已停更」的序列断言 == 0，读到的是「没在测」，
	// 不是「测出来是 0」。没有这条守卫，这条告警会在每次切换后误报。
	require.Contains(t, byAlert["AutoRouteSettleBaselineCohortEmpty"],
		"increase(llmgw_autoroute_settle_source_total",
		"the empty-cohort alert must require that this family is still being used; "+
			"a stale gauge series would otherwise read 0 forever after a switch")
	require.Contains(t, byAlert["AutoRouteSettleBaselineCohortEmpty"],
		"llmgw_autoroute_settle_baseline_cohort_rows == 0",
		"the empty-cohort alert must fire on the cohort gauge being 0")

	// 中性回落是**比率**告警：cohort 可能整体非空但某个 task_type 分组为空，
	// 只有按 term 的回落率会动。断言它确实是个比值，而不是绝对值阈值。
	require.Contains(t, byAlert["AutoRouteSettleBaselineNeutralDominant"], "0.5 *",
		"the neutral-fallback alert must compare against the settled total (a ratio); "+
			"an absolute threshold would miss the partial case where the cohort is non-empty "+
			"but one task_type group is missing")
	require.Contains(t, byAlert["AutoRouteSettleBaselineNeutralDominant"], "sum by (term",
		"latency and cost fall back independently, so they must stay separable in the alert")
	// R33（2026-10-02）：原版两侧标签集 {term,family} vs {family} 不配对，比较
	// 结果恒空（永不触发的死告警，promtool 实证）。跨标签集比较必须显式 on()。
	require.Contains(t, byAlert["AutoRouteSettleBaselineNeutralDominant"], "on(family)",
		"LHS is {term,family}, RHS is {family}; without explicit on(family) the comparison "+
			"never pairs any series and the alert can never fire (dead alert, R33)")

	// GW-00 低基数守卫。cohort_rows 刻意不带 task_type（它来自请求内容，基数
	// 无上界），所以 expr 里出现 task_type 过滤就是把无界维度引进告警标签。
	require.NotContains(t, exprs, "task_type=", "must not filter by task_type (unbounded cardinality)")
	require.NotContains(t, exprs, "model=", "must not filter by model")
	require.NotContains(t, exprs, "tenant=", "must not filter by tenant")
	require.NotContains(t, exprs, "request_id=", "must not filter by request_id")
	text := string(data)
	require.False(t, strings.Contains(text, `task_type="`))
	require.False(t, strings.Contains(text, `model="`))
}

// TestAutoRouteSettleBaselineAlertDocumentsTheRealMeasurement 把告警文案里
// 引用的数字钉在审计记录上。
//
// 这些数字是**本机实测**，且其中一条（p95 偏移 15.2%）取自本地探针流量而**不是**
// 真实用户流量。告警文案是运维在半夜读到的第一手材料，如果它把一个合成流量上
// 测出的数字说成生产结论，那就是在错误的层面引导排查。这道门要求文案自己声明
// 了这个限定。
func TestAutoRouteSettleBaselineAlertDocumentsTheRealMeasurement(t *testing.T) {
	data, err := os.ReadFile("auto-route-settle-baseline.yml")
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "不代表生产",
		"the p95 shift was measured on local probe traffic, not real user traffic; "+
			"the annotation must say so, or it misleads whoever reads the alert at 3am")
	require.Contains(t, text, "659252 / 820336",
		"the 80.4% session-side coverage ratio should be stated with its raw counts "+
			"so a reader can tell which surface was measured")
}

// mapValues returns the values of m in unspecified order. Used only to build a
// searchable blob of every expr.
func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
