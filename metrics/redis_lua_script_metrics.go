package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RedisLuaScriptMetrics tracks Redis Lua script execution and caching behavior.
//
// Design: These metrics help detect SCRIPT FLUSH events and EVALSHA cache misses
// that cause expensive fallback to EVAL (transmitting 11 KB scripts on hot path).
//
// Usage:
//
//	recorder := NewRedisLuaScriptRecorder()
//	recorder.RecordScriptExecution("record_request", "evalsha", true, 0.015)
//	recorder.RecordScriptExecution("record_request", "eval_fallback", false, 0.025)
type RedisLuaScriptMetrics struct {
	// scriptExecutions counts Lua script calls by name and method (evalsha vs eval)
	scriptExecutions *prometheus.CounterVec

	// scriptFallbacks counts EVALSHA → EVAL fallbacks (NOSCRIPT errors)
	scriptFallbacks *prometheus.CounterVec

	// scriptDuration tracks script execution latency
	scriptDuration *prometheus.HistogramVec

	// scriptPreloaded indicates whether scripts were successfully preloaded at startup
	scriptPreloaded *prometheus.GaugeVec

	// scriptSizes exposes the byte size of each embedded Lua script
	scriptSizes *prometheus.GaugeVec
}

// NewRedisLuaScriptRecorder creates a new Redis Lua script metrics recorder.
func NewRedisLuaScriptRecorder() *RedisLuaScriptMetrics {
	return &RedisLuaScriptMetrics{
		scriptExecutions: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_lua_script_executions_total",
				Help: "Total number of Redis Lua script executions by script name and method (evalsha/eval)",
			},
			[]string{"script", "method"},
		),
		scriptFallbacks: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_lua_script_fallbacks_total",
				Help: "Total number of EVALSHA → EVAL fallbacks due to NOSCRIPT errors",
			},
			[]string{"script"},
		),
		scriptDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "redis_lua_script_duration_seconds",
				Help:    "Redis Lua script execution duration in seconds",
				Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
			},
			[]string{"script", "method"},
		),
		scriptPreloaded: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_lua_script_preloaded",
				Help: "Indicates whether Lua scripts were successfully preloaded at startup (1=success, 0=failed)",
			},
			[]string{"module"}, // "ursm" or "systemmonitor"
		),
		scriptSizes: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_lua_script_size_bytes",
				Help: "Size in bytes of embedded Lua scripts",
			},
			[]string{"script"},
		),
	}
}

// RecordScriptExecution records a Lua script execution.
//
// Parameters:
//   - scriptName: e.g., "record_request", "claim", "apply_decision"
//   - method: "evalsha" (cache hit) or "eval" (fallback)
//   - success: whether the execution succeeded
//   - durationSec: execution duration in seconds
func (m *RedisLuaScriptMetrics) RecordScriptExecution(scriptName, method string, success bool, durationSec float64) {
	m.scriptExecutions.WithLabelValues(scriptName, method).Inc()
	m.scriptDuration.WithLabelValues(scriptName, method).Observe(durationSec)

	// If method is "eval", it means we fell back from EVALSHA
	if method == "eval" {
		m.scriptFallbacks.WithLabelValues(scriptName).Inc()
	}
}

// SetScriptPreloaded sets the preload status for a module.
//
// Call this after PreloadScripts() succeeds or fails:
//
//	if _, err := store.PreloadScripts(ctx, rdb); err != nil {
//	    recorder.SetScriptPreloaded("ursm", false)
//	} else {
//	    recorder.SetScriptPreloaded("ursm", true)
//	}
func (m *RedisLuaScriptMetrics) SetScriptPreloaded(module string, success bool) {
	value := 0.0
	if success {
		value = 1.0
	}
	m.scriptPreloaded.WithLabelValues(module).Set(value)
}

// SetScriptSizes exposes script sizes for monitoring.
//
// Call this once at startup:
//
//	sizes := store.ScriptSizes()
//	for name, size := range sizes {
//	    recorder.SetScriptSize(name, size)
//	}
func (m *RedisLuaScriptMetrics) SetScriptSize(scriptName string, sizeBytes int) {
	m.scriptSizes.WithLabelValues(scriptName).Set(float64(sizeBytes))
}

// Example Prometheus queries:
//
// 1. EVALSHA fallback rate (should be <0.5% after preloading):
//
//	rate(redis_lua_script_fallbacks_total[5m])
//	  / rate(redis_lua_script_executions_total{method="evalsha"}[5m])
//
// 2. Top scripts by fallback count:
//
//	topk(5, sum by (script) (rate(redis_lua_script_fallbacks_total[5m])))
//
// 3. Script execution latency p95:
//
//	histogram_quantile(0.95,
//	  sum(rate(redis_lua_script_duration_seconds_bucket[5m])) by (script, le))
//
// 4. Large scripts being executed via EVAL (network overhead):
//
//	redis_lua_script_size_bytes > 10000
//
// 5. Modules with failed preload:
//
//	redis_lua_script_preloaded == 0
//
// Example Grafana alert:
//
//	- alert: RedisLuaScriptFallbackHigh
//	  expr: |
//	    rate(redis_lua_script_fallbacks_total{script="record_request"}[5m])
//	      / rate(redis_lua_script_executions_total{script="record_request"}[5m])
//	      > 0.01
//	  for: 5m
//	  annotations:
//	    summary: "Redis Lua script fallback rate > 1% for record_request"
//	    description: "EVALSHA cache misses causing 11 KB transmission per fallback"
