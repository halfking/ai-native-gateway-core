package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/casdoor"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/native"
)

// initAuthRegistry creates and configures the authentication plugin registry.
// It registers providers based on environment variables:
//   - AUTH_PROVIDER: "native" (default) or "casdoor"
//   - USER_MANAGER: "native" (default) or "casdoor"
//   - TENANT_MANAGER: "native" (default) or "casdoor"
//
// Casdoor configuration (required if any Casdoor provider is used):
//   - CASDOOR_ENDPOINT: Casdoor server URL
//   - CASDOOR_CLIENT_ID: OAuth2 client ID
//   - CASDOOR_CLIENT_SECRET: OAuth2 client secret
//   - CASDOOR_ORGANIZATION: Organization name
//   - CASDOOR_APPLICATION: Application name
//   - CASDOOR_CERTIFICATE: (optional) Public certificate for JWT validation
func initAuthRegistry(db *pgxpool.Pool, jwtSecret string) (*authplugin.Registry, error) {
	if db == nil {
		return nil, fmt.Errorf("database pool is required for auth registry")
	}

	// Read provider configuration from environment
	authProviderType := getEnvOrDefault("AUTH_PROVIDER", "native")
	userManagerType := getEnvOrDefault("USER_MANAGER", "native")
	tenantManagerType := getEnvOrDefault("TENANT_MANAGER", "native")

	// Create registry with configured providers
	config := &authplugin.PluginConfig{
		AuthProvider:  authProviderType,
		UserManager:   userManagerType,
		TenantManager: tenantManagerType,
	}

	registry := authplugin.NewRegistry(config)

	// Check if any Casdoor provider is needed
	needsCasdoor := authProviderType == "casdoor" || userManagerType == "casdoor" || tenantManagerType == "casdoor"
	var casdoorConfig *authplugin.CasdoorConfig
	if needsCasdoor {
		var err error
		casdoorConfig, err = loadCasdoorConfig()
		if err != nil {
			return nil, fmt.Errorf("load casdoor config: %w", err)
		}
	}

	// Register auth provider
	if err := registerAuthProvider(registry, authProviderType, db, jwtSecret, casdoorConfig); err != nil {
		return nil, fmt.Errorf("register auth provider: %w", err)
	}

	// Register user manager
	if err := registerUserManager(registry, userManagerType, db, casdoorConfig); err != nil {
		return nil, fmt.Errorf("register user manager: %w", err)
	}

	// Register tenant manager
	if err := registerTenantManager(registry, tenantManagerType, db, casdoorConfig); err != nil {
		return nil, fmt.Errorf("register tenant manager: %w", err)
	}

	slog.Info("auth plugin registry initialized",
		"auth_provider", authProviderType,
		"user_manager", userManagerType,
		"tenant_manager", tenantManagerType)

	return registry, nil
}

// loadCasdoorConfig loads Casdoor configuration from environment variables.
func loadCasdoorConfig() (*authplugin.CasdoorConfig, error) {
	endpoint := os.Getenv("CASDOOR_ENDPOINT")
	clientID := os.Getenv("CASDOOR_CLIENT_ID")
	clientSecret := os.Getenv("CASDOOR_CLIENT_SECRET")
	organization := os.Getenv("CASDOOR_ORGANIZATION")
	application := os.Getenv("CASDOOR_APPLICATION")
	certificate := os.Getenv("CASDOOR_CERTIFICATE")

	// Validate required fields
	if endpoint == "" {
		return nil, fmt.Errorf("CASDOOR_ENDPOINT environment variable is required")
	}
	if clientID == "" {
		return nil, fmt.Errorf("CASDOOR_CLIENT_ID environment variable is required")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("CASDOOR_CLIENT_SECRET environment variable is required")
	}
	if organization == "" {
		return nil, fmt.Errorf("CASDOOR_ORGANIZATION environment variable is required")
	}
	if application == "" {
		return nil, fmt.Errorf("CASDOOR_APPLICATION environment variable is required")
	}

	return &authplugin.CasdoorConfig{
		Endpoint:     endpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Organization: organization,
		Application:  application,
		Certificate:  certificate,
	}, nil
}

// registerAuthProvider registers the configured authentication provider.
func registerAuthProvider(registry *authplugin.Registry, providerType string, db *pgxpool.Pool, jwtSecret string, casdoorConfig *authplugin.CasdoorConfig) error {
	switch providerType {
	case "native":
		provider, err := native.NewNativeAuthProvider(db, jwtSecret)
		if err != nil {
			return fmt.Errorf("create native auth provider: %w", err)
		}
		return registry.RegisterAuthProvider(provider)

	case "casdoor":
		if casdoorConfig == nil {
			return fmt.Errorf("casdoor config is required for casdoor auth provider")
		}
		provider, err := casdoor.NewCasdoorAuthProvider(casdoorConfig, db)
		if err != nil {
			return fmt.Errorf("create casdoor auth provider: %w", err)
		}
		return registry.RegisterAuthProvider(provider)

	default:
		return fmt.Errorf("unsupported auth provider type: %s", providerType)
	}
}

// registerUserManager registers the configured user manager.
func registerUserManager(registry *authplugin.Registry, managerType string, db *pgxpool.Pool, casdoorConfig *authplugin.CasdoorConfig) error {
	switch managerType {
	case "native":
		manager, err := native.NewNativeUserManager(db)
		if err != nil {
			return fmt.Errorf("create native user manager: %w", err)
		}
		return registry.RegisterUserManager(manager)

	case "casdoor":
		if casdoorConfig == nil {
			return fmt.Errorf("casdoor config is required for casdoor user manager")
		}
		manager, err := casdoor.NewCasdoorUserManager(casdoorConfig, db)
		if err != nil {
			return fmt.Errorf("create casdoor user manager: %w", err)
		}
		return registry.RegisterUserManager(manager)

	default:
		return fmt.Errorf("unsupported user manager type: %s", managerType)
	}
}

// registerTenantManager registers the configured tenant manager.
func registerTenantManager(registry *authplugin.Registry, managerType string, db *pgxpool.Pool, casdoorConfig *authplugin.CasdoorConfig) error {
	switch managerType {
	case "native":
		manager, err := native.NewNativeTenantManager(db)
		if err != nil {
			return fmt.Errorf("create native tenant manager: %w", err)
		}
		return registry.RegisterTenantManager(manager)

	case "casdoor":
		if casdoorConfig == nil {
			return fmt.Errorf("casdoor config is required for casdoor tenant manager")
		}
		manager, err := casdoor.NewCasdoorTenantManager(casdoorConfig)
		if err != nil {
			return fmt.Errorf("create casdoor tenant manager: %w", err)
		}
		return registry.RegisterTenantManager(manager)

	default:
		return fmt.Errorf("unsupported tenant manager type: %s", managerType)
	}
}

// getEnvOrDefault returns the environment variable value or the default if not set.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

