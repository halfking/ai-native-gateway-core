package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_http_requests_total",
		Help: "Total number of HTTP requests handled by llm-gateway-go.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_gateway_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds for llm-gateway-go.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "path", "status"})
)

type PrometheusMiddleware struct {
	BaseMiddleware
}

func NewPrometheusMiddleware() *PrometheusMiddleware {
	return &PrometheusMiddleware{
		BaseMiddleware: BaseMiddleware{name: "prometheus"},
	}
}

func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

func (m *PrometheusMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &prometheusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		path := normalizeMetricsPath(r.URL.Path)
		statusCode := strconv.Itoa(rw.statusCode)
		httpRequestsTotal.WithLabelValues(r.Method, path, statusCode).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path, statusCode).Observe(time.Since(start).Seconds())
	})
}

type prometheusResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *prometheusResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *prometheusResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *prometheusResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func normalizeMetricsPath(path string) string {
	switch {
	case path == "":
		return "/"
	case strings.HasPrefix(path, "/v1/sessions/"):
		return "/v1/sessions/:id"
	default:
		return path
	}
}
