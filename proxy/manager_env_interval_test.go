package proxy

// manager_env_interval_test.go — S8-F4 (R60) 回归测试。
//
// NewManager 的订阅刷新 / 全节点探活间隔原为硬编码（1h / 5m），
// 现支持 env 覆盖：
//   - LLM_GATEWAY_PROXY_SUBSCRIPTION_REFRESH（默认 1h）
//   - LLM_GATEWAY_PROXY_PROBE_INTERVAL（默认 5m）
//
// 本文件断言：env 覆盖生效 + 非法值（含非正值）warn 后回退默认。

import (
	"os"
	"testing"
	"time"
)

func TestNewManagerIntervalEnvOverrides(t *testing.T) {
	tests := []struct {
		name              string
		refreshEnv        string
		probeEnv          string
		wantRefresh       time.Duration
		wantHealthCheck   time.Duration
	}{
		{
			name:            "defaults when env unset",
			refreshEnv:      "",
			probeEnv:        "",
			wantRefresh:     time.Hour,
			wantHealthCheck: 5 * time.Minute,
		},
		{
			name:            "valid overrides take effect",
			refreshEnv:      "90m",
			probeEnv:        "30s",
			wantRefresh:     90 * time.Minute,
			wantHealthCheck: 30 * time.Second,
		},
		{
			name:            "invalid refresh value falls back to default",
			refreshEnv:      "not-a-duration",
			probeEnv:        "10m",
			wantRefresh:     time.Hour,
			wantHealthCheck: 10 * time.Minute,
		},
		{
			name:            "invalid probe value falls back to default",
			refreshEnv:      "2h",
			probeEnv:        "soon",
			wantRefresh:     2 * time.Hour,
			wantHealthCheck: 5 * time.Minute,
		},
		{
			name:            "non-positive values fall back to default (NewTicker would panic)",
			refreshEnv:      "0",
			probeEnv:        "-5m",
			wantRefresh:     time.Hour,
			wantHealthCheck: 5 * time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setenvForIntervalTest(t, "LLM_GATEWAY_PROXY_SUBSCRIPTION_REFRESH", tc.refreshEnv)
			setenvForIntervalTest(t, "LLM_GATEWAY_PROXY_PROBE_INTERVAL", tc.probeEnv)

			m := NewManager(nil, nil, nil)
			if m.autoRefreshInterval != tc.wantRefresh {
				t.Errorf("autoRefreshInterval = %v, want %v", m.autoRefreshInterval, tc.wantRefresh)
			}
			if m.healthCheckInterval != tc.wantHealthCheck {
				t.Errorf("healthCheckInterval = %v, want %v", m.healthCheckInterval, tc.wantHealthCheck)
			}
		})
	}
}

// setenvForIntervalTest 设置 env 并在测试结束后恢复原值（含未设置态）。
func setenvForIntervalTest(t *testing.T, key, value string) {
	t.Helper()
	old, hadOld := os.LookupEnv(key)
	if value == "" {
		os.Unsetenv(key)
	} else if err := os.Setenv(key, value); err != nil {
		t.Fatalf("setenv %s failed: %v", key, err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}
