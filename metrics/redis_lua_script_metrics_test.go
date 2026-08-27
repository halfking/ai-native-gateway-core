package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Shared recorder instance to avoid duplicate metric registration
var sharedRecorder = NewRedisLuaScriptRecorder()

func TestRedisLuaScriptMetrics_RecordScriptExecution(t *testing.T) {
	recorder := sharedRecorder

	// Record EVALSHA success
	recorder.RecordScriptExecution("record_request", "evalsha", true, 0.015)

	// Record EVAL fallback
	recorder.RecordScriptExecution("record_request", "eval", true, 0.025)

	// Verify counter incremented
	count := testutil.ToFloat64(recorder.scriptExecutions.WithLabelValues("record_request", "evalsha"))
	if count != 1.0 {
		t.Errorf("expected evalsha count=1, got %f", count)
	}

	// Verify fallback counter incremented
	fallbacks := testutil.ToFloat64(recorder.scriptFallbacks.WithLabelValues("record_request"))
	if fallbacks != 1.0 {
		t.Errorf("expected fallback count=1, got %f", fallbacks)
	}
}

func TestRedisLuaScriptMetrics_SetScriptPreloaded(t *testing.T) {
	recorder := sharedRecorder

	// Set preload success
	recorder.SetScriptPreloaded("ursm", true)
	value := testutil.ToFloat64(recorder.scriptPreloaded.WithLabelValues("ursm"))
	if value != 1.0 {
		t.Errorf("expected preloaded=1.0, got %f", value)
	}

	// Set preload failure
	recorder.SetScriptPreloaded("systemmonitor", false)
	value = testutil.ToFloat64(recorder.scriptPreloaded.WithLabelValues("systemmonitor"))
	if value != 0.0 {
		t.Errorf("expected preloaded=0.0, got %f", value)
	}
}

func TestRedisLuaScriptMetrics_SetScriptSize(t *testing.T) {
	recorder := sharedRecorder

	// Set script sizes
	recorder.SetScriptSize("record_request.lua", 11469)
	recorder.SetScriptSize("claim.lua", 4823)

	// Verify sizes
	size1 := testutil.ToFloat64(recorder.scriptSizes.WithLabelValues("record_request.lua"))
	if size1 != 11469.0 {
		t.Errorf("expected size=11469, got %f", size1)
	}

	size2 := testutil.ToFloat64(recorder.scriptSizes.WithLabelValues("claim.lua"))
	if size2 != 4823.0 {
		t.Errorf("expected size=4823, got %f", size2)
	}
}

func TestRedisLuaScriptMetrics_MetricNames(t *testing.T) {
	recorder := sharedRecorder

	// Record some data to ensure metrics are registered
	recorder.RecordScriptExecution("test", "evalsha", true, 0.01)
	recorder.SetScriptPreloaded("test", true)
	recorder.SetScriptSize("test.lua", 1000)

	// Gather metrics
	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"redis_lua_script_executions_total": false,
		"redis_lua_script_fallbacks_total":  false,
		"redis_lua_script_duration_seconds": false,
		"redis_lua_script_preloaded":        false,
		"redis_lua_script_size_bytes":       false,
	}

	for _, mf := range metrics {
		name := mf.GetName()
		if _, ok := expectedMetrics[name]; ok {
			expectedMetrics[name] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("expected metric %q not found", name)
		}
	}
}

func TestRedisLuaScriptMetrics_FallbackRate(t *testing.T) {
	recorder := sharedRecorder

	// Get baseline counts (may be non-zero due to shared state)
	baselineEvalsha := testutil.ToFloat64(recorder.scriptExecutions.WithLabelValues("record_request", "evalsha"))
	baselineFallback := testutil.ToFloat64(recorder.scriptFallbacks.WithLabelValues("record_request"))

	// Simulate 100 EVALSHA calls (cache hits)
	for i := 0; i < 100; i++ {
		recorder.RecordScriptExecution("record_request", "evalsha", true, 0.010)
	}

	// Simulate 3 EVAL fallbacks (cache misses)
	for i := 0; i < 3; i++ {
		recorder.RecordScriptExecution("record_request", "eval", true, 0.025)
	}

	evalshaCount := testutil.ToFloat64(recorder.scriptExecutions.WithLabelValues("record_request", "evalsha")) - baselineEvalsha
	fallbackCount := testutil.ToFloat64(recorder.scriptFallbacks.WithLabelValues("record_request")) - baselineFallback

	// Verify incremental counts
	if evalshaCount != 100.0 {
		t.Errorf("expected evalsha increment=100, got %f", evalshaCount)
	}
	if fallbackCount != 3.0 {
		t.Errorf("expected fallback increment=3, got %f", fallbackCount)
	}

	// Calculate fallback rate
	fallbackRate := fallbackCount / evalshaCount
	if fallbackRate > 0.05 {
		t.Errorf("fallback rate %.2f%% exceeds 5%% threshold", fallbackRate*100)
	}
	t.Logf("Fallback rate: %.2f%%", fallbackRate*100)
}

func TestRedisLuaScriptMetrics_Help(t *testing.T) {
	recorder := sharedRecorder
	recorder.RecordScriptExecution("test", "evalsha", true, 0.01)

	// Gather and check help text
	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	for _, mf := range metrics {
		if strings.HasPrefix(mf.GetName(), "redis_lua_script_") {
			help := mf.GetHelp()
			if help == "" {
				t.Errorf("metric %s has empty help text", mf.GetName())
			}
			t.Logf("%s: %s", mf.GetName(), help)
		}
	}
}
