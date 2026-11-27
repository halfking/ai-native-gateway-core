// Package config implements configuration hot-reloading for the LLM Gateway.
//
// It monitors configuration files for changes and automatically reloads them
// with validation and atomic replacement to ensure service continuity.
package config

import (
	"sync/atomic"
)

// Config represents the gateway configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Plugins  PluginsConfig  `yaml:"plugins"`
	Security SecurityConfig `yaml:"security"`
}

// ServerConfig contains server settings.
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// PluginsConfig contains plugin settings.
type PluginsConfig struct {
	Directory string   `yaml:"directory"`
	Enabled   []string `yaml:"enabled"`
	// ResourceLimitsEnabled 是资源限制总开关（§6.3 配置开关）：false 时即使
	// 配置了 default_resource_limits / manifest resource_limits 也不创建
	// limiter（紧急关闭通道）。缺省 true（零值即生效，与安全默认一致）。
	ResourceLimitsEnabled *bool `yaml:"resource_limits_enabled,omitempty"`
	// DefaultResourceLimits 是全局默认资源限制（§3.4）：Manifest 未声明
	// resource_limits 时的兜底配置；nil 时无默认限制（历史行为）。
	DefaultResourceLimits *ResourceLimitsConfig `yaml:"default_resource_limits,omitempty"`
}

// ResourceLimitsConfig 是 gateway.yaml 中的资源限制配置块。结构与
// plugin-runtime/limiter 的线协议类型保持同形（JSON/YAML 字段一一对应）；
// controller 装配时转换为 pluginruntime.ResourceLimits。
type ResourceLimitsConfig struct {
	Memory       *MemoryLimitConfig `yaml:"memory,omitempty"`
	CPU          *CPULimitConfig    `yaml:"cpu,omitempty"`
	MaxProcesses int                `yaml:"max_processes,omitempty"`
}

// MemoryLimitConfig 定义内存限制。
type MemoryLimitConfig struct {
	// Limit 内存上限，格式：100M, 1G, 512MB, 256Mi
	Limit string `yaml:"limit"`
	// Swap 是否允许使用 swap（Linux only，默认 false）
	Swap bool `yaml:"swap,omitempty"`
}

// CPULimitConfig 定义 CPU 限制。
type CPULimitConfig struct {
	// Limit CPU 百分比限制，格式：25%, 50%, 100%
	Limit string `yaml:"limit"`
}

// SecurityConfig contains security settings.
type SecurityConfig struct {
	TLSEnabled bool   `yaml:"tls_enabled"`
	CertFile   string `yaml:"cert_file,omitempty"`
	KeyFile    string `yaml:"key_file,omitempty"`
}

// ConfigHolder holds the current configuration with atomic access.
// It allows thread-safe reads without locking while writes are atomic.
type ConfigHolder struct {
	config atomic.Value // stores *Config
}

// NewConfigHolder creates a new ConfigHolder with the given initial configuration.
func NewConfigHolder(initialConfig *Config) *ConfigHolder {
	holder := &ConfigHolder{}
	holder.config.Store(initialConfig)
	return holder
}

// Get returns the current configuration (thread-safe, non-blocking).
func (h *ConfigHolder) Get() *Config {
	return h.config.Load().(*Config)
}

// Set atomically updates the configuration.
func (h *ConfigHolder) Set(newConfig *Config) {
	h.config.Store(newConfig)
}
