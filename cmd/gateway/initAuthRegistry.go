package main

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/native"
)

// initAuthRegistry creates and configures the authentication plugin registry.
// It registers all native providers (auth, user, tenant) with the registry.
func initAuthRegistry(db *pgxpool.Pool, jwtSecret string) (*authplugin.Registry, error) {
	if db == nil {
		return nil, fmt.Errorf("database pool is required for auth registry")
	}
	
	// Create registry with default configuration
	config := &authplugin.PluginConfig{
		AuthProvider:   "native",
		UserManager:    "native",
		TenantManager:  "native",
	}
	
	registry := authplugin.NewRegistry(config)
	
	// Create and register native auth provider
	nativeAuth, err := native.NewNativeAuthProvider(db, jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("create native auth provider: %w", err)
	}
	if err := registry.RegisterAuthProvider(nativeAuth); err != nil {
		return nil, fmt.Errorf("register native auth provider: %w", err)
	}
	
	// Create and register native user manager
	nativeUser, err := native.NewNativeUserManager(db)
	if err != nil {
		return nil, fmt.Errorf("create native user manager: %w", err)
	}
	if err := registry.RegisterUserManager(nativeUser); err != nil {
		return nil, fmt.Errorf("register native user manager: %w", err)
	}
	
	// Create and register native tenant manager
	nativeTenant, err := native.NewNativeTenantManager(db)
	if err != nil {
		return nil, fmt.Errorf("create native tenant manager: %w", err)
	}
	if err := registry.RegisterTenantManager(nativeTenant); err != nil {
		return nil, fmt.Errorf("register native tenant manager: %w", err)
	}
	
	slog.Info("auth plugin registry initialized",
		"auth_provider", "native",
		"user_manager", "native",
		"tenant_manager", "native")
	
	return registry, nil
}

