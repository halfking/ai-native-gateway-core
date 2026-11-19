// Package maasapi — HTTP 层可观测性：Prometheus 指标 + OTel 追踪中间件。
//
// 指标直接注册到 prometheus.DefaultRegisterer（gateway-v2 的 /metrics 已用
// promhttp.HandlerFor 输出该注册表），首次打点后自动出现在 /metrics。
package maasapi

import (
	"time"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MaaS HTTP 层核心指标（对照 PHASE-NEXT-IMPLEMENTATION-PLAN §3.2 关键指标）。
var (
	// maasHTTPRequestsTotal 按 method/route/status 分组的请求计数。
	maasHTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "maas",
			Name:      "http_requests_total",
			Help:      "Total MaaS API requests by method, route and status",
		},
		[]string{"method", "route", "status"},
	)

	// maasHTTPDurationSeconds 请求延迟分布（P95/P99 目标 < 200ms）。
	maasHTTPDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "maas",
			Name:      "http_request_duration_seconds",
			Help:      "MaaS API request latency (success path; success target P99 < 200ms)",
			Buckets:   []float64{.005, .01, .025, .05, .1, .2, .5, 1, 2.5, 5},
		},
		[]string{"method", "route", "status"},
	)

	// maasDomainErrorsTotal 领域错误码计数（超卖/配额/围栏不匹配等业务信号）。
	maasDomainErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "maas",
			Name:      "domain_errors_total",
			Help:      "Domain error occurrences by contract error code",
		},
		[]string{"code"},
	)
)

// RecordDomainError 记录一次领域错误码（由 respondError 调用）。
func RecordDomainError(code string) {
	maasDomainErrorsTotal.WithLabelValues(code).Inc()
}

// metricsMiddleware 为每个请求打点：计数 + 延迟（按路由模板而非具体路径，
// 避免高基数标签炸掉 Prometheus）。
//
// 打点通过 Response.Before 钩子在响应状态提交时执行 —— 因为 Echo 的错误
// 处理器（404/422/409 等）在中间件链返回之后才写响应，若在中间件返回时
// 打点会一律记成默认 200，污染状态码维度。
func metricsMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()

			res, ok := c.Response().(*echo.Response)
			if !ok {
				return next(c)
			}
			method := c.Request().Method
			// 路由标签只接受已注册模板（RouteInfo.Path）；未匹配路由
			// （404/405 时 RouteInfo.Path 为空）归入 "unmatched"，防止
			// 攻击者用随机路径撑爆 Prometheus 标签基数。
			route := redactRoute(c.RouteInfo().Path)

			// ★ 钩子必须在 next(c) 之前注册：Echo 的错误处理器在中间件
			// 链返回之后才写响应，若在返回后注册则响应已提交、钩子永不
			// 触发，状态码也会一律误记为 200。闭包在 WriteHeader 时刻
			// 读取最终 Status 并计算总延迟。
			res.Before(func() {
				status := itoa(res.Status)
				maasHTTPRequestsTotal.WithLabelValues(method, route, status).Inc()
				maasHTTPDurationSeconds.WithLabelValues(method, route, status).
					Observe(time.Since(start).Seconds())
			})

			return next(c)
		}
	}
}

// redactRoute 把路径参数替换为模板占位，保持标签基数有界。
// Echo 的 c.Path() 已返回注册模板（如 /v0.1/holds/:hold_id/confirm），直接可用。
func redactRoute(route string) string {
	if route == "" {
		return "unmatched"
	}
	return route
}

func itoa(v int) string {
	// 小而直接的 int → string，避免 fmt.Sprintf 在热路径的分配开销。
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
