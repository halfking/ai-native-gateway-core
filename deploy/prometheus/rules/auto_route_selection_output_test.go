package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// auto-route selection 产出侧告警（审计 §9.57）的门。
//
// §9.56 的实测结论驱动了这道门：到那时为止，告警**全部建在读侧**，
// 产出侧「selection 写入量归零」无门，而 252 上该状态已持续两周半而无人知情。
// `llm_gateway_auto_selections_total` 有生产者、有值、**无消费者**——
// 与 §9.37「没有告警读的指标是装饰」同源。
func TestAutoRouteSelectionOutputRulesCoverBothMetrics(t *testing.T) {
	data, err := os.ReadFile("auto-route-selection-output.yml")
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
	require.Equal(t, "auto_route_selection_output", file.Groups[0].Name)

	byAlert := make(map[string]string)
	for _, r := range file.Groups[0].Rules {
		require.NotEmpty(t, r.Alert, "a rule without an alert name never fires")
		require.NotEmpty(t, r.Expr, "alert %s has an empty expr", r.Alert)
		require.NotEmpty(t, r.For, "alert %s has no for: window", r.Alert)
		require.NotEmpty(t, r.Label["severity"], "alert %s has no severity", r.Alert)
		require.NotContains(t, byAlert, r.Alert, "duplicate alert name %s", r.Alert)
		byAlert[r.Alert] = r.Expr
	}
	require.Len(t, byAlert, 2)

	// selection_metrics.go 注册了两个指标；两个都必须有告警消费。
	// `dropped_total` 的文件注释写着「worth alerting on rather than merely
	// graphing」——注释里声称的保护如果没有门兑现，那条注释就是装饰。
	for _, metric := range []string{
		"llm_gateway_auto_selections_total",
		"llm_gateway_auto_selections_dropped_total",
	} {
		consumed := false
		for _, expr := range byAlert {
			if strings.Contains(expr, metric) {
				consumed = true
				break
			}
		}
		require.True(t, consumed,
			"metric %s is registered in domains/hooks/observability/telemetry/selection_metrics.go "+
				"but no rule references it — it has a producer and a value and no consumer, "+
				"which is §9.37's decoration in mirror form", metric)
	}
}

// TestAutoRouteSelectionNotProducedHandlesTheCounterVecTrap 门住那道
// **「把空序列变成 0」的机制**。
//
// 这是本组告警最容易写坏、且坏了之后**完全看不出来**的一环：
// `llm_gateway_auto_selections_total` 是 CounterVec，首次 Inc() 之前
// Prometheus 不导出任何序列 ⇒ `sum(increase(...))` 得到**空向量**而不是 0；
// 空向量 `== 0` 仍是空向量 ⇒ **告警永远不响**，而那恰好是它唯一要抓的场景。
//
// 判据只断言**能被证明的那一侧**：表达式里必须存在那一项。
// 它**不**证明 PromQL 语义正确——那由 promtool 场景 A 证明
// （`rule_tests/auto-route-selection-output_test.yml`，真跑 promtool）。
// 两道门分工：Go 侧廉价地挡住「那一项被删掉」，promtool 侧证明「有它就对」。
func TestAutoRouteSelectionNotProducedHandlesTheCounterVecTrap(t *testing.T) {
	data, err := os.ReadFile("auto-route-selection-output.yml")
	require.NoError(t, err)

	var file struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(data, &file))

	var expr string
	for _, r := range file.Groups[0].Rules {
		if r.Alert == "AutoRouteSelectionsNotProduced" {
			expr = r.Expr
		}
	}
	require.NotEmpty(t, expr, "AutoRouteSelectionsNotProduced is missing")

	require.Contains(t, expr, "llm_gateway_auto_selections_total",
		"the alert must read the persisted-selection counter, not the dropped one")
	// 这里断言的是**机制**（把「空」变成 0 的那一项），不是某个字面串。
	//
	// 第一版门要求出现字面的 `or vector(0)`。第二轮把表达式改成
	// `or (0 * max by (job, instance) (up{...}))` 之后门红了——但那一项
	// 起的是**同一个作用**（无序列的实例得到 0），而且额外保住了 job/instance
	// 标签（`or vector(0)` 会把它们抹掉，promtool 场景 A 抓到了）。
	// ⇒ 门若钉死字面串，就会在一次**修复**面前误报。
	require.Regexp(t, `or\s*\(?\s*0\s*\*`,
		expr,
		"AutoRouteSelectionsNotProduced MUST keep a term that turns \"no series\" into 0. "+
			"llm_gateway_auto_selections_total is a CounterVec: before its first Inc() Prometheus "+
			"exports NO series, so sum(increase(...)) is an EMPTY vector, `empty == 0` is still "+
			"empty, and the alert can never fire — which is the exact state it exists to catch "+
			"(§9.56). Currently expressed as `or (0 * max by (job, instance) (up{...}))`, which "+
			"keeps the job/instance labels that a bare `or vector(0)` would erase. "+
			"**This Go gate only proves the term is present; the evaluation semantics are "+
			"proved by promtool scenario A in rule_tests/auto-route-selection-output_test.yml.**")
	require.Regexp(t, `sum by \(job, instance\)`,
		expr,
		"the sum must aggregate BY (job, instance) — aggregating by instance alone "+
			"silently drops the cluster label (found by promtool scenario A, invisible to "+
			"promtool check rules because the syntax is valid)")

	require.Contains(t, expr, "increase(llm_gateway_auto_selections_total[2h])",
		"the window must be on the counter itself; querying the raw value would re-arm "+
			"itself after a process restart and never notice a second outage")

	// 进程存活守卫：没有它，告警会在「网关根本没在跑」时误报成
	// 「auto-route 不产出」——两种完全不同的故障共用一条告警。
	require.Contains(t, expr, "and on(job, instance)",
		"the liveness guard must use a set operator on (job, instance) — the pattern "+
			"already used in role-fallback-mainstream.yml. Without one the label sets never "+
			"match; with a *narrower* one the alert silently loses cluster attribution "+
			"(promtool scenario A caught exactly that: by (instance) dropped job)")
	// 同样只断言**机制**：up 序列被引用、且与 1 比较。
	// 写成字面 `up{job=~"..."} == 1` 会在表达式加上 `max by (...)` 包装时误报——
	// 那次包装恰恰是为了保住 job/instance 标签（promtool 场景 A 抓到的缺陷）。
	require.Regexp(t, `up\{job=~"llm-gateway\.\*"\}[^)]*\)\s*==\s*1`,
		expr,
		"the alert must be gated on the gateway actually being up and scraped — "+
			"`up{job=~\"llm-gateway.*\"}` is this repo's established gateway-liveness form "+
			"(see alerts.yml). Without it a dead gateway reports as \"auto-route stopped\"")
}

// TestAutoRouteSelectionOutputStatesItsKnownLimits 门住「如实登记局限」。
//
// 这组告警有两个真实的覆盖缺口：① 不用 auto 路由的部署会**永久**报红；
// ② 252 上没有 Prometheus ⇒ **完全不生效**。两条都写进了 yml 的注释。
//
// 判据不要求文案的措辞，只要求**局限被写下来**：一段不声明边界的告警，
// 会让值班的人以为它覆盖了它没覆盖的东西。
func TestAutoRouteSelectionOutputStatesItsKnownLimits(t *testing.T) {
	data, err := os.ReadFile("auto-route-selection-output.yml")
	require.NoError(t, err)
	text := string(data)

	require.Contains(t, text, "已知局限",
		"the rule file must carry a known-limits section — these alerts have two real "+
			"coverage gaps and an alert that does not say so sends the reader down the "+
			"wrong path")
	require.Contains(t, text, "252",
		"the limits section must name 252, where §9.56 measured that no Prometheus is "+
			"deployed at all — these alerts cannot fire there, and that must be stated")
	require.Contains(t, text, "假设该部署在用 auto-route",
		"the permanent-false-positive case (a deployment that legitimately has no "+
			"auto traffic) must be named, otherwise the alert reads as unconditional")
}
