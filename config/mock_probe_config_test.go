package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMockProbeDefaults 验证 Mock Probe 4 字段默认值：全关 + Admin 隐藏 +
// 30s 间隔 + 阈值 3（设计 §3.1/§六：默认关闭，生产安全）。
func TestMockProbeDefaults(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_ENABLED", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_FAILURE_THRESHOLD", "")

	cfg := Load()
	if cfg.MockProbeEnabled {
		t.Fatal("MockProbeEnabled default must be false")
	}
	if !cfg.MockProbeHideInAdmin {
		t.Fatal("MockProbeHideInAdmin default must be true")
	}
	if cfg.MockProbeInterval != 30*time.Second {
		t.Fatalf("MockProbeInterval default = %v, want 30s", cfg.MockProbeInterval)
	}
	if cfg.MockProbeFailureThreshold != 3 {
		t.Fatalf("MockProbeFailureThreshold default = %d, want 3", cfg.MockProbeFailureThreshold)
	}
}

// TestMockProbeEnvOverrides 验证 env 覆盖解析（含 hide 的显式 false 放行）。
func TestMockProbeEnvOverrides(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", "false")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS", "7")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_FAILURE_THRESHOLD", "5")

	cfg := Load()
	if !cfg.MockProbeEnabled {
		t.Fatal("MockProbeEnabled should honor env true")
	}
	if cfg.MockProbeHideInAdmin {
		t.Fatal("MockProbeHideInAdmin should honor env false")
	}
	if cfg.MockProbeInterval != 7*time.Second {
		t.Fatalf("MockProbeInterval = %v, want 7s", cfg.MockProbeInterval)
	}
	if cfg.MockProbeFailureThreshold != 5 {
		t.Fatalf("MockProbeFailureThreshold = %d, want 5", cfg.MockProbeFailureThreshold)
	}
}

// TestMockProbeIntervalClamp 验证防探测风暴钳制：<1s（含 0/负数/垃圾值）
// 一律回落 30s（设计 §六 风险表）。
func TestMockProbeIntervalClamp(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"0", 30 * time.Second},
		{"-5", 30 * time.Second},
		{"", 30 * time.Second},
		{"garbage", 30 * time.Second},
	} {
		t.Setenv("LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS", tc.env)
		if got := Load().MockProbeInterval; got != tc.want {
			t.Errorf("interval env %q = %v, want %v", tc.env, got, tc.want)
		}
	}
}

// TestMockProbeHideInAdminHelper 验证 admin 每请求读取的 helper 与 env
// 语义一致（默认 true；false/0 放行；其它值视为 true）。
func TestMockProbeHideInAdminHelper(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true},
		{"false", false},
		{"0", false},
		{"true", true},
		{"1", true},
		{"yes", true},
	}
	for _, c := range cases {
		t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", c.env)
		if got := MockProbeHideInAdmin(); got != c.want {
			t.Errorf("MockProbeHideInAdmin() env=%q = %v, want %v", c.env, got, c.want)
		}
	}
}

// TestMockProbeYamlMerge 验证 YAML 文件路径的 4 字段合并（env 优先）。
func TestMockProbeYamlMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yaml := `
mock_probe_enabled: true
mock_probe_hide_in_admin: false
mock_probe_interval: 15s
mock_probe_failure_threshold: 9
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_ENABLED", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS", "")
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_FAILURE_THRESHOLD", "")

	cfg := Load()
	if err := cfg.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if !cfg.MockProbeEnabled || cfg.MockProbeHideInAdmin ||
		cfg.MockProbeInterval != 15*time.Second || cfg.MockProbeFailureThreshold != 9 {
		t.Fatalf("yaml merge mismatch: %+v", cfg)
	}

	// env 优先于 yaml。
	t.Setenv("LLM_GATEWAY_MOCK_PROBE_INTERVAL_SECONDS", "60")
	cfg2 := Load()
	if err := cfg2.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if cfg2.MockProbeInterval != 60*time.Second {
		t.Fatalf("env should win over yaml: got %v", cfg2.MockProbeInterval)
	}
}
