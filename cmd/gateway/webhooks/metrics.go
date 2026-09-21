// Package webhooks contains the gateway's inbound webhook handlers.
//
// 2026-08-26 hzx-2 / 充值回调 webhook (落点 B):
//   - QuotaRechargedHandler 处理供应商充值回调，HMAC-SHA256 验签后立即触发
//     BalanceQuotaProbe.OnQuotaRecharged 把凭据从 2 分钟 tick 提到秒级。
//   - metric 前缀 llmgw_（项目惯例），通过 promauto 注册到默认 registry，
//     与 metrics/ 下的其他 CounterVec 风格一致。
package webhooks

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// QuotaWebhookMetrics 充值回调相关 Prometheus 指标。
//
//   - QuotaWebhookAcceptedTotal: HMAC 通过 + body 校验通过的 webhook 数，按 event_type 分桶。
//   - QuotaWebhookRejectedTotal: 任意阶段被拒的 webhook 数，按 reason 分桶（signature, json, schema, stale_ts, secret_missing）。
//
// reason 标签低基数且枚举稳定，可以安全用作 alert rule 的 rate() selector。
type QuotaWebhookMetrics struct {
	QuotaWebhookAcceptedTotal *prometheus.CounterVec
	QuotaWebhookRejectedTotal *prometheus.CounterVec
}

// NewQuotaWebhookMetrics 构造充值回调指标。
//
// promauto.With(prometheus.DefaultRegisterer) 让 vec 在模块加载阶段就
// 注册，避免 CounterVec "第一次 WithLabelValues 之前不出现 /metrics"
// 的常见坑（与 provider.RegisterCredentialRevealMetrics 的预热思路一致）。
func NewQuotaWebhookMetrics() *QuotaWebhookMetrics {
	factory := promauto.With(prometheus.DefaultRegisterer)
	return &QuotaWebhookMetrics{
		QuotaWebhookAcceptedTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "llmgw_quota_webhook_accepted_total",
			Help: "Total accepted quota webhook requests by event_type.",
		}, []string{"event_type"}),
		QuotaWebhookRejectedTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "llmgw_quota_webhook_rejected_total",
			Help: "Total rejected quota webhook requests by reason.",
		}, []string{"reason"}),
	}
}