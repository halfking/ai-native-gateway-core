package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/examples/approvalconfig"
	"github.com/kaixuan/llm-gateway-go/examples/webhook_handler"
	"github.com/kaixuan/llm-gateway-go/internal/approval"
	"github.com/kaixuan/llm-gateway-go/internal/casdoor"
)

// appConfig 是**示例用的**应用配置骨架。
//
// ★ 示例不引入一个真实的 internal/config 包（网关并没有这样的包），
//
//	这里用最小结构体表达「你的应用配置里需要有哪些字段」，
//	真实项目请替换成你自己的配置类型。
type appConfig struct {
	// Approval 是审批系统配置（见 examples/approval_config）。
	Approval approvalconfig.ApprovalConfig
	// Database 是数据库连接配置。
	Database dbConfig
	// ApprovalTimeout 是原生审批超时时长。
	ApprovalTimeout time.Duration
}

// dbConfig 是**示例用的**数据库配置骨架。
type dbConfig struct {
	// URL 是 PostgreSQL 连接串。
	URL string
}

// initApprovalSystem initializes the dual-layer approval architecture.
//
// This function should be called during main application initialization,
// after database pool and config are loaded.
func initApprovalSystem(
	cfg approvalconfig.ApprovalConfig,
	pool *pgxpool.Pool,
	nativeApprovalManager *sessionaudit.ApprovalManager,
) (*approval.Router, *casdoor.Client, *approval.CasdoorProvider) {

	// 1. Initialize Casdoor client
	casdoorClient := casdoor.NewClient(casdoor.Config{
		Enabled:        cfg.Casdoor.Enabled,
		Endpoint:       cfg.Casdoor.Endpoint,
		ClientID:       cfg.Casdoor.ClientID,
		ClientSecret:   cfg.Casdoor.ClientSecret,
		CallbackSecret: cfg.Casdoor.CallbackSecret,
		CallbackKid:    cfg.Casdoor.CallbackKid,
		CallbackURL:    cfg.Casdoor.CallbackURL,
		TaskOwner:      cfg.Casdoor.TaskOwner,
		DefaultFlow:    cfg.Casdoor.DefaultFlow,
		TTLMinutes:     cfg.Casdoor.TTLMinutes,
	})

	// 2. Initialize approval store (for Casdoor task mapping)
	approvalStore := approval.NewPgxApprovalStore(pool)

	// 3. Initialize native provider (L1 fast approval)
	nativeProvider := approval.NewNativeProvider(
		nativeApprovalManager,
		cfg.Native.TimeoutMinutes,
	)

	// 4. Initialize Casdoor provider (L2 complex approval)
	casdoorProvider := approval.NewCasdoorProvider(
		casdoorClient,
		approvalStore,
	)

	// 5. Initialize router with sensitivity-based routing
	routerConfig := approval.RouterConfig{
		HighSensitivityThreshold:       cfg.Router.HighSensitivityThreshold,
		RequireCasdoorKeywords:         cfg.Router.RequireCasdoorKeywords,
		CrossDepartmentRequiresCasdoor: cfg.Router.CrossDepartmentRequiresCasdoor,
	}

	router := approval.NewRouter(routerConfig, nativeProvider, casdoorProvider)

	// 6. Log initialization status
	slog.Info("approval system initialized",
		"mode", cfg.Mode,
		"native_enabled", cfg.Native.Enabled,
		"native_timeout_minutes", cfg.Native.TimeoutMinutes,
		"casdoor_enabled", casdoorClient.IsEnabled(),
		"casdoor_endpoint", cfg.Casdoor.Endpoint,
		"high_sensitivity_threshold", cfg.Router.HighSensitivityThreshold,
	)

	// Log warning if in hybrid mode but Casdoor is not enabled
	if cfg.Mode == "hybrid" && !casdoorClient.IsEnabled() {
		slog.Warn("approval system in hybrid mode but Casdoor is disabled",
			"effective_mode", "native",
			"reason", "casdoor_not_configured",
		)
	}

	return router, casdoorClient, casdoorProvider
}

// Example main function integration
func main() {
	// 1. Load config
	cfg := loadConfig()

	// 2. Initialize database pool
	pool := initDBPool(cfg.Database)
	defer pool.Close()

	// 3. Initialize existing components
	nativeApprovalManager := sessionaudit.NewApprovalManager(pool, cfg.ApprovalTimeout)

	// 4. Initialize approval system
	approvalRouter, casdoorClient, casdoorProvider := initApprovalSystem(
		cfg.Approval,
		pool,
		nativeApprovalManager,
	)

	// 5. Initialize handlers
	handlers := initHandlers(approvalRouter, casdoorClient, casdoorProvider)

	// 6. Setup routes
	mux := setupRoutes(handlers)

	// 7. Start server
	startServer(mux)
}

// loadConfig loads application configuration.
func loadConfig() *appConfig {
	// Implementation depends on your config loading mechanism
	cfg := &appConfig{}
	// ... load from file and env ...
	return cfg
}

// initDBPool initializes PostgreSQL connection pool.
func initDBPool(dbConfig dbConfig) *pgxpool.Pool {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbConfig.URL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}

	slog.Info("database connection established",
		"max_conns", pool.Stat().MaxConns(),
	)

	return pool
}

// initHandlers initializes all HTTP handlers.
func initHandlers(
	approvalRouter *approval.Router,
	casdoorClient *casdoor.Client,
	casdoorProvider *approval.CasdoorProvider,
) *Handlers {
	// ... initialize other handlers ...

	return &Handlers{
		// ... other handlers ...
		ApprovalWebhook: webhookhandler.NewApprovalWebhookHandler(
			casdoorClient,
			casdoorProvider,
			approvalRouter,
		),
	}
}

// setupRoutes sets up HTTP routes.
func setupRoutes(handlers *Handlers) *http.ServeMux {
	mux := http.NewServeMux()

	// ... other routes ...

	// Casdoor approval webhook
	mux.HandleFunc("/api/webhooks/casdoor/approval",
		handlers.ApprovalWebhook.HandleCasdoorApprovalWebhook,
	)

	return mux
}

// startServer starts the HTTP server.
func startServer(mux *http.ServeMux) {
	addr := ":8080"
	slog.Info("starting server", "addr", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

type Handlers struct {
	// ... other handlers ...
	ApprovalWebhook *webhookhandler.ApprovalWebhookHandler
}
