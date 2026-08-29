package proxy

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 代理子系统的 Prometheus 指标面。
//
// 命名沿用仓库惯例 llm_gateway_<子系统>_*；全部为低基数总计/计数（不把节点 ID、
// server、URL 等放进 label）。per-node 细节只进 slog.Debug，避免高基数系列。
//
// 设计为可注入 Registerer：生产用 prometheus.DefaultRegisterer，测试用私有
// prometheus.NewRegistry()（参考 domains/orchestration/observ/metrics.go）。
type Metrics struct {
	subscriptionsTotal    *prometheus.GaugeVec
	nodesTotal            prometheus.Gauge
	nodesDialable         prometheus.Gauge
	nodesUnhealthy        prometheus.Gauge
	healthFailuresTotal   prometheus.Counter
	egressSelectionsTotal *prometheus.CounterVec
}

// NewMetrics 在给定的 Registerer 上注册代理指标。reg 为 nil 时使用默认注册表。
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		subscriptionsTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_subscriptions_total",
			Help: "代理订阅数量（按 active 状态分列）",
		}, []string{"active"}),
		nodesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_nodes_total",
			Help: "代理节点总数",
		}),
		nodesDialable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_nodes_dialable",
			Help: "可被 Go 直接拨号的节点数（http/https/socks5；trojan/vless 等为 0）",
		}),
		nodesUnhealthy: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_nodes_unhealthy",
			Help: "处于 unhealthy 状态的节点数（连续探活失败 >=3）",
		}),
		healthFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_health_failures_total",
			Help: "代理节点探活失败累计次数",
		}),
		egressSelectionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_egress_selections_total",
			Help: "出口节点选择次数（按结果：dialable / undialable / none）",
		}, []string{"result"}),
	}
	m.subscriptionsTotal = registerOrGetGaugeVec(reg, m.subscriptionsTotal)
	m.nodesTotal = registerOrGetGauge(reg, m.nodesTotal)
	m.nodesDialable = registerOrGetGauge(reg, m.nodesDialable)
	m.nodesUnhealthy = registerOrGetGauge(reg, m.nodesUnhealthy)
	m.healthFailuresTotal = registerOrGetCounter(reg, m.healthFailuresTotal)
	m.egressSelectionsTotal = registerOrGetCounterVec(reg, m.egressSelectionsTotal)
	return m
}

func registerOrGetGaugeVec(reg prometheus.Registerer, collector *prometheus.GaugeVec) *prometheus.GaugeVec {
	if err := reg.Register(collector); err != nil {
		if registered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if existing, ok := registered.ExistingCollector.(*prometheus.GaugeVec); ok {
				return existing
			}
		}
		panic(err)
	}
	return collector
}

func registerOrGetGauge(reg prometheus.Registerer, collector prometheus.Gauge) prometheus.Gauge {
	if err := reg.Register(collector); err != nil {
		if registered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if existing, ok := registered.ExistingCollector.(prometheus.Gauge); ok {
				return existing
			}
		}
		panic(err)
	}
	return collector
}

func registerOrGetCounter(reg prometheus.Registerer, collector prometheus.Counter) prometheus.Counter {
	if err := reg.Register(collector); err != nil {
		if registered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if existing, ok := registered.ExistingCollector.(prometheus.Counter); ok {
				return existing
			}
		}
		panic(err)
	}
	return collector
}

func registerOrGetCounterVec(reg prometheus.Registerer, collector *prometheus.CounterVec) *prometheus.CounterVec {
	if err := reg.Register(collector); err != nil {
		if registered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if existing, ok := registered.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing
			}
		}
		panic(err)
	}
	return collector
}

// SetSubscriptions 设置订阅计数（active=true/false 各一列）。
func (m *Metrics) SetSubscriptions(active, inactive int) {
	m.subscriptionsTotal.WithLabelValues("true").Set(float64(active))
	m.subscriptionsTotal.WithLabelValues("false").Set(float64(inactive))
}

// SetNodeCounts 设置节点计数。
func (m *Metrics) SetNodeCounts(total, dialable, unhealthy int) {
	m.nodesTotal.Set(float64(total))
	m.nodesDialable.Set(float64(dialable))
	m.nodesUnhealthy.Set(float64(unhealthy))
}

// IncHealthFailure 探活失败 +1。
func (m *Metrics) IncHealthFailure() {
	m.healthFailuresTotal.Inc()
}

// IncEgressSelection 记录一次出口选择结果。
func (m *Metrics) IncEgressSelection(result string) {
	m.egressSelectionsTotal.WithLabelValues(result).Inc()
}
