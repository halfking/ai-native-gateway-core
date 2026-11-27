package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlUnmarshal 是测试内使用的 yaml 解码薄封装。
func yamlUnmarshal(b []byte, out *Config) error {
	return yaml.Unmarshal(b, out)
}

// validBaseConfig returns a Config that passes the structural validators
// (server + an existing plugins directory).
func validBaseConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Server:  ServerConfig{Host: "0.0.0.0", Port: 8080},
		Plugins: PluginsConfig{Directory: t.TempDir()},
	}
}

// §3.4/§6.3：default_resource_limits 合法配置通过校验并正确解码。
func TestValidate_DefaultResourceLimitsOK(t *testing.T) {
	cfg := validBaseConfig(t)
	enabled := true
	cfg.Plugins.ResourceLimitsEnabled = &enabled
	cfg.Plugins.DefaultResourceLimits = &ResourceLimitsConfig{
		Memory:       &MemoryLimitConfig{Limit: "512M"},
		CPU:          &CPULimitConfig{Limit: "50%"},
		MaxProcesses: 10,
	}
	if err := NewValidator().Validate(cfg); err != nil {
		t.Fatalf("valid limits should pass: %v", err)
	}
}

// 非法内存格式 / CPU 越界 / 负进程数在配置加载期即被拒绝。
func TestValidate_DefaultResourceLimitsBad(t *testing.T) {
	cases := []struct {
		name string
		rl   *ResourceLimitsConfig
	}{
		{"bad memory", &ResourceLimitsConfig{Memory: &MemoryLimitConfig{Limit: "abc"}}},
		{"empty memory", &ResourceLimitsConfig{Memory: &MemoryLimitConfig{Limit: ""}}},
		{"cpu over 100", &ResourceLimitsConfig{CPU: &CPULimitConfig{Limit: "150%"}}},
		{"cpu garbage", &ResourceLimitsConfig{CPU: &CPULimitConfig{Limit: "fast"}}},
		{"negative procs", &ResourceLimitsConfig{MaxProcesses: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseConfig(t)
			cfg.Plugins.DefaultResourceLimits = tc.rl
			if err := NewValidator().Validate(cfg); err == nil {
				t.Fatalf("%s should fail validation", tc.name)
			}
		})
	}
}

// nil default_resource_limits 不影响既有配置合法性（向后兼容）。
func TestValidate_NoResourceLimits(t *testing.T) {
	cfg := validBaseConfig(t)
	if err := NewValidator().Validate(cfg); err != nil {
		t.Fatalf("plain config should pass: %v", err)
	}
}

// gateway.yaml 端到端：yaml 块按 §3.4 示例解码进 PluginsConfig。
func TestYAML_DefaultResourceLimitsDecodes(t *testing.T) {
	dir := t.TempDir()
	// validator 要求 directory 存在
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	yamlBody := `server:
  host: 0.0.0.0
  port: 8080
plugins:
  directory: ` + filepath.Join(dir, "plugins") + `
  enabled: [demo]
  resource_limits_enabled: true
  default_resource_limits:
    memory:
      limit: "512M"
    cpu:
      limit: "50%"
    max_processes: 10
`
	path := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yamlUnmarshal(b, &cfg); err != nil {
		t.Fatalf("yaml decode: %v", err)
	}
	if err := NewValidator().Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rl := cfg.Plugins.DefaultResourceLimits
	if rl == nil || rl.Memory == nil || rl.Memory.Limit != "512M" {
		t.Fatalf("memory limit not decoded: %+v", rl)
	}
	if rl.CPU == nil || rl.CPU.Limit != "50%" {
		t.Fatalf("cpu limit not decoded: %+v", rl)
	}
	if rl.MaxProcesses != 10 {
		t.Fatalf("max_processes = %d, want 10", rl.MaxProcesses)
	}
	if cfg.Plugins.ResourceLimitsEnabled == nil || !*cfg.Plugins.ResourceLimitsEnabled {
		t.Fatal("resource_limits_enabled should decode as true")
	}
}
