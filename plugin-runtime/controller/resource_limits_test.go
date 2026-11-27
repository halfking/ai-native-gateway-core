package controller

import (
	"os"
	"path/filepath"
	"testing"

	root "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/config"
)

// writeScanFixture writes a minimal enabled-plugin layout for scan():
// <dir>/one/plugin-manifest.json without resource_limits.
func writeScanFixture(t *testing.T, manifestExtra string) (string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(filepath.Join(plugins, "one"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"plugin_id":"one","plugin_version":"1.0.0","gateway_compatibility":{"api_contract":"gateway-plugin-v1"},"runtime":{"entrypoint":"run","protocol":"http-unix-socket","handshake_path":"/plugin/handshake","health_path":"/plugin/healthz"}` + manifestExtra
	if err := os.WriteFile(filepath.Join(plugins, "one", "plugin-manifest.json"), []byte(body+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "localhost", Port: 8080},
		Plugins: config.PluginsConfig{
			Directory:             plugins,
			Enabled:               []string{"one"},
			ResourceLimitsEnabled: &enabled,
			DefaultResourceLimits: &config.ResourceLimitsConfig{
				Memory:       &config.MemoryLimitConfig{Limit: "512M"},
				CPU:          &config.CPULimitConfig{Limit: "50%"},
				MaxProcesses: 10,
			},
		},
	}
	return plugins, cfg
}

// §3.4 装配：Manifest 未声明 resource_limits 时，scan 补上 gateway.yaml 的
// default_resource_limits（转换为线协议类型）。
func TestScan_MergesDefaultResourceLimits(t *testing.T) {
	_, cfg := writeScanFixture(t, "")
	got, err := scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	m := got["one"]
	if m == nil {
		t.Fatal("plugin one missing from scan result")
	}
	if m.ResourceLimits == nil {
		t.Fatal("default_resource_limits should be merged into manifest without resource_limits")
	}
	if m.ResourceLimits.Memory == nil || m.ResourceLimits.Memory.Limit != "512M" {
		t.Errorf("memory limit not merged: %+v", m.ResourceLimits)
	}
	if m.ResourceLimits.CPU == nil || m.ResourceLimits.CPU.Limit != "50%" {
		t.Errorf("cpu limit not merged: %+v", m.ResourceLimits)
	}
	if m.ResourceLimits.MaxProcesses != 10 {
		t.Errorf("max_processes = %d, want 10", m.ResourceLimits.MaxProcesses)
	}
}

// Manifest 已声明 resource_limits 时：保留 Manifest 值，默认不覆盖。
func TestScan_ManifestLimitsWin(t *testing.T) {
	_, cfg := writeScanFixture(t, `,"resource_limits":{"memory":{"limit":"64M"},"max_processes":2}`)
	got, err := scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	rl := got["one"].ResourceLimits
	if rl == nil {
		t.Fatal("manifest resource_limits lost")
	}
	if rl.Memory == nil || rl.Memory.Limit != "64M" {
		t.Errorf("manifest memory limit should win, got %+v", rl.Memory)
	}
	if rl.MaxProcesses != 2 {
		t.Errorf("manifest max_processes should win, got %d", rl.MaxProcesses)
	}
}

// §6.3 总开关：resource_limits_enabled: false 时 scan 不合并任何限制。
func TestScan_KillSwitchDisablesLimits(t *testing.T) {
	_, cfg := writeScanFixture(t, "")
	off := false
	cfg.Plugins.ResourceLimitsEnabled = &off
	got, err := scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rl := got["one"].ResourceLimits; rl != nil {
		t.Errorf("kill switch must suppress default limits, got %+v", rl)
	}
}

// 未配置 default_resource_limits 时：Manifest 保持无限制（向后兼容）。
func TestScan_NoDefaultsConfigured(t *testing.T) {
	_, cfg := writeScanFixture(t, "")
	cfg.Plugins.DefaultResourceLimits = nil
	got, err := scan(cfg)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rl := got["one"].ResourceLimits; rl != nil {
		t.Errorf("no limits expected without defaults, got %+v", rl)
	}
}

// defaultLimitsOf 的转换完备性：config 块 → 线协议类型逐字段对应。
func TestDefaultLimitsOf_FieldMapping(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			ResourceLimitsEnabled: &enabled,
			DefaultResourceLimits: &config.ResourceLimitsConfig{
				Memory:       &config.MemoryLimitConfig{Limit: "1G", Swap: true},
				CPU:          &config.CPULimitConfig{Limit: "25%"},
				MaxProcesses: 7,
			},
		},
	}
	got := defaultLimitsOf(cfg)
	if got == nil {
		t.Fatal("expected non-nil mapping")
	}
	if got.Memory == nil || got.Memory.Limit != "1G" || !got.Memory.Swap {
		t.Errorf("memory mapping wrong: %+v", got.Memory)
	}
	if got.CPU == nil || got.CPU.Limit != "25%" {
		t.Errorf("cpu mapping wrong: %+v", got.CPU)
	}
	if got.MaxProcesses != 7 {
		t.Errorf("max_processes = %d, want 7", got.MaxProcesses)
	}
	var _ *root.ResourceLimits = got // 类型必须是线协议别名
}
