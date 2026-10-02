package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestBgWorkerHealthRulesUseRegisteredMetrics 钉住 bg-worker-health.yml：
// 两个 bg panic/重启指标必须都有告警覆盖（2026-10-01 结构性 P1 的"接告警"
// 半边），且不得引入 model/tenant 级高基数选择器。
func TestBgWorkerHealthRulesUseRegisteredMetrics(t *testing.T) {
	data, err := os.ReadFile("bg-worker-health.yml")
	require.NoError(t, err)

	var file ruleFile
	require.NoError(t, yaml.Unmarshal(data, &file))
	require.Len(t, file.Groups, 1)
	require.Equal(t, "bg_worker_health", file.Groups[0].Name)

	alerts := make([]string, 0, len(file.Groups[0].Rules))
	exprs := ""
	for _, r := range file.Groups[0].Rules {
		alerts = append(alerts, r.Alert)
		exprs += r.Expr + "\n"
	}

	wantAlerts := []string{
		"BgGoroutinePanicked",
		"BgWorkerCrashLoop",
	}
	require.ElementsMatch(t, wantAlerts, alerts)

	// 指标名与 bg/metrics.go 注册名逐字对齐（防漂移）。
	wantMetrics := []string{
		"llm_gateway_bg_goroutine_panics_total",
		"llm_gateway_bg_worker_restarts_total",
	}
	for _, m := range wantMetrics {
		require.Contains(t, exprs, m, "alert rules must cover metric %s", m)
	}

	// GW-00 低基数守卫：不得出现 model/tenant 选择器。
	require.NotContains(t, exprs, `model=`)
	require.NotContains(t, exprs, `tenant=`)

	text := string(data)
	require.False(t, strings.Contains(text, "model=\""))
	require.False(t, strings.Contains(text, "tenant=\""))
}
