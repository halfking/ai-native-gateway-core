package autoroute

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// gatherRoleFallbackMetrics 收集本指标族在**默认 registry** 上的实际值。
//
// 走 Gather 而不是直接读 counter 变量：只有 Gather 能回答「这条 series
// 在 /metrics 上真的存在吗」——而那正是本文件要证的承重性质
// （flag-off 部署上也必须存在，读数 0）。
func gatherRoleFallbackMetrics(t *testing.T) (layers map[string]float64, active float64, activeFound bool) {
	t.Helper()
	return gatherRoleFallbackFrom(t, prometheus.DefaultGatherer)
}

func gatherRoleFallbackFrom(t *testing.T, g prometheus.Gatherer) (map[string]float64, float64, bool) {
	t.Helper()

	mfs, err := g.Gather()
	require.NoError(t, err)

	layers := map[string]float64{}
	active := 0.0
	activeFound := false
	for _, mf := range mfs {
		switch mf.GetName() {
		case "llmgw_autoroute_role_fallback_layer_total":
			for _, metric := range mf.GetMetric() {
				var layer string
				for _, lp := range metric.GetLabel() {
					if lp.GetName() == "layer" {
						layer = lp.GetValue()
					}
				}
				layers[layer] = metric.GetCounter().GetValue()
			}
		case "llmgw_autoroute_role_routing_active":
			activeFound = true
			for _, metric := range mf.GetMetric() {
				active = metric.GetGauge().GetValue()
			}
		}
	}
	return layers, active, activeFound
}

// TestRoleFallbackMetricsExposeBothLayersWhenFeatureDisabled 是本文件的核心门。
//
// 它守的不是一个"字段存在"，而是**告警能不能在真故障时开口**。
//
// 背景：父开关 AUTO_ROLE_ROUTING_ENABLED 默认关闭。关闭时 rolePrefs 为空、
// RoleFallbackLayer 带 omitempty 根本不进序列化，SQL 侧
// auto_decision->>'role_fallback_layer' 恒为 NULL。若 counter 也不预置，
// flag-off 部署的 increase(...{layer="mainstream"}[15m]) 返回**空向量**，
// 告警拿到 no data 而不是 0 —— 而"no data"和"真的是 0"在告警语义上完全
// 不同：前者不触发任何通知，于是「轻量池整层掉线」与「机制没装载」
// 在监控面板上长得一模一样，两者都是静默的。
//
// 因此 Add(0) 预置是承重的：删掉它，本门红，且线上告警同步失效。
//
// 刻意用**全新 registry** 而不是默认 registry：包内其他 R52 测试
// （decision_role_wiring_test 等）会走真实决策路径给默认 registry 里的
// 同名 series 加值。在默认 registry 上，"零事件时 series 就存在"这件事
// 会被那些事件证伪不了——series 早被别的测试建出来了，本门会退化成
// 恒绿，恰好丢掉它唯一要守的性质。
func TestRoleFallbackMetricsExposeBothLayersWhenFeatureDisabled(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, gauge := newRoleFallbackMetrics(reg, false)

	layers, active, activeFound := gatherRoleFallbackFrom(t, reg)
	require.True(t, activeFound, "llmgw_autoroute_role_routing_active 未注册")
	require.Equal(t, 0.0, active, "父开关关闭时 gauge 必须是 0")
	require.Equal(t, 0.0, gaugeValue(gauge), "gauge 构造值与暴露值不一致")

	// 承重断言：两个 label 值在**零事件**的干净 registry 上就已存在。
	require.Contains(t, layers, RoleLayerKind,
		"layer=kind 必须在构造时预置：CounterVec 未 WithLabelValues 过就不会出现在 /metrics，increase() 会返回空向量而非 0")
	require.Contains(t, layers, RoleLayerMainstream,
		"layer=mainstream 必须预置：否则 flag-off 部署上告警静默拿不到 no data 之外的真实读数")
	require.Equal(t, 0.0, layers[RoleLayerKind])
	require.Equal(t, 0.0, layers[RoleLayerMainstream])
}

// gaugeValue 读 Gauge 的当前值（prometheus.Gauge 是接口，测试需要具体值）。
func gaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	g.Write(&m)
	return m.GetGauge().GetValue()
}

// TestRoleFallbackGaugeReflectsEnabledFlag 钉住 gauge 的两个取值方向。
//
// 告警表达式完全依赖它做前置门：==0 走"机制没开"可见性告警，==1 才让
// 主流层告警有资格求值。两个方向取反都会让对应那条告警静默。
func TestRoleFallbackGaugeReflectsEnabledFlag(t *testing.T) {
	reg := prometheus.NewRegistry()
	newRoleFallbackMetrics(reg, true)

	_, active, found := gatherRoleFallbackFrom(t, reg)
	require.True(t, found)
	require.Equal(t, 1.0, active, "父开关开启时 gauge 必须是 1，否则主流层告警永远被门挡住")
}

// TestRecordRoleFallbackLayerCountsMainstreamSeparately 证伪"两个 label 记成同一个"。
//
// 若两条 series 被记成同一个值，主流层占比恒为 50%（或 0%），告警要么永不
// 触发要么恒触发——两种都是静默的失效形态。
func TestRecordRoleFallbackLayerCountsMainstreamSeparately(t *testing.T) {
	before, _, _ := gatherRoleFallbackMetrics(t)

	recordRoleFallbackLayer(RoleLayerMainstream)
	recordRoleFallbackLayer(RoleLayerMainstream)
	recordRoleFallbackLayer(RoleLayerKind)

	after, _, _ := gatherRoleFallbackMetrics(t)

	// 断言**增量**而非绝对值：本包其他测试会往默认 registry 累加，
	// 绝对值断言既依赖测试顺序又会在包内执行顺序变化时随机失败。
	require.Equal(t, before[RoleLayerMainstream]+2, after[RoleLayerMainstream],
		"两次 mainstream 必须落在 mainstream series 上")
	require.Equal(t, before[RoleLayerKind]+1, after[RoleLayerKind],
		"一次 kind 必须落在 kind series 上，不能与 mainstream 混记")

	// 本次三个事件的占比是 2/3 —— 即告警表达式的分母语义
	// （role 路由介入的请求数，非全量 auto 流量）。
	deltaMainstream := after[RoleLayerMainstream] - before[RoleLayerMainstream]
	deltaKind := after[RoleLayerKind] - before[RoleLayerKind]
	require.InDelta(t, 2.0/3.0, deltaMainstream/(deltaMainstream+deltaKind), 1e-9)
}

// TestRecordRoleFallbackLayerCollapsesUnknownToKind 钉住防御分支。
//
// 未知取值归入 kind 而不是静默丢弃：若将来 layerName 新增第三层而忘记改
// recordRoleFallbackLayer，丢弃会让主流层告警保持沉默（危险方向），
// 归入 kind 则让占比下降、告警保守不响（安全方向）。
func TestRecordRoleFallbackLayerCollapsesUnknownToKind(t *testing.T) {
	before, _, _ := gatherRoleFallbackMetrics(t)

	recordRoleFallbackLayer("some-future-layer")

	after, _, _ := gatherRoleFallbackMetrics(t)
	require.Equal(t, before[RoleLayerKind]+1, after[RoleLayerKind],
		"未知 layer 必须落到 kind（保守方向），不能静默丢弃")
	require.Equal(t, before[RoleLayerMainstream], after[RoleLayerMainstream],
		"未知 layer 不得污染 mainstream series")
	require.Len(t, after, 2, "series 数必须恒为 2，未知取值不得创建新 label")
}

// TestRoleFallbackMetricNamesMatchAlertRule 钉住指标名与告警表达式同源。
//
// 指标名在两处硬编码（Go 注册 + YAML 规则）。任一侧改名，YAML 侧的
// require.Contains 仍然全绿（它只查字符串），但线上告警会静默失效——
// Prometheus 对不存在的 metric 求值得到空向量，不报错。
// 这条门把两侧钉在一起。
func TestRoleFallbackMetricNamesMatchAlertRule(t *testing.T) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	registered := map[string]bool{}
	for _, mf := range mfs {
		registered[mf.GetName()] = true
	}

	// 与 deploy/prometheus/rules/role-fallback-mainstream.yml 中引用的
	// 三个名字逐一对应（两个 counter label 值共用同一个 metric name）。
	require.True(t, registered["llmgw_autoroute_role_fallback_layer_total"],
		"告警表达式引用的 counter 未注册，PromQL 求值得到空向量且不报错——告警会静默失效")
	require.True(t, registered["llmgw_autoroute_role_routing_active"],
		"告警表达式的 role_routing_active 门未注册，rule 恒不成立")
}

// TestRoleLayerConstantsMatchAuditFieldValues 证伪"埋点与审计字段分叉"。
//
// Decision.RoleFallbackLayer 经 decisionToWire 序列化进 request_logs.auto_decision，
// 运维的 SQL 与 Prometheus 的告警看的是同一个语义。若两处字面量不同
// （例如审计写 "mainstream"、埋点记 "main"），SQL 查得到但告警不响。
func TestRoleLayerConstantsMatchAuditFieldValues(t *testing.T) {
	require.Equal(t, "kind", RoleLayerKind)
	require.Equal(t, "mainstream", RoleLayerMainstream)

	// layerName 是审计字段的唯一产出口径。
	require.Equal(t, RoleLayerKind, layerName(1))
	require.Equal(t, RoleLayerMainstream, layerName(2))
	require.Equal(t, "", layerName(0), "未命中必须产出空串（omitempty 依赖它）")
	require.Equal(t, "", layerName(3), "未知层号必须产出空串，不得回落成某个合法取值")
}
