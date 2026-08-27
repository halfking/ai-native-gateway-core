package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRedisLuaScriptPreloaded(t *testing.T) {
	RedisLuaScriptPreloaded.WithLabelValues("ursm").Set(1)
	RedisLuaScriptPreloaded.WithLabelValues("systemmonitor").Set(0)

	if v := testutil.ToFloat64(RedisLuaScriptPreloaded.WithLabelValues("ursm")); v != 1.0 {
		t.Errorf("ursm preload gauge = %f, want 1", v)
	}
	if v := testutil.ToFloat64(RedisLuaScriptPreloaded.WithLabelValues("systemmonitor")); v != 0.0 {
		t.Errorf("systemmonitor preload gauge = %f, want 0", v)
	}
}

func TestRedisLuaScriptSizeBytes(t *testing.T) {
	RedisLuaScriptSizeBytes.WithLabelValues("record_request.lua").Set(11469)
	RedisLuaScriptSizeBytes.WithLabelValues("claim.lua").Set(4823)

	if v := testutil.ToFloat64(RedisLuaScriptSizeBytes.WithLabelValues("record_request.lua")); v != 11469.0 {
		t.Errorf("record_request.lua size = %f, want 11469", v)
	}
	if v := testutil.ToFloat64(RedisLuaScriptSizeBytes.WithLabelValues("claim.lua")); v != 4823.0 {
		t.Errorf("claim.lua size = %f, want 4823", v)
	}
}

func TestRedisLuaScriptFallbacksTotal(t *testing.T) {
	baseline := testutil.ToFloat64(RedisLuaScriptFallbacksTotal.WithLabelValues("record_request"))

	// 模拟 3 次 NOSCRIPT 回退
	for i := 0; i < 3; i++ {
		RedisLuaScriptFallbacksTotal.WithLabelValues("record_request").Inc()
	}

	delta := testutil.ToFloat64(RedisLuaScriptFallbacksTotal.WithLabelValues("record_request")) - baseline
	if delta != 3.0 {
		t.Errorf("fallback increment = %f, want 3", delta)
	}
}

func TestRedisLuaScriptMetricNaming(t *testing.T) {
	// 确保所有指标注册到全局 registry 且带 llmgw_ 前缀
	RedisLuaScriptPreloaded.WithLabelValues("test").Set(1)
	RedisLuaScriptSizeBytes.WithLabelValues("test.lua").Set(1)
	RedisLuaScriptFallbacksTotal.WithLabelValues("test").Inc()

	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	found := map[string]bool{}
	for _, mf := range metrics {
		name := mf.GetName()
		if strings.HasPrefix(name, "llmgw_redis_lua_script_") {
			found[name] = true
			if mf.GetHelp() == "" {
				t.Errorf("metric %s has empty help", name)
			}
		}
	}

	for _, want := range []string{
		"llmgw_redis_lua_script_preloaded",
		"llmgw_redis_lua_script_size_bytes",
		"llmgw_redis_lua_script_fallbacks_total",
	} {
		if !found[want] {
			t.Errorf("expected metric %q not found", want)
		}
	}
}
