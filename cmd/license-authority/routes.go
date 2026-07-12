package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/kaixuan/llm-gateway-go/center"
	mw "github.com/kaixuan/llm-gateway-go/cmd/license-authority/middleware"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// setupAPIRoutes registers all /api/v1/* routes.
// Routes are mapped from traditional /api/admin/* to /api/v1/* for License Authority.
func setupAPIRoutes(api *echo.Group, pool *pgxpool.Pool, serverPrivKey ed25519.PrivateKey, redisClient *redis.Client) {
	serverPubKey := serverPrivKey.Public().(ed25519.PublicKey)

	// ── RSA Keys for Licensing CryptoConfig ───────────────────────────────
	dataDir := getEnv("LICENSE_AUTHORITY_DATA_DIR", "./data")
	rsaPrivKey, rsaPubKey, err := LoadOrCreateRSAKeys(dataDir)
	if err != nil {
		slog.Error("failed to load/create RSA keys for licensing", "error", err)
		panic(fmt.Sprintf("RSA key initialization failed: %v", err))
	}

	// ── Licensing routes (/api/v1/license/*) ──────────────────────────────
	licenseStore := licensing.NewPgxStore(pool)

	// Initialize CryptoConfig with RSA keys + AES key + JWT secret
	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		panic(fmt.Sprintf("failed to generate AES key: %v", err))
	}
	jwtSecret := []byte(getEnv("LICENSE_JWT_SECRET", "change-me-in-production"))

	cryptoConfig := &licensing.CryptoConfig{
		PrivateKey: rsaPrivKey,
		PublicKey:  rsaPubKey,
		AESKey:     aesKey,
		JWTSecret:  jwtSecret,
	}

	// Initialize Validator
	validator := licensing.NewValidator(cryptoConfig, licenseStore)

	// Initialize DeviceManager
	deviceManager := licensing.NewDeviceManager(licenseStore, validator)

	// Initialize Activator
	activator := licensing.NewActivator(cryptoConfig, licenseStore, deviceManager)

	// Initialize OfflineManager
	offlineManager := licensing.NewOfflineManager(cryptoConfig, licenseStore)

	// Initialize AdminHandler with all dependencies
	licenseHandler := licensing.NewAdminHandler(licenseStore, cryptoConfig, activator, offlineManager, validator)
	licenseGroup := api.Group("/license")
	licenseHandler.RegisterRoutes(licenseGroup)

	// ── Center routes (/api/v1/instances/*) ───────────────────────────────
	centerStore := center.NewPgxStore(pool)
	centerServer := center.NewServer(centerStore)
	centerAPI := center.NewAdminAPI(centerServer, centerStore)
	instancesGroup := api.Group("/instances")
	centerAPI.RegisterRoutes(instancesGroup)

	// ── Register endpoint ─────────────────────────────────────────────────
	registerHandler := NewRegisterHandler(licenseStore, centerStore, serverPrivKey)
	registerHandler.RegisterRoutes(instancesGroup)

	// ── Refresh token endpoint ────────────────────────────────────────────
	refreshHandler := NewRefreshHandler(centerStore, serverPrivKey)
	instancesGroup.POST("/refresh", refreshHandler.HandleRefresh)

	// ── Heartbeat endpoint ────────────────────────────────────────────────
	heartbeatHandler := NewHeartbeatHandler(centerStore, serverPubKey)
	heartbeatHandler.RegisterRoutes(instancesGroup)

	// ── Signature verification middleware on /api/v1/instances/* ─────────
	// Mounts Redis-backed signature verifier (or in-memory if redisClient is nil)
	clientPubKeyLookup := func(instanceID string) (ed25519.PublicKey, error) {
		instance, err := centerStore.GetInstance(ctxFromContext(api), instanceID)
		if err != nil {
			return nil, err
		}
		// Decode base64 public key
		return parsePublicKey(instance.PublicKey)
	}
	if redisClient != nil {
		instancesGroup.Use(mw.SignatureVerifierWithRedis(clientPubKeyLookup, redisClient))
		slog.Info("Redis-backed signature verifier attached to /api/v1/instances/*")
	}
	// /heartbeat uses JWT, not Ed25519; skip middleware for that path

	// ── Autoupdate routes (/api/v1/updates/*) ─────────────────────────────
	updateStore := autoupdate.NewPgxStore(pool)
	downloader := autoupdate.NewDownloader("/var/lib/kx-gateway/downloads")
	installer := autoupdate.NewInstaller("/var/lib/kx-gateway/kx-gateway", "/var/lib/kx-gateway/backups", "/var/lib/kx-gateway")
	rollback := autoupdate.NewRollback("/var/lib/kx-gateway/kx-gateway", "/var/lib/kx-gateway/backups", "/var/lib/kx-gateway")
	updateAPI := autoupdate.NewAdminAPI(updateStore, downloader, installer, rollback)
	updatesGroup := api.Group("/updates")
	updateAPI.RegisterRoutes(updatesGroup)

	// ── Update check endpoint ─────────────────────────────────────────────
	updateHandler := NewUpdateHandler(updateStore, serverPubKey)
	updateHandler.RegisterRoutes(updatesGroup)

	// ── Update report endpoint ────────────────────────────────────────────
	updateReportHandler := NewUpdateReportHandler(updateStore)
	updateReportHandler.RegisterRoutes(updatesGroup)

	// ── Manifest endpoint ─────────────────────────────────────────────────
	manifestHandler := NewManifestHandler(updateStore)
	manifestHandler.RegisterRoutes(updatesGroup)

	// ── Rollback endpoint ─────────────────────────────────────────────────
	rollbackHandler := NewRollbackHandler(updateStore)
	rollbackHandler.RegisterRoutes(updatesGroup)

	// Signature verifier for /api/v1/updates/* (uses instance_token in JWT)
	if redisClient != nil {
		updatesGroup.Use(mw.SignatureVerifierWithRedis(clientPubKeyLookup, redisClient))
		slog.Info("Redis-backed signature verifier attached to /api/v1/updates/*")
	}
}

// ctxFromContext extracts a context from echo.Group's request scope.
// echo.Group itself doesn't carry a context, so we use context.Background() here;
// the middleware will use the per-request context via c.Request().Context().
func ctxFromContext(_ *echo.Group) context.Context {
	return context.Background()
}

func parsePublicKey(b64 string) (ed25519.PublicKey, error) {
	if b64 == "" {
		return nil, fmt.Errorf("empty public key")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: %d", len(data))
	}
	return ed25519.PublicKey(data), nil
}
