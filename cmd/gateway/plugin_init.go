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

	"github.com/kaixuan/llm-gateway-go/domains/approval"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/casdoor"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/native"
)

// initializePluginRegistry sets up the plugin registry with all available providers.
// It reads configuration from environment variables or database and registers
// both Native (PostgreSQL) and Casdoor providers.
//
// ★ 2026-11-27 L135 轮（断点③修复）：这是**全站唯一**的 authplugin.Registry
//
//	装配点。此前 main.go 有两条独立链——adminHandler 挂 initAuthRegistry（只读
//	env、三接口、无 ApprovalProvider），plugin-config API 挂本函数（读表+env、
//	四接口）⇒ plugin-config PUT 切换的 provider 对登录面永远不可见。
//	jwtSecretFallback 是 admin 面的 cfg.SecretKey，作为 native 签名链的回退键
//	（链：LLM_GATEWAY_JWT_SECRET → 此回退），与 admin.JWTExpiry 同一 SSOT。
func initializePluginRegistry(db *pgxpool.Pool, redisClient *redis.Client, jwtSecretFallback string) (*authplugin.Registry, error) {
	ctx := context.Background()

	// Read provider selection from database or environment
	config := loadPluginConfig(ctx, db, jwtSecretFallback)

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

// jwtSecretFromEnvOrFallback returns the admin-face JWT signing secret:
// LLM_GATEWAY_JWT_SECRET env first (the first link of the same chain admin
// jwtSecret uses), then the cfg.SecretKey fallback injected by the caller
// via loadPluginConfig's jwtSecretFallback parameter. Mirrors
// native.resolveJWTSecret / admin.jwtSecret — one chain, two faces.
func jwtSecretFromEnvOrFallback(fallback string) string {
	if s := os.Getenv("LLM_GATEWAY_JWT_SECRET"); s != "" {
		return s
	}
	return fallback
}

// loadPluginConfig reads plugin configuration from database or environment variables.
// Priority: Environment Variables > Database > Defaults
//
// ★ 2026-11-27 L135 轮（断点③修复 · 密钥源收口）：Native.JWTSecret 此前读
//
//	野生 `JWT_SECRET` env——部署面从不配置它 ⇒ native provider 的签名回退键
//	恒为空 ⇒ 一旦登录走 plugin 链（registry 非 nil），native 签发 token 全部
//	失败（resolveJWTSecret 返回 nil）。改为 jwtSecretFromEnv()：与 admin 面
//	同一条链（LLM_GATEWAY_JWT_SECRET → 调用点显式注入的 cfg.SecretKey 回退），
//	合并后 registry 与 admin 用同一把签名密钥，native 签发的 token 能过
//	AdminMiddleware → identity.Verify 的 legacy 验签路径。
func loadPluginConfig(ctx context.Context, db *pgxpool.Pool, jwtSecretFallback string) *authplugin.PluginConfig {
	config := &authplugin.PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
		Native: &authplugin.NativeConfig{
			DatabaseDSN:   os.Getenv("DATABASE_URL"),
			JWTSecret:     jwtSecretFromEnvOrFallback(jwtSecretFallback),
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
	if err := registerNativeApprovalProvider(registry, db, redisClient); err != nil {
		return err
	}

	slog.Info("native providers registered successfully")
	return nil
}

// registerNativeApprovalProvider registers the native (PostgreSQL-backed)
// approval provider.
//
// ★ 2026-11-23（211 生产告警修复）：这段注册此前整块被注释掉，而
// loadPluginConfig 的默认值是 ApprovalProvider="native" ⇒ registry 里
// 永远不会出现名为 "native" 的 approval provider ⇒ IsConfigured() 必挂 ⇒
// initializePluginRegistry 返回 "plugin registry not fully configured:
// approval provider not configured" ⇒ 211 生产启动日志恒有一条 ERROR。
//
// ★ 为什么现在能启用：approval domain 已经就位（domains/approval），
// 且其方法集与 native.ConfigManager 接口逐字一致
// （GetConfig/UpdateConfig/GetApprovers/AddApprover/UpdateApprover/
// RemoveApprover/GetRules/AddRule/RemoveRule）⇒ *approval.ConfigManager
// 天然满足该接口，无需适配层。
//
// ★ 注意构造函数名：不是 approval.NewStore（不存在），而是
// approval.NewPGApprovalStore(pool, cache)——它返回 ApprovalStore 接口，
// 正是 NewConfigManager 的第一个参数类型。
//
// ★ 为什么抽成具名函数：接线（"调用点还在不在、还真的注册了吗"）才是会
// 退化的那一环——构造体好测，"调用点有没有被顺手删掉/注释回去"只有具名
// 函数 + 行为测试才能钉死。这正是 AGENTS.md 的纪律：具名函数才能被测试点到。
func registerNativeApprovalProvider(
	registry *authplugin.Registry,
	db *pgxpool.Pool,
	redisClient *redis.Client,
) error {
	approvalStore := approval.NewPGApprovalStore(db, redisClient)
	configManager := approval.NewConfigManager(approvalStore, redisClient)
	nativeApproval := native.NewNativeApprovalProvider(configManager)
	if err := registry.RegisterApprovalProvider(nativeApproval); err != nil {
		return fmt.Errorf("register native approval provider: %w", err)
	}
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
