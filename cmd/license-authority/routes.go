package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
)

// setupAPIRoutes registers all /api/v1/* routes.
// Routes are mapped from traditional /api/admin/* to /api/v1/* for License Authority.
func setupAPIRoutes(api *echo.Group, pool *pgxpool.Pool, serverPrivKey ed25519.PrivateKey) {
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

	// ── Autoupdate routes (/api/v1/updates/*) ─────────────────────────────
	updateStore := autoupdate.NewPgxStore(pool)
	// Downloader, Installer, Rollback are nil for skeleton - will be implemented later
	updateAPI := autoupdate.NewAdminAPI(updateStore, nil, nil, nil)
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
}
