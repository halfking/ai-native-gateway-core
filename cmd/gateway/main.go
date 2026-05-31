// Command gateway is the LLM Gateway Go data-plane entry point.
//
// Usage:
//
//	LLM_GATEWAY_LISTEN=:8781 LLM_GATEWAY_API_KEY=... go run ./cmd/gateway
//
// Environment variables:
//
//	LLM_GATEWAY_LISTEN                  TCP listen address (default ":8781")
//	LLM_GATEWAY_API_KEY                 Gateway API key for client auth (empty = disabled)
//	LLM_GATEWAY_LOG_LEVEL               Log level: debug, info, warn, error (default "info")
//	LLM_GATEWAY_DEFAULT_PROVIDER        Default provider ID (default "1")
//	LLM_GATEWAY_DEFAULT_CREDENTIAL      Default credential ID (default "1")
//	LLM_GATEWAY_PYTHON_ENDPOINT         Python control plane base URL (e.g. http://127.0.0.1:8780)
//	LLM_GATEWAY_ADMIN_API_KEY           Admin API key for Python control plane calls
//	LLM_GATEWAY_UPSTREAM                Upstream URL override (default http://127.0.0.1:8780)
//	LLM_GATEWAY_STREAM_CHUNK_TIMEOUT    Per-SSE-chunk idle timeout in seconds (default 300)
//	LLM_GATEWAY_STREAM_TIMEOUT          Total streaming request timeout in seconds (default 900)
//	LLM_GATEWAY_UPSTREAM_TIMEOUT        Non-streaming request timeout in seconds (default 120)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/relay"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/telemetry"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstream "github.com/kaixuan/llm-gateway-go/upstream"
)

func main() {
	// ── Logging ───────────────────────────────────────────────────────────
	level := slog.LevelInfo
	if l := os.Getenv("LLM_GATEWAY_LOG_LEVEL"); l != "" {
		switch l {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	// ── Dependencies ──────────────────────────────────────────────────────
	cfg := config.Load()
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Warn("postgres disabled", "error", err)
	}
	defer func() {
		if dbConn != nil {
			dbConn.Close()
		}
	}()

	cm := circuit.NewManager()
	lim := limiter.New()

	matrixPath := transform.DefaultMatrixPath()
	matrix := transform.New(matrixPath)

	pythonEndpoint := os.Getenv("LLM_GATEWAY_PYTHON_ENDPOINT")
	resolver := resolve.NewResolver(pythonEndpoint, 120*time.Second)

	auditSink := audit.NewMultiSink(
		&audit.LogSink{},
		audit.NewJSONSink(10000),
	)

	pools := pool.NewPoolManager()

	chatHandler := relay.NewChatHandler(cm, lim, matrix, pools, resolver, auditSink)
	healthHandler := relay.NewHealthHandler(cm, lim)
	modelsHandler := relay.NewModelsHandler(pythonEndpoint)
	messagesHandler := relay.NewMessagesHandler(chatHandler)
	responsesHandler := relay.NewResponsesHandler(chatHandler)

	// ── Session Manager ────────────────────────────────────────────────
	var sessionMgr *sessions.Manager
	if cfg.RedisAddr != "" {
		redisClient := sessions.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err := redisClient.Ping(context.Background()); err == nil {
			ttl := time.Duration(cfg.SessionTTLHours) * time.Hour
			sessionMgr = sessions.NewManager(redisClient, ttl)
			chatHandler.SetSessionGetter(sessionMgr)
			slog.Info("session manager enabled", "redis", cfg.RedisAddr, "ttl_hours", cfg.SessionTTLHours)
		} else {
			slog.Warn("session manager: redis ping failed", "error", err)
		}
	} else {
		slog.Warn("session manager disabled (no LLM_GATEWAY_REDIS_ADDR)")
	}

	// ── Routing executor (multi-candidate P2C) ──────────────────────────
	adminAPIKey := os.Getenv("LLM_GATEWAY_ADMIN_API_KEY")
	providerClient := provider.NewClient(pythonEndpoint, adminAPIKey)
	if dbConn != nil && dbConn.Enabled() {
		providerClient.SetDB(dbConn.Pool(), cfg.SecretKey, cfg.CredentialEncryptionKey)
	}
	if providerClient.Enabled() {
		stickyCache := routing.NewStickyCache()
		router := routing.NewRouter(stickyCache, lim)
		norm := relay.NewNormalizer()
		upClient := upstream.New()
		exec := routing.NewExecutor(
			router, cm, lim, pools, upClient,
			norm.NormalizeChunk,
			func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, normFunc routing.NormalizerFunc, capture *audit.StreamCapture) {
				relay.StreamChatWithCapture(w, resp, clientModel, outboundModel, norm, capture)
			},
			auditSink,
		)
		exec.StreamTimeout = relay.StreamTimeout()
		exec.UpstreamTimeout = relay.UpstreamTimeout()
		if dbConn != nil && dbConn.Enabled() {
			exec.State = credentialstate.NewWriter(dbConn.Pool())
		}
		chatHandler.SetExecutor(exec, providerClient, stickyCache)
		slog.Info("routing executor enabled", "endpoint", pythonEndpoint)
	} else {
		slog.Warn("routing executor disabled (no LLM_GATEWAY_ADMIN_API_KEY or LLM_GATEWAY_PYTHON_ENDPOINT)")
	}

	// ── Auth + Rate Limiting ──────────────────────────────────────────────
	keyVerifier := auth.NewKeyVerifier(pythonEndpoint, adminAPIKey)
	if dbConn != nil && dbConn.Enabled() {
		keyVerifier.SetDB(dbConn.Pool(), cfg.SecretKey)
	}
	if keyVerifier.Enabled() {
		slidingRL := ratelimit.NewSlidingWindowLimiter()
		chatHandler.SetAuth(keyVerifier, slidingRL)
		slog.Info("API key authentication + RPM rate limiting enabled")
	} else {
		slog.Warn("API key authentication disabled (no admin key or Python endpoint)")
	}

	// ── Telemetry ─────────────────────────────────────────────────────────
	telemetryClient := telemetry.NewClient(pythonEndpoint, adminAPIKey)
	if dbConn != nil && dbConn.Enabled() {
		telemetryClient.SetDB(dbConn.Pool())
	}
	if telemetryClient.Enabled() {
		chatHandler.SetTelemetry(telemetryClient)
		slog.Info("telemetry emission enabled")
	}

	// ── Model Discovery ─────────────────────────────────────────────────
	var discoverySvc *discovery.Service
	if dbConn != nil && dbConn.Enabled() {
		modelsHandler.SetDB(dbConn.Pool())
		discoverySvc = discovery.NewService(dbConn.Pool(), 1*time.Hour)
		discoverySvc.Start(context.Background())
		slog.Info("model discovery service enabled")
	}

	// ── Admin API ───────────────────────────────────────────────────────
	var adminHandler *admin.Handler
	var fernetKey []byte
	if dbConn != nil && dbConn.Enabled() {
		var ferr error
		fernetKey, ferr = secret.FernetKeyFromSecret(cfg.SecretKey, cfg.CredentialEncryptionKey)
		if ferr != nil {
			slog.Warn("admin API: fernet key unavailable", "error", ferr)
			fernetKey = nil
		}
		adminHandler = admin.NewHandler(dbConn.Pool(), cfg.SecretKey, fernetKey)
		if discoverySvc != nil {
			adminHandler.SetDiscoveryService(discoverySvc)
		}
	}

	// ── Background Services ─────────────────────────────────────────────
	var credRecovery *bg.CredentialRecovery
	var credCycler *bg.CredentialCycler
	var stickyCleaner *bg.StickyCleaner
	var envelopeCleaner *bg.EnvelopeCleaner
	var taxonomySync *bg.TaxonomySync
	if dbConn != nil && dbConn.Enabled() {
		credRecovery = bg.NewCredentialRecovery(dbConn.Pool())
		credRecovery.Start(context.Background())
		if fernetKey != nil {
			credCycler = bg.NewCredentialCycler(dbConn.Pool(), fernetKey)
			credCycler.Start(context.Background())
			slog.Info("credential cycler started")
		}
		stickyCleaner = bg.NewStickyCleaner(dbConn.Pool())
		stickyCleaner.Start(context.Background())
		envelopeCleaner = bg.NewEnvelopeCleaner(dbConn.Pool())
		envelopeCleaner.Start(context.Background())
		taxonomySync = bg.NewTaxonomySync(dbConn.Pool(), "")
		taxonomySync.Start(context.Background())
		slog.Info("taxonomy sync started")

		if adminHandler != nil {
			adminHandler.SetBackgroundServices(credCycler, credRecovery, envelopeCleaner, stickyCleaner, taxonomySync)
		}
	}

	// ── Listen address ────────────────────────────────────────────────────
	listen := os.Getenv("LLM_GATEWAY_LISTEN")
	if listen == "" {
		listen = ":8781"
	}

	// ── Static files (Vue SPA) ───────────────────────────────────────────
	staticDir := os.Getenv("LLM_GATEWAY_STATIC_DIR")
	if staticDir == "" {
		staticDir = "web/dist"
	}
	staticHandler := relay.NewStaticHandler(staticDir)

	// ── Router ────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Health
	mux.Handle("/healthz", healthHandler)

	// Chat completions
	mux.Handle("/v1/chat/completions", chatHandler)
	mux.Handle("/v1/completions", chatHandler)

	// Anthropic Messages API
	mux.Handle("/v1/messages", messagesHandler)

	// OpenAI Responses API
	mux.Handle("/v1/responses", responsesHandler)

	// Models listing
	mux.Handle("/v1/models", modelsHandler)

	// Session management
	if sessionMgr != nil {
		sessionHandler := sessions.NewHandler(sessionMgr)
		mux.Handle("/v1/sessions", sessionHandler)
		mux.Handle("/v1/sessions/", sessionHandler)
		slog.Info("session endpoints enabled")
	}

	// Static files / SPA fallback
	if staticHandler != nil {
		mux.Handle("/", staticHandler)
		slog.Info("serving Vue SPA", "dir", staticDir)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"service":"llm-gateway-go","version":"0.3.0"}`))
				return
			}
			http.NotFound(w, r)
		})
	}

	// Admin API routes
	if adminHandler != nil {
		adminHandler.RegisterRoutes(mux)
		slog.Info("admin API enabled")
	}

	// ── Middleware stack ──────────────────────────────────────────────────
	handler := middleware.APIKeyAuth(mux)
	handler = middleware.WithRequestID(handler)
	handler = middleware.WithLogging(handler)
	handler = middleware.CORS(handler)
	handler = middleware.WithRecovery(handler)

	srv := &http.Server{
		Addr:           listen,
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   0,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("gateway starting", "listen", listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("gateway listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("gateway shutting down")

	// Stop discovery service first
	if discoverySvc != nil {
		discoverySvc.Stop()
	}
	if credRecovery != nil {
		credRecovery.Stop()
	}
	if credCycler != nil {
		credCycler.Stop()
	}
	if taxonomySync != nil {
		taxonomySync.Stop()
	}
	if stickyCleaner != nil {
		stickyCleaner.Stop()
	}
	if envelopeCleaner != nil {
		envelopeCleaner.Stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("gateway shutdown error", "error", err)
	}
	telemetryClient.Stop()
	lim.Stop()
	pools.Stop()
	pools.CloseAll()
	slog.Info("gateway stopped")
}
