package rules_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type roleFallbackRuleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert string `yaml:"alert"`
			Expr  string `yaml:"expr"`
			For   string `yaml:"for"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func loadRoleFallbackRules(t *testing.T) map[string]struct {
	Expr string
	For  string
} {
	t.Helper()

	data := readRoleFallbackYAML(t)

	var file roleFallbackRuleFile
	require.NoError(t, yaml.Unmarshal([]byte(data), &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "autoroute_role_fallback", file.Groups[0].Name)

	byAlert := make(map[string]struct {
		Expr string
		For  string
	})
	for _, rule := range file.Groups[0].Rules {
		byAlert[rule.Alert] = struct {
			Expr string
			For  string
		}{Expr: rule.Expr, For: rule.For}
	}
	return byAlert
}

func readRoleFallbackYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("role-fallback-mainstream.yml")
	require.NoError(t, err)
	return string(data)
}

// requireSetOperatorMatchesOnEmptyLabels 钉住 set operator 的匹配口径。
//
// 它防的是一个**纯静默**的失效：PromQL 的 and 默认按"除 metric name 外的
// 全部标签"匹配。左侧若带 scrape 注入的 job/instance、右侧 sum() 无标签，
// 两侧标签集不相等 → and 产出**空向量** → 告警永不触发，而 Prometheus
// 不报任何错。
//
// promtool 四变体对照实测（deploy/prometheus/rule_tests/，输入带 job/instance）：
//
//	裸 and + 未聚合 gauge        → 不触发  ← 静默失效
//	裸 and + max() 折叠          → 触发
//	and on() + 未聚合 gauge      → 触发
//	and on() + max() 折叠（现行）→ 触发
//
// 即 max() 与 and on() **各自都能**独立解决。本门要求两者同时存在，是为了
// 任一被后人"顺手简化"掉时仍然安全——只留一个也正确，但那时必须重跑
// promtool 单元测试确认。
//
// 断言方式是"把 and on() 全部抹掉后，剩余文本里不得再有裸 and"，而不是
// "表达式里含有 and on()"——后者太弱：主流层告警有**两个** set operator，
// 只退化其中一个时 Contains 仍然成立（变异 M2 实测踩中：门绿，变异存活）。
// 逐个 set operator 承重的门必须用"全部剥离后无残留"的口径。
func requireSetOperatorMatchesOnEmptyLabels(t *testing.T, expr string) {
	t.Helper()

	require.Contains(t, expr, "and on()",
		"set operator 必须写成 and on()：裸 and 按全部标签匹配，未聚合 gauge 带 job/instance 而 sum() 无标签，标签集不相等会让告警产出空向量、静默失效")

	stripped := strings.ReplaceAll(expr, "and on()", "")
	bare := regexp.MustCompile(`\band\b`).FindAllString(stripped, -1)
	require.Empty(t, bare,
		"存在未加 on() 的裸 and：默认按全部标签匹配，会让告警产出空向量并静默失效")
}

// TestRoleFallbackMainstreamSpikeRuleUsesRegisteredMetric 钉住本规则文件的
// 承重性质。任何一条被删掉，这条告警都会在真实故障时保持沉默，而 YAML
// 本身仍然合法、Prometheus 也不会报错。
func TestRoleFallbackMainstreamSpikeRuleUsesRegisteredMetric(t *testing.T) {
	byAlert := loadRoleFallbackRules(t)

	contract, ok := byAlert["AutoRouteRoleFallbackMainstreamSpike"]
	require.True(t, ok, "缺 AutoRouteRoleFallbackMainstreamSpike 告警")
	expr := contract.Expr

	// 1. 必须引用 Go 侧已注册的 counter（名字与 autoroute/metrics.go 一致）。
	require.Contains(t, expr, "llmgw_autoroute_role_fallback_layer_total")
	require.Contains(t, expr, `layer="mainstream"`)

	// 2. 必须显式要求 role_routing_active == 1。
	//
	// 删掉它不会让 YAML 解析失败，只会让告警在父开关关闭时静默失效：
	// 分子分母同为 0 → 0/0 = NaN，而 NaN 与任何阈值比较恒为 false。
	// 运维看到"没告警"会读成"轻量池健康"，真相比"兜底机制压根没装载"
	// 更危险。反过来，这条 guard 在机制开启时恒真，零误报成本。
	require.Contains(t, expr, "llmgw_autoroute_role_routing_active) == 1",
		"主流层告警必须被 role_routing_active 门住，否则 flag-off 时 0/0=NaN 会让它恒不触发")

	// 3. gauge 侧必须被 max() 折叠掉 job/instance，否则与 sum() 侧无法匹配。
	require.Contains(t, expr, "max(llmgw_autoroute_role_routing_active)",
		"gauge 侧必须用 max() 折叠 scrape 标签")
	requireSetOperatorMatchesOnEmptyLabels(t, expr)

	// 4. 分母必须含 kind 层（role 路由实际介入的请求数）。
	//
	// 若分母误用全量 auto 请求数，main 会话等从不进 role 路由的流量会
	// 稀释比值，真实的整层掉线永远达不到阈值。
	require.Contains(t, expr, `layer="kind"`,
		"分母必须是 kind+mainstream（role 路由介入的请求数），不能用全量 auto 请求数")

	// 5. 样本量下界：单次 mainstream 事件除以单次 kind 事件 = 100% 的假阳性。
	require.Contains(t, expr, ">= 5",
		"缺样本量下界时，1 次主流层事件会算出 100% 占比并误报")
	require.NotEqual(t, "", contract.For)

	// 6. 高基数标签一律禁止（与仓库既有 routing 规则同款约束）。
	require.NotContains(t, expr, "model=")
	require.NotContains(t, expr, "tenant=")
	require.NotContains(t, expr, "request_id")

	text := readRoleFallbackYAML(t)
	require.False(t, strings.Contains(text, "model="))
	require.False(t, strings.Contains(text, "tenant="))
}

// TestRoleFallbackDisabledAlertDistinguishesFlagOff 钉住"机制没开"的可见性。
//
// 这条告警的唯一职责是让「我以为兜底在保护我」变成可核对的事实。没有它，
// 灰度决策（handoff §6 第 2 条）没有任何数据支撑。
func TestRoleFallbackDisabledAlertDistinguishesFlagOff(t *testing.T) {
	byAlert := loadRoleFallbackRules(t)

	contract, ok := byAlert["AutoRouteRoleRoutingDisabled"]
	require.True(t, ok, "缺 AutoRouteRoleRoutingDisabled 可见性告警")
	require.Contains(t, contract.Expr, "llmgw_autoroute_role_routing_active == 0")
	require.NotEqual(t, "", contract.For)

	// 这条没有 set operator（单条比较按实例各自求值），因此不需要 and on()。
	require.NotContains(t, contract.Expr, "and on()")

	// severity=info 不是 warning：父开关默认关闭，既有部署上这条会长期
	// firing，那是预期的灰度状态而不是故障。若升成 warning，运维会开始
	// 无视它，反而丢掉"兜底是否装载"这个信息。
	require.Contains(t, readRoleFallbackYAML(t), "severity: info")
}

// TestRoleFallbackNeverExercisedAlertUsesKindVolumeGate 钉住"兜底从未验证"。
//
// 阈值语义是「role 路由在跑但兜底层一次没命中」，所以 kind 侧必须有量下界：
// 否则一个刚开开关的部署（kind=0、mainstream=0）会立刻因为
// "mainstream == 0" 而误报。
func TestRoleFallbackNeverExercisedAlertUsesKindVolumeGate(t *testing.T) {
	byAlert := loadRoleFallbackRules(t)

	contract, ok := byAlert["AutoRouteRoleFallbackNeverExercised"]
	require.True(t, ok, "缺 AutoRouteRoleFallbackNeverExercised 告警")
	expr := contract.Expr

	require.Contains(t, expr, "llmgw_autoroute_role_routing_active) == 1")
	require.Contains(t, expr, "max(llmgw_autoroute_role_routing_active)")
	requireSetOperatorMatchesOnEmptyLabels(t, expr)
	require.Contains(t, expr, `layer="mainstream"`)
	require.Contains(t, expr, `layer="kind"`)

	// kind 侧量下界（100）是这条告警的鉴别门：删掉它，flag-on 但零流量的
	// 部署会因 "mainstream == 0" 立刻报警。
	require.Contains(t, expr, "> 100",
		"缺 kind 侧量下界时，flag-on 但尚无流量的部署会立刻满足 mainstream==0 而误报")
}
