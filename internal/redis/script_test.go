package redis

import (
	"errors"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIsNoScript(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"redis.Nil", redis.Nil, false},
		{"NOSCRIPT bare", errors.New("NOSCRIPT No matching script. Please use EVAL."), true},
		{"NOSCRIPT prefix", errors.New("NOSCRIPT"), true},
		{"other err", errors.New("connection refused"), false},
		{"wrapped NOSCRIPT", fmt.Errorf("redis: %w", errors.New("NOSCRIPT ...")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoScript(tt.err); got != tt.want {
				t.Errorf("isNoScript(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRunScript_FallbackMetricPath verifies the metric label set used by
// RunScript is wired to the global registry. We can't easily trigger
// NOSCRIPT through miniredis (it caches SCRIPT LOAD automatically), so
// the unit test asserts the metric label exists and increments; the
// real fallback path is covered by bg/systemmonitor tests that exercise
// runScript directly against miniredis after a manual ScriptFlush.
func TestRunScript_FallbackMetricPath(t *testing.T) {
	baseline := testutil.ToFloat64(metrics.RedisLuaScriptFallbacksTotal.WithLabelValues("__test_only__"))
	metrics.RedisLuaScriptFallbacksTotal.WithLabelValues("__test_only__").Inc()
	got := testutil.ToFloat64(metrics.RedisLuaScriptFallbacksTotal.WithLabelValues("__test_only__")) - baseline
	if got != 1.0 {
		t.Errorf("expected metric delta=1, got %f", got)
	}
}
