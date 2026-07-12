// Command license-authority is the License Authority server entry point.
//
// Usage:
//
//	LICENSE_AUTHORITY_DATABASE_URL=postgres://... go run ./cmd/license-authority
//
// Configuration (environment variables):
//   - LICENSE_AUTHORITY_LISTEN: listen address (default :8443)
//   - LICENSE_AUTHORITY_DATABASE_URL: PostgreSQL connection string (required)
//   - LICENSE_AUTHORITY_LOG_LEVEL: log level (debug/info/warn/error, default info)
//   - LICENSE_AUTHORITY_DATA_DIR: directory for Ed25519 keys (default ./data)
package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

func main() {
	// ── Flags ─────────────────────────────────────────────────────────────
	listen := flag.String("listen", getEnv("LICENSE_AUTHORITY_LISTEN", ":8443"), "HTTP listen address")
	dbURL := flag.String("db", getEnv("LICENSE_AUTHORITY_DATABASE_URL", ""), "PostgreSQL connection URL")
	logLevel := flag.String("log-level", getEnv("LICENSE_AUTHORITY_LOG_LEVEL", "info"), "Log level (debug/info/warn/error)")
	flag.Parse()

	// ── Logging ───────────────────────────────────────────────────────────
	level := slog.LevelInfo
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	slog.Info("license-authority starting", "listen", *listen, "log_level", *logLevel)

	// ── Database ──────────────────────────────────────────────────────────
	if *dbURL == "" {
		slog.Error("LICENSE_AUTHORITY_DATABASE_URL is required")
		os.Exit(1)
	}

	ctx := context.Background()
	dbConn, err := db.Open(ctx, *dbURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer dbConn.Close()

	// ── Load or generate Ed25519 keys ─────────────────────────────────────
	dataDir := getEnv("LICENSE_AUTHORITY_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		slog.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}
	serverPrivKey, serverPubKey, err := LoadOrGenerateServerKeys(dataDir)
	if err != nil {
		slog.Error("failed to load/generate server keys", "error", err)
		os.Exit(1)
	}
	slog.Info("server keys loaded", "data_dir", dataDir, "public_key_size", len(serverPubKey))

	// ── Initialize Redis client (optional for nonce storage) ──────────────
	var redisClient *redis.Client
	redisURL := getEnv("LICENSE_AUTHORITY_REDIS_URL", "")
	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Warn("invalid REDIS_URL, falling back to in-memory nonce store", "error", err)
		} else {
			redisClient = redis.NewClient(opt)
			// Ping test
			pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
			if err := redisClient.Ping(pingCtx).Err(); err != nil {
				slog.Warn("redis ping failed, falling back to in-memory nonce store", "error", err)
				redisClient = nil
			}
			pingCancel()
		}
	}
	if redisClient == nil {
		slog.Info("using in-memory nonce store (single instance only)")
	}

	// ── Echo ──────────────────────────────────────────────────────────────
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// ── Routes ────────────────────────────────────────────────────────────
	registerRoutes(e, dbConn.Pool(), serverPrivKey, redisClient)

	// ── Start MonitorInstances goroutine ──────────────────────────────────
	centerStore := center.NewPgxStore(dbConn.Pool())
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	go center.MonitorInstances(monitorCtx, centerStore, 30*time.Second)
	slog.Info("instance monitor started", "interval", "30s")

	// ── Graceful Shutdown ─────────────────────────────────────────────────
	go func() {
		if err := e.Start(*listen); err != nil {
			slog.Info("server stopped", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		slog.Error("server shutdown failed", "error", err)
	}
	slog.Info("server exited")
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func registerRoutes(e *echo.Echo, pool *pgxpool.Pool, serverPrivKey ed25519.PrivateKey, redisClient *redis.Client) {
	// Health check
	e.GET("/api/v1/healthz", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	// API v1 routes - delegated to routes.go
	api := e.Group("/api/v1")
	setupAPIRoutes(api, pool, serverPrivKey, redisClient)
}
