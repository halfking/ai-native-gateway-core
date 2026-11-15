// Package main initialization - Plugin Registry Setup
// This file contains the initialization code for the plugin-based architecture.
// Add this to your main.go or create a separate init file.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/casdoor"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/native"
)

// initializePluginRegistry sets up the plugin registry with all available providers.
// It reads configuration from environment variables or database and registers
// both Native (PostgreSQL) and Casdoor providers.
func initializePluginRegistry(db *pgxpool.Pool, redisClient *redis.Client) (*authplugin.Registry, error) {
	ctx := context.Background()

	// Read provider selection from database or environment
	config := loadPluginConfig(ctx, db)

	slog.Info("initializing plugin registry",
		"auth_provider", config.AuthProvider,
		"user_manager", config.UserManager,
		"tenant_manager", config.TenantManager,
		"approval_provider", config.ApprovalProvider,
	)

	// Create registry with configuration
	registry := authplugin.NewRegistry(config)

	// Register Native providers
	if err := registerNativeProviders(registry, db, redisClient, config); err != nil {
		return nil, fmt.Errorf("failed to register native providers: %w", err)
	}

	// Register Casdoor providers (if configured)
	if config.Casdoor != nil && config.Casdoor.Endpoint != "" {
		if err := registerCasdoorProviders(registry, db, config); err != nil {
			slog.Warn("failed to register casdoor providers", "error", err)
			// Don't fail - Casdoor might not be configured
		}
	}

	// Verify that configured providers are available
	if err := registry.IsConfigured(); err != nil {
		return nil, fmt.Errorf("plugin registry not fully configured: %w", err)
	}

	slog.Info("plugin registry initialized successfully",
		"summary", registry.Summary(),
	)

	return registry, nil
}

// loadPluginConfig reads plugin configuration from database or environment variables.
// Priority: Environment Variables > Database > Defaults
func loadPluginConfig(ctx context.Context, db *pgxpool.Pool) *authplugin.PluginConfig {
	config := &authplugin.PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
		Native: &authplugin.NativeConfig{
			DatabaseDSN:   os.Getenv("DATABASE_URL"),
			JWTSecret:     os.Getenv("JWT_SECRET"),
			EncryptionKey: []byte(os.Getenv("ENCRYPTION_KEY")),
		},
		Casdoor: &authplugin.CasdoorConfig{
			Endpoint:      os.Getenv("CASDOOR_ENDPOINT"),
			ClientID:      os.Getenv("CASDOOR_CLIENT_ID"),
			ClientSecret:  os.Getenv("CASDOOR_CLIENT_SECRET"),
			Organization:  os.Getenv("CASDOOR_ORGANIZATION"),
			Application:   os.Getenv("CASDOOR_APPLICATION"),
			TLSSkipVerify: os.Getenv("CASDOOR_TLS_SKIP_VERIFY") == "true",
			Certificate:   os.Getenv("CASDOOR_CERTIFICATE"),
		},
	}

	// Override from environment variables if set
	if envAuth := os.Getenv("AUTH_PROVIDER"); envAuth != "" {
		config.AuthProvider = envAuth
	}
	if envUser := os.Getenv("USER_MANAGER"); envUser != "" {
		config.UserManager = envUser
	}
	if envTenant := os.Getenv("TENANT_MANAGER"); envTenant != "" {
		config.TenantManager = envTenant
	}
	if envApproval := os.Getenv("APPROVAL_PROVIDER"); envApproval != "" {
		config.ApprovalProvider = envApproval
	}

	// If no environment variables set, try loading from database
	if os.Getenv("AUTH_PROVIDER") == "" && db != nil {
		dbConfig := loadConfigFromDatabase(ctx, db)
		if dbConfig != nil {
			config.AuthProvider = dbConfig.AuthProvider
			config.UserManager = dbConfig.UserManager
			config.TenantManager = dbConfig.TenantManager
			config.ApprovalProvider = dbConfig.ApprovalProvider
		}
	}

	return config
}

// loadConfigFromDatabase reads plugin configuration from plugin_config table.
func loadConfigFromDatabase(ctx context.Context, db *pgxpool.Pool) *authplugin.PluginConfig {
	var config authplugin.PluginConfig

	query := `
		SELECT auth_provider, user_manager, tenant_manager, approval_provider
		FROM plugin_config
		WHERE id = 1
		LIMIT 1
	`

	err := db.QueryRow(ctx, query).Scan(
		&config.AuthProvider,
		&config.UserManager,
		&config.TenantManager,
		&config.ApprovalProvider,
	)

	if err != nil {
		slog.Debug("no plugin config in database, using defaults", "error", err)
		return nil
	}

	slog.Info("loaded plugin config from database",
		"auth_provider", config.AuthProvider,
		"user_manager", config.UserManager,
		"tenant_manager", config.TenantManager,
		"approval_provider", config.ApprovalProvider,
	)

	return &config
}

// registerNativeProviders registers all Native (PostgreSQL) providers.
func registerNativeProviders(
	registry *authplugin.Registry,
	db *pgxpool.Pool,
	redisClient *redis.Client,
	config *authplugin.PluginConfig,
) error {
	// Native Auth Provider
	nativeAuth, err := native.NewNativeAuthProvider(db, config.Native.JWTSecret)
	if err != nil {
		return fmt.Errorf("create native auth provider: %w", err)
	}
	if err := registry.RegisterAuthProvider(nativeAuth); err != nil {
		return fmt.Errorf("register native auth provider: %w", err)
	}

	// Native User Manager
	nativeUser, err := native.NewNativeUserManager(db)
	if err != nil {
		return fmt.Errorf("create native user manager: %w", err)
	}
	if err := registry.RegisterUserManager(nativeUser); err != nil {
		return fmt.Errorf("register native user manager: %w", err)
	}

	// Native Tenant Manager
	nativeTenant, err := native.NewNativeTenantManager(db)
	if err != nil {
		return fmt.Errorf("create native tenant manager: %w", err)
	}
	if err := registry.RegisterTenantManager(nativeTenant); err != nil {
		return fmt.Errorf("register native tenant manager: %w", err)
	}

	// Native Approval Provider
	// Note: This assumes approval.ConfigManager exists in the codebase
	// The actual integration depends on the existing approval domain implementation
	// For now, we skip approval provider registration if the domain is not available
	// Uncomment and adjust the following when approval domain is ready:
	/*
		approvalStore := approval.NewStore(db)
		configManager := approval.NewConfigManager(approvalStore, redisClient)
		nativeApproval := native.NewNativeApprovalProvider(configManager)
		if err := registry.RegisterApprovalProvider(nativeApproval); err != nil {
			return fmt.Errorf("register native approval provider: %w", err)
		}
	*/

	slog.Info("native providers registered successfully")
	return nil
}

// registerCasdoorProviders registers all Casdoor providers.
func registerCasdoorProviders(
	registry *authplugin.Registry,
	db *pgxpool.Pool,
	config *authplugin.PluginConfig,
) error {
	// Casdoor Auth Provider
	casdoorAuth, err := casdoor.NewCasdoorAuthProvider(config.Casdoor, db)
	if err != nil {
		return fmt.Errorf("create casdoor auth provider: %w", err)
	}
	if err := registry.RegisterAuthProvider(casdoorAuth); err != nil {
		return fmt.Errorf("register casdoor auth provider: %w", err)
	}

	// Casdoor User Manager
	casdoorUser, err := casdoor.NewCasdoorUserManager(config.Casdoor, db)
	if err != nil {
		return fmt.Errorf("create casdoor user manager: %w", err)
	}
	if err := registry.RegisterUserManager(casdoorUser); err != nil {
		return fmt.Errorf("register casdoor user manager: %w", err)
	}

	// Casdoor Tenant Manager
	casdoorTenant, err := casdoor.NewCasdoorTenantManager(config.Casdoor)
	if err != nil {
		return fmt.Errorf("create casdoor tenant manager: %w", err)
	}
	if err := registry.RegisterTenantManager(casdoorTenant); err != nil {
		return fmt.Errorf("register casdoor tenant manager: %w", err)
	}

	// Casdoor Approval Provider (uses standard http.Client)
	httpClient := casdoor.NewHTTPClientWithDefaults()
	idMapper := casdoor.NewIDMapper(db)
	casdoorApproval := casdoor.NewCasdoorApprovalProvider(
		httpClient,
		config.Casdoor.Endpoint,
		config.Casdoor.ClientID,
		config.Casdoor.ClientSecret,
		idMapper,
		config.Casdoor.Organization,
		config.Casdoor.Application,
	)
	if err := registry.RegisterApprovalProvider(casdoorApproval); err != nil {
		return fmt.Errorf("register casdoor approval provider: %w", err)
	}

	slog.Info("casdoor providers registered successfully",
		"endpoint", config.Casdoor.Endpoint,
		"organization", config.Casdoor.Organization,
	)
	return nil
}

// Example usage in main():
//
// func main() {
//     // ... setup db, redis, etc ...
//
//     // Initialize plugin registry
//     registry, err := initializePluginRegistry(db, redisClient)
//     if err != nil {
//         log.Fatal("failed to initialize plugin registry:", err)
//     }
//
//     // Create handlers with registry support
//     handler := admin.NewHandler(db, secretKey)
//     handler.registry = registry  // Add registry field to Handler struct
//
//     // Update approval config handler to use registry
//     approvalConfigHandler := admin.NewApprovalConfigHandlerWithRegistry(
//         registry,
//         approval.NewConfigManager(approvalStore, redisClient), // Fallback
//     )
//
//     // Create plugin config handler
//     pluginConfigHandler := admin.NewPluginConfigHandler(registry, db)
//
//     // Register routes
//     mux.HandleFunc("/api/admin/plugin-config", func(w http.ResponseWriter, r *http.Request) {
//         if r.Method == http.MethodGet {
//             pluginConfigHandler.GetPluginConfig(w, r)
//         } else if r.Method == http.MethodPut {
//             pluginConfigHandler.UpdatePluginConfig(w, r)
//         }
//     })
//     mux.HandleFunc("/api/admin/plugin-providers", pluginConfigHandler.GetProviderStatus)
//
//     // ... rest of your main() ...
// }
