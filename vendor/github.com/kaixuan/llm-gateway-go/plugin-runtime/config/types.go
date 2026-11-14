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
