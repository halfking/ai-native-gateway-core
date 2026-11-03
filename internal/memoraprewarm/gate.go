// gate.go — W3：预热灰度开关与五态指标（P1-2③，ADR §2）。
package memoraprewarm

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 五态指标（ADR §2）：requested|hit|miss|timeout|unavailable。
//
//	requested：Trigger 被调且门放行（真正的分母——门关闭/未装配不计）
//	hit：旁路写成功（有注入可用）
//	miss：ASM 回 204 零命中
//	timeout：800ms 预算内未完成（HTTP 或写超时）
//	unavailable：ASM 不可用/5xx/未配置
var prewarmTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_memory_prewarm_total",
		Help: "Memora context prewarm lifecycle: requested/hit/miss/timeout/unavailable (P1-2③ G3 observability)",
	},
	[]string{"outcome"},
)

// RecordPrewarm 记一态（导出供测试与未来挂载点复用）。
func RecordPrewarm(outcome string) { prewarmTotal.WithLabelValues(outcome).Inc() }

// 注入体量分布（ADR §2：注入字节数直方图；仅实际注入时记录）。
var injectedBytes = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "gateway_memory_prewarm_injected_bytes",
		Help:    "Bytes of rendered memora injection appended to the outbound prompt (P1-2③ ADR §2)",
		Buckets: []float64{128, 256, 512, 1024, 1536, 2048, 4096},
	},
)

// RecordInjectedBytes 记一次注入体量（仅注入发生时调用）。
func RecordInjectedBytes(n int) { injectedBytes.Observe(float64(n)) }

// Outcome 常量（五态）。
const (
	OutcomeRequested   = "requested"
	OutcomeHit         = "hit"
	OutcomeMiss        = "miss"
	OutcomeTimeout     = "timeout"
	OutcomeUnavailable = "unavailable"
)

// Gate 是灰度开关（ADR §2：memory.prewarm.enabled 平台级 +
// memory.prewarm.tenant_allowlist 租户级；settings 热更）。
//
// 判定规则：
//   - enabled=false ⇒ 全关；
//   - allowlist 空 ⇒ 全开（平台级放量）；
//   - allowlist 非空 ⇒ 仅名单内租户开（灰度）。
type Gate struct {
	enabled   atomic.Bool
	allowlist atomic.Value // []string
}

// NewGate 构造（初值由 Set 设置；未 Set ⇒ 关）。
func NewGate() *Gate {
	g := &Gate{}
	g.allowlist.Store([]string(nil))
	return g
}

// Set 更新开关状态（settings 热更回调的落点；全量覆盖，非增量）。
func (g *Gate) Set(enabled bool, tenants []string) {
	if g == nil {
		return
	}
	g.enabled.Store(enabled)
	if tenants == nil {
		tenants = []string(nil)
	}
	g.allowlist.Store(tenants)
}

// Allowed 判定租户是否放行。
func (g *Gate) Allowed(tenantID string) bool {
	if g == nil || !g.enabled.Load() {
		return false
	}
	list, _ := g.allowlist.Load().([]string)
	if len(list) == 0 {
		return true
	}
	for _, t := range list {
		if t == tenantID {
			return true
		}
	}
	return false
}

// ClassifyOutcome 把 HTTP 结果映射到五态（PrewarmHTTPClient 与测试共用
// 同一口径；fake client 也可直接构造对应 err/状态）。
func ClassifyOutcome(statusCode int, err error) string {
	if err != nil {
		if isTimeoutErr(err) {
			return OutcomeTimeout
		}
		return OutcomeUnavailable
	}
	if statusCode == http.StatusNoContent {
		return OutcomeMiss
	}
	if statusCode >= 200 && statusCode < 300 {
		return OutcomeHit
	}
	return OutcomeUnavailable
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout") ||
		strings.Contains(msg, "timeout")
}

// ClassifyWriteError 把**旁路写**失败映射到五态。
//
// ★ 与 ClassifyOutcome 同族但独立：写侧没有 HTTP 状态码，只有 error。
//
//	口径同 ADR §2「timeout：800ms 预算内未完成（HTTP 或写超时）」⇒
//	写超时归 timeout、其余（连接失败、鉴权失败等）归 unavailable。
//
// ★ 存在的理由：写失败原先**只写日志不记指标** ⇒ 「有注入可用」的 hit
//
//	与「写失败」在指标上不可区分 ⇒ 漏斗对不上。
func ClassifyWriteError(err error) string {
	if err == nil {
		return OutcomeHit
	}
	if isTimeoutErr(err) {
		return OutcomeTimeout
	}
	return OutcomeUnavailable
}
