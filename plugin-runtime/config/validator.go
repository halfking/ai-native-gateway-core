package config

import (
	"fmt"
	"os"
	"strings"
)

// Validator validates configuration.
type Validator interface {
	Validate(config *Config) error
}

type validator struct{}

// NewValidator creates a new configuration validator.
func NewValidator() Validator {
	return &validator{}
}

// Validate validates the configuration.
func (v *validator) Validate(config *Config) error {
	// Validate server config
	if err := v.validateServer(&config.Server); err != nil {
		return fmt.Errorf("invalid server config: %w", err)
	}

	// Validate plugins config
	if err := v.validatePlugins(&config.Plugins); err != nil {
		return fmt.Errorf("invalid plugins config: %w", err)
	}

	// Validate security config
	if err := v.validateSecurity(&config.Security); err != nil {
		return fmt.Errorf("invalid security config: %w", err)
	}

	return nil
}

// validateServer validates server configuration.
func (v *validator) validateServer(server *ServerConfig) error {
	if server.Host == "" {
		return fmt.Errorf("server host is required")
	}

	if server.Port < 1 || server.Port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535, got %d", server.Port)
	}

	return nil
}

// validatePlugins validates plugins configuration.
func (v *validator) validatePlugins(plugins *PluginsConfig) error {
	if plugins.Directory == "" {
		return fmt.Errorf("plugins directory is required")
	}

	// Check if directory exists
	info, err := os.Stat(plugins.Directory)
	if os.IsNotExist(err) {
		return fmt.Errorf("plugins directory does not exist: %s", plugins.Directory)
	}
	if err != nil {
		return fmt.Errorf("cannot access plugins directory: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("plugins directory is not a directory: %s", plugins.Directory)
	}

	// Validate default resource limits format early (fail at config load,
	// not at first plugin start).
	if err := v.validateResourceLimits(plugins.DefaultResourceLimits); err != nil {
		return fmt.Errorf("invalid default_resource_limits: %w", err)
	}

	return nil
}

// validateResourceLimits checks the limit formats in a resource limits block.
func (v *validator) validateResourceLimits(rl *ResourceLimitsConfig) error {
	if rl == nil {
		return nil
	}
	if rl.Memory != nil {
		if _, err := parseMemoryQuantity(rl.Memory.Limit); err != nil {
			return fmt.Errorf("memory.limit: %w", err)
		}
	}
	if rl.CPU != nil {
		if err := validateCPUPercent(rl.CPU.Limit); err != nil {
			return fmt.Errorf("cpu.limit: %w", err)
		}
	}
	if rl.MaxProcesses < 0 {
		return fmt.Errorf("max_processes must be >= 0, got %d", rl.MaxProcesses)
	}
	return nil
}

// parseMemoryQuantity validates memory formats like 256Mi / 512MB / 128M / 1G
// / plain bytes. Returns the byte count on success.
func parseMemoryQuantity(mem string) (int64, error) {
	s := strings.TrimSpace(mem)
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid memory format: %q", mem)
	}
	var value int64
	var unit string
	two := s[len(s)-2:]
	switch two {
	case "Mi", "Gi", "Ki", "MB", "GB", "KB":
		if _, err := fmt.Sscanf(s, "%d%s", &value, &unit); err != nil {
			return 0, fmt.Errorf("invalid memory format: %q", mem)
		}
	default:
		single := s[len(s)-1:]
		if single == "M" || single == "G" || single == "K" {
			if _, err := fmt.Sscanf(s, "%d%s", &value, &unit); err != nil {
				return 0, fmt.Errorf("invalid memory format: %q", mem)
			}
			unit += "B"
			if unit != "MB" && unit != "GB" && unit != "KB" {
				return 0, fmt.Errorf("unknown unit in %q", mem)
			}
		} else {
			if _, err := fmt.Sscanf(s, "%d", &value); err != nil {
				return 0, fmt.Errorf("invalid memory format: %q", mem)
			}
			return value, nil
		}
	}
	switch unit {
	case "Ki", "KB":
		return value * 1024, nil
	case "Mi", "MB":
		return value * 1024 * 1024, nil
	case "Gi", "GB":
		return value * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown unit in %q", mem)
	}
}

// validateCPUPercent checks CPU formats like "25" or "25%" within 1-100.
func validateCPUPercent(cpu string) error {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cpu), "%"))
	var percent int
	if _, err := fmt.Sscanf(s, "%d", &percent); err != nil {
		return fmt.Errorf("invalid CPU format: %q", cpu)
	}
	// 0% 会在 cgroup v2 写出 cpu.max="0 100000"，内核语义 quota=0=unlimited
	// （2026-11-28 审计 F5）——配置面与 limiter 解析面同语义拒绝。
	if percent == 0 {
		return fmt.Errorf("CPU percent must be >= 1 (0%% maps to unlimited in cgroup v2): %q", cpu)
	}
	if percent < 0 || percent > 100 {
		return fmt.Errorf("CPU percent must be 0-100, got %d", percent)
	}
	return nil
}

// validateSecurity validates security configuration.
func (v *validator) validateSecurity(security *SecurityConfig) error {
	if security.TLSEnabled {
		if security.CertFile == "" {
			return fmt.Errorf("cert_file is required when TLS is enabled")
		}
		if security.KeyFile == "" {
			return fmt.Errorf("key_file is required when TLS is enabled")
		}

		// Check if cert file exists
		if _, err := os.Stat(security.CertFile); os.IsNotExist(err) {
			return fmt.Errorf("cert file not found: %s", security.CertFile)
		}

		// Check if key file exists
		if _, err := os.Stat(security.KeyFile); os.IsNotExist(err) {
			return fmt.Errorf("key file not found: %s", security.KeyFile)
		}
	}

	return nil
}
