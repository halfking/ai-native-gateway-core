package config

import (
	"fmt"
	"os"
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
