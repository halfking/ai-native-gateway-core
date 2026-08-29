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

	// 订阅刷新指标
	subscriptionRefreshTotal          *prometheus.CounterVec // labels: subscription_id_prefix, status
	subscriptionRefreshDurationSeconds prometheus.Histogram
	subscriptionNodeCount             *prometheus.GaugeVec // labels: subscription_id_prefix

	// 节点健康检查指标
	nodeHealthCheckTotal          *prometheus.CounterVec // labels: status
	nodeHealthCheckDurationSeconds prometheus.Histogram
	nodeResponseTimeMs            prometheus.Histogram
	nodeConsecutiveFailures       prometheus.Histogram

	// 节点选择指标
	nodeSelectionTotal          *prometheus.CounterVec // labels: result
	nodeSelectionDurationSeconds prometheus.Histogram

	// 密码解密指标
	passwordDecryptFailedTotal prometheus.Counter

	// Transport 连接池指标
	transportCacheSize          prometheus.Gauge
	transportInvalidationsTotal prometheus.Counter
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

		// 订阅刷新指标
		subscriptionRefreshTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_subscription_refresh_total",
			Help: "代理订阅刷新次数（按订阅ID前缀和状态分列）",
		}, []string{"subscription_id_prefix", "status"}),
		subscriptionRefreshDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_gateway_proxy_subscription_refresh_duration_seconds",
			Help:    "代理订阅刷新耗时分布（秒）",
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
		}),
		subscriptionNodeCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_subscription_node_count",
			Help: "每个订阅的节点数量",
		}, []string{"subscription_id_prefix"}),

		// 节点健康检查指标
		nodeHealthCheckTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_node_health_check_total",
			Help: "节点健康检查次数（按状态分列：success / timeout / error）",
		}, []string{"status"}),
		nodeHealthCheckDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_gateway_proxy_node_health_check_duration_seconds",
			Help:    "节点健康检查耗时分布（秒）",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1.0, 2.0, 5.0},
		}),
		nodeResponseTimeMs: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_gateway_proxy_node_response_time_ms",
			Help:    "节点响应时间分布（毫秒）",
			Buckets: []float64{10, 50, 100, 200, 500, 1000, 2000, 5000},
		}),
		nodeConsecutiveFailures: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_gateway_proxy_node_consecutive_failures",
			Help:    "节点连续失败次数分布",
			Buckets: []float64{1, 2, 3, 5, 10, 20, 50},
		}),

		// 节点选择指标
		nodeSelectionTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_node_selection_total",
			Help: "节点选择次数（按结果分列：success / no_healthy / no_available）",
		}, []string{"result"}),
		nodeSelectionDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_gateway_proxy_node_selection_duration_seconds",
			Help:    "节点选择耗时分布（秒）",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
		}),

		// 密码解密指标
		passwordDecryptFailedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_password_decrypt_failed_total",
			Help: "代理节点密码解密失败累计次数",
		}),

		// Transport 连接池指标
		transportCacheSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "llm_gateway_proxy_transport_cache_size",
			Help: "Transport 连接池当前缓存大小",
		}),
		transportInvalidationsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "llm_gateway_proxy_transport_invalidations_total",
			Help: "Transport 连接池失效累计次数",
		}),
	}

	// 注册现有指标
	m.subscriptionsTotal = registerOrGetGaugeVec(reg, m.subscriptionsTotal)
	m.nodesTotal = registerOrGetGauge(reg, m.nodesTotal)
	m.nodesDialable = registerOrGetGauge(reg, m.nodesDialable)
	m.nodesUnhealthy = registerOrGetGauge(reg, m.nodesUnhealthy)
	m.healthFailuresTotal = registerOrGetCounter(reg, m.healthFailuresTotal)
	m.egressSelectionsTotal = registerOrGetCounterVec(reg, m.egressSelectionsTotal)

	// 注册订阅刷新指标
	m.subscriptionRefreshTotal = registerOrGetCounterVec(reg, m.subscriptionRefreshTotal)
	m.subscriptionRefreshDurationSeconds = registerOrGetHistogram(reg, m.subscriptionRefreshDurationSeconds)
	m.subscriptionNodeCount = registerOrGetGaugeVec(reg, m.subscriptionNodeCount)

	// 注册节点健康检查指标
	m.nodeHealthCheckTotal = registerOrGetCounterVec(reg, m.nodeHealthCheckTotal)
	m.nodeHealthCheckDurationSeconds = registerOrGetHistogram(reg, m.nodeHealthCheckDurationSeconds)
	m.nodeResponseTimeMs = registerOrGetHistogram(reg, m.nodeResponseTimeMs)
	m.nodeConsecutiveFailures = registerOrGetHistogram(reg, m.nodeConsecutiveFailures)

	// 注册节点选择指标
	m.nodeSelectionTotal = registerOrGetCounterVec(reg, m.nodeSelectionTotal)
	m.nodeSelectionDurationSeconds = registerOrGetHistogram(reg, m.nodeSelectionDurationSeconds)

	// 注册密码解密指标
	m.passwordDecryptFailedTotal = registerOrGetCounter(reg, m.passwordDecryptFailedTotal)

	// 注册 Transport 连接池指标
	m.transportCacheSize = registerOrGetGauge(reg, m.transportCacheSize)
	m.transportInvalidationsTotal = registerOrGetCounter(reg, m.transportInvalidationsTotal)

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

func registerOrGetHistogram(reg prometheus.Registerer, collector prometheus.Histogram) prometheus.Histogram {
	if err := reg.Register(collector); err != nil {
		if registered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if existing, ok := registered.ExistingCollector.(prometheus.Histogram); ok {
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

// ObserveSubscriptionRefresh 记录一次订阅刷新操作（带耗时）。
// subscriptionID 会被截取前8位以避免高基数；status 为 "success" 或 "error"。
func (m *Metrics) ObserveSubscriptionRefresh(subscriptionID string, status string, durationSeconds float64) {
	prefix := truncateID(subscriptionID, 8)
	m.subscriptionRefreshTotal.WithLabelValues(prefix, status).Inc()
	m.subscriptionRefreshDurationSeconds.Observe(durationSeconds)
}

// SetSubscriptionNodeCount 设置某个订阅的节点数量。
// subscriptionID 会被截取前8位以避免高基数。
func (m *Metrics) SetSubscriptionNodeCount(subscriptionID string, count int) {
	prefix := truncateID(subscriptionID, 8)
	m.subscriptionNodeCount.WithLabelValues(prefix).Set(float64(count))
}

// IncHealthCheck 记录一次节点健康检查。
// status 可以是 "success"、"timeout"、"error"。
func (m *Metrics) IncHealthCheck(status string) {
	m.nodeHealthCheckTotal.WithLabelValues(status).Inc()
}

// ObserveHealthCheckDuration 记录节点健康检查耗时（秒）。
func (m *Metrics) ObserveHealthCheckDuration(durationSeconds float64) {
	m.nodeHealthCheckDurationSeconds.Observe(durationSeconds)
}

// ObserveNodeResponseTime 记录节点响应时间（毫秒）。
func (m *Metrics) ObserveNodeResponseTime(ms float64) {
	m.nodeResponseTimeMs.Observe(ms)
}

// ObserveNodeConsecutiveFailures 记录节点连续失败次数。
func (m *Metrics) ObserveNodeConsecutiveFailures(count int) {
	m.nodeConsecutiveFailures.Observe(float64(count))
}

// ObserveNodeSelection 记录一次节点选择操作（带耗时）。
// result 可以是 "success"、"no_healthy"、"no_available"。
func (m *Metrics) ObserveNodeSelection(result string, durationSeconds float64) {
	m.nodeSelectionTotal.WithLabelValues(result).Inc()
	m.nodeSelectionDurationSeconds.Observe(durationSeconds)
}

// IncPasswordDecryptFailed 记录一次密码解密失败。
func (m *Metrics) IncPasswordDecryptFailed() {
	m.passwordDecryptFailedTotal.Inc()
}

// SetTransportCacheSize 设置 Transport 连接池当前大小。
func (m *Metrics) SetTransportCacheSize(size int) {
	m.transportCacheSize.Set(float64(size))
}

// IncTransportInvalidation 记录一次 Transport 连接池失效。
func (m *Metrics) IncTransportInvalidation() {
	m.transportInvalidationsTotal.Inc()
}

// truncateID 截取 ID 的前 n 位，避免高基数标签。
func truncateID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}
