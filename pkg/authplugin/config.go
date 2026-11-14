package authplugin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadConfigFromEnv loads plugin configuration from environment variables.
//
// Environment variables:
//   - AUTH_PROVIDER: Provider name (default: "native")
//   - USER_MANAGER: Manager name (default: "native")
//   - TENANT_MANAGER: Manager name (default: "native")
//   - APPROVAL_PROVIDER: Provider name (default: "native")
//
// Native provider (PostgreSQL):
//   - DATABASE_DSN: PostgreSQL connection string
//   - JWT_SECRET: JWT signing secret
//   - ENCRYPTION_KEY: AES-256 encryption key (32 bytes)
//
// Casdoor provider:
//   - CASDOOR_ENDPOINT: Casdoor server URL
//   - CASDOOR_CLIENT_ID: OAuth2 client ID
//   - CASDOOR_CLIENT_SECRET: OAuth2 client secret
//   - CASDOOR_ORGANIZATION: Organization name
//   - CASDOOR_APPLICATION: Application name
//   - CASDOOR_TLS_SKIP_VERIFY: Skip TLS verification (default: false)
//   - CASDOOR_CERTIFICATE: TLS certificate (optional)
func LoadConfigFromEnv() (*PluginConfig, error) {
	cfg := &PluginConfig{
		AuthProvider:     getEnvOrDefault("AUTH_PROVIDER", "native"),
		UserManager:      getEnvOrDefault("USER_MANAGER", "native"),
		TenantManager:    getEnvOrDefault("TENANT_MANAGER", "native"),
		ApprovalProvider: getEnvOrDefault("APPROVAL_PROVIDER", "native"),
	}

	// Always load native config (needed even for hybrid configurations)
	cfg.Native = &NativeConfig{
		DatabaseDSN:   os.Getenv("DATABASE_DSN"),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		EncryptionKey: []byte(os.Getenv("ENCRYPTION_KEY")),
	}

	// Validate native config if any provider uses "native"
	if needsNativeConfig(cfg) {
		if err := validateNativeConfig(cfg.Native); err != nil {
			return nil, fmt.Errorf("native config validation failed: %w", err)
		}
	}

	// Load Casdoor config if any provider uses "casdoor"
	if needsCasdoorConfig(cfg) {
		cfg.Casdoor = &CasdoorConfig{
			Endpoint:      os.Getenv("CASDOOR_ENDPOINT"),
			ClientID:      os.Getenv("CASDOOR_CLIENT_ID"),
			ClientSecret:  os.Getenv("CASDOOR_CLIENT_SECRET"),
			Organization:  os.Getenv("CASDOOR_ORGANIZATION"),
			Application:   os.Getenv("CASDOOR_APPLICATION"),
			TLSSkipVerify: getEnvBool("CASDOOR_TLS_SKIP_VERIFY", false),
			Certificate:   os.Getenv("CASDOOR_CERTIFICATE"),
		}

		if err := validateCasdoorConfig(cfg.Casdoor); err != nil {
			return nil, fmt.Errorf("casdoor config validation failed: %w", err)
		}
	}

	return cfg, nil
}

// needsNativeConfig checks if native configuration is required.
func needsNativeConfig(cfg *PluginConfig) bool {
	return strings.Contains(cfg.AuthProvider, "native") ||
		strings.Contains(cfg.UserManager, "native") ||
		strings.Contains(cfg.TenantManager, "native") ||
		strings.Contains(cfg.ApprovalProvider, "native")
}

// needsCasdoorConfig checks if Casdoor configuration is required.
func needsCasdoorConfig(cfg *PluginConfig) bool {
	return strings.Contains(cfg.AuthProvider, "casdoor") ||
		strings.Contains(cfg.UserManager, "casdoor") ||
		strings.Contains(cfg.TenantManager, "casdoor")
}

// validateNativeConfig validates native provider configuration.
func validateNativeConfig(cfg *NativeConfig) error {
	if cfg == nil {
		return fmt.Errorf("native config is nil")
	}

	if strings.TrimSpace(cfg.DatabaseDSN) == "" {
		return fmt.Errorf("DATABASE_DSN is required for native provider")
	}

	if strings.TrimSpace(cfg.JWTSecret) == "" {
		return fmt.Errorf("JWT_SECRET is required for native provider")
	}

	if len(cfg.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters (got %d)", len(cfg.JWTSecret))
	}

	// Encryption key is optional but if provided must be exactly 32 bytes
	if len(cfg.EncryptionKey) > 0 && len(cfg.EncryptionKey) != 32 {
		return fmt.Errorf("ENCRYPTION_KEY must be exactly 32 bytes for AES-256 (got %d)", len(cfg.EncryptionKey))
	}

	return nil
}

// validateCasdoorConfig validates Casdoor provider configuration.
func validateCasdoorConfig(cfg *CasdoorConfig) error {
	if cfg == nil {
		return fmt.Errorf("casdoor config is nil")
	}

	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("CASDOOR_ENDPOINT is required")
	}

	if !strings.HasPrefix(cfg.Endpoint, "http://") && !strings.HasPrefix(cfg.Endpoint, "https://") {
		return fmt.Errorf("CASDOOR_ENDPOINT must start with http:// or https://")
	}

	if strings.TrimSpace(cfg.ClientID) == "" {
		return fmt.Errorf("CASDOOR_CLIENT_ID is required")
	}

	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return fmt.Errorf("CASDOOR_CLIENT_SECRET is required")
	}

	if strings.TrimSpace(cfg.Organization) == "" {
		return fmt.Errorf("CASDOOR_ORGANIZATION is required")
	}

	if strings.TrimSpace(cfg.Application) == "" {
		return fmt.Errorf("CASDOOR_APPLICATION is required")
	}

	// Warn if TLS verification is disabled in production
	if cfg.TLSSkipVerify && !isDevelopment() {
		// Note: Just validation, actual warning should be logged by caller
	}

	return nil
}

// getEnvOrDefault returns the environment variable value or a default.
func getEnvOrDefault(key, defaultValue string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return defaultValue
}

// getEnvBool returns the environment variable as a boolean.
func getEnvBool(key string, defaultValue bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return defaultValue
	}

	// Parse as boolean
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

// isDevelopment checks if running in development environment.
func isDevelopment() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	return env == "dev" || env == "development" || env == "local"
}

// DefaultConfig returns a default configuration suitable for local development.
func DefaultConfig() *PluginConfig {
	return &PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
		Native: &NativeConfig{
			DatabaseDSN:   "postgres://localhost:5432/llmgateway?sslmode=disable",
			JWTSecret:     "development-secret-min-32-characters-long",
			EncryptionKey: []byte("development-aes-key-32-bytes!"),
		},
	}
}

// ValidateConfig performs comprehensive validation of plugin configuration.
func ValidateConfig(cfg *PluginConfig) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Validate provider names are not empty
	if strings.TrimSpace(cfg.AuthProvider) == "" {
		return fmt.Errorf("AUTH_PROVIDER cannot be empty")
	}
	if strings.TrimSpace(cfg.UserManager) == "" {
		return fmt.Errorf("USER_MANAGER cannot be empty")
	}
	if strings.TrimSpace(cfg.TenantManager) == "" {
		return fmt.Errorf("TENANT_MANAGER cannot be empty")
	}
	if strings.TrimSpace(cfg.ApprovalProvider) == "" {
		return fmt.Errorf("APPROVAL_PROVIDER cannot be empty")
	}

	// Validate provider-specific configs
	if needsNativeConfig(cfg) {
		if cfg.Native == nil {
			return fmt.Errorf("native config is required but not provided")
		}
		if err := validateNativeConfig(cfg.Native); err != nil {
			return err
		}
	}

	if needsCasdoorConfig(cfg) {
		if cfg.Casdoor == nil {
			return fmt.Errorf("casdoor config is required but not provided")
		}
		if err := validateCasdoorConfig(cfg.Casdoor); err != nil {
			return err
		}
	}

	return nil
}
