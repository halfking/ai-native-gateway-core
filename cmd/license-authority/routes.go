package main

import (
	"crypto/ed25519"

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

	// ── Licensing routes (/api/v1/license/*) ──────────────────────────────
	// AdminHandler requires Store, CryptoConfig, Activator, OfflineManager, Validator
	// For now, we instantiate minimal dependencies (nil placeholders for non-Store deps)
	licenseStore := licensing.NewPgxStore(pool)
	// TODO: Initialize CryptoConfig, Activator, OfflineManager, Validator properly
	// Leaving as nil for skeleton - will be implemented by Agent-B
	licenseHandler := licensing.NewAdminHandler(licenseStore, nil, nil, nil, nil)
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
}
