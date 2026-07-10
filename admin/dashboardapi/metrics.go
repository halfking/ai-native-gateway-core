// Package dashboardapi - metrics.go
// Prometheus metrics for Dashboard API
//
// Metrics exposed:
//   - llmgw_dashboard_api_requests_total       (Counter)   API请求总数 by endpoint/status
//   - llmgw_dashboard_api_duration_seconds     (Histogram) API响应延迟
//   - llmgw_dashboard_api_errors_total         (Counter)   API错误总数 by endpoint/error_type
//   - llmgw_dashboard_slow_queries_total       (Counter)   慢查询总数 by endpoint
//   - llmgw_dashboard_cache_operations_total   (Counter)   缓存操作 by operation/result
package dashboardapi

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// dashboardAPIRequestsTotal counts API requests by endpoint and status.
	dashboardAPIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_dashboard_api_requests_total",
			Help: "Total number of Dashboard API requests.",
		},
		[]string{"endpoint", "status"}, // status: success/error/timeout
	)

	// dashboardAPIDuration observes API response time in seconds.
	dashboardAPIDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "llmgw_dashboard_api_duration_seconds",
			Help: "Dashboard API response duration in seconds.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"endpoint"},
	)

	// dashboardAPIErrorsTotal counts API errors by endpoint and error type.
	dashboardAPIErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_dashboard_api_errors_total",
			Help: "Total number of Dashboard API errors.",
		},
		[]string{"endpoint", "error_type"}, // error_type: database/timeout/validation/internal
	)

	// dashboardSlowQueriesTotal counts slow queries (>1s).
	dashboardSlowQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_dashboard_slow_queries_total",
			Help: "Total number of slow queries (>1s) in Dashboard API.",
		},
		[]string{"endpoint"},
	)

	// dashboardCacheOperationsTotal counts cache operations.
	dashboardCacheOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_dashboard_cache_operations_total",
			Help: "Total number of Dashboard cache operations.",
		},
		[]string{"operation", "result"}, // operation: get/set/delete, result: hit/miss/error
	)

	// dashboardActiveConnections tracks current active API connections.
	dashboardActiveConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "llmgw_dashboard_active_connections",
			Help: "Current number of active Dashboard API connections.",
		},
	)

	// dashboardQueryRowsReturned observes number of rows returned per query.
	dashboardQueryRowsReturned = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "llmgw_dashboard_query_rows_returned",
			Help: "Number of rows returned by Dashboard API queries.",
			Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000, 10000},
		},
		[]string{"endpoint"},
	)
)

// recordAPIRequest records metrics for a Dashboard API request.
func recordAPIRequest(endpoint string, status string, duration time.Duration) {
	dashboardAPIRequestsTotal.WithLabelValues(endpoint, status).Inc()
	dashboardAPIDuration.WithLabelValues(endpoint).Observe(duration.Seconds())

	// Record slow query if duration > 1s
	if duration.Seconds() > 1.0 {
		dashboardSlowQueriesTotal.WithLabelValues(endpoint).Inc()
	}
}

// recordAPIError records an API error.
func recordAPIError(endpoint string, errorType string) {
	dashboardAPIErrorsTotal.WithLabelValues(endpoint, errorType).Inc()
}

// recordCacheOperation records a cache operation.
func recordCacheOperation(operation string, result string) {
	dashboardCacheOperationsTotal.WithLabelValues(operation, result).Inc()
}

// incrementActiveConnections increments the active connections gauge.
func incrementActiveConnections() {
	dashboardActiveConnections.Inc()
}

// decrementActiveConnections decrements the active connections gauge.
func decrementActiveConnections() {
	dashboardActiveConnections.Dec()
}

// recordQueryRows records the number of rows returned by a query.
func recordQueryRows(endpoint string, rows int) {
	dashboardQueryRowsReturned.WithLabelValues(endpoint).Observe(float64(rows))
}
