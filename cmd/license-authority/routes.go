package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"

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

	// Secrets must be stable across restarts and must never use a public default.
	aesKey, err := loadSecretKey("LICENSE_AES_KEY", 32)
	if err != nil {
		panic(fmt.Sprintf("LICENSE_AES_KEY initialization failed: %v", err))
	}
	jwtSecret := []byte(getEnv("LICENSE_JWT_SECRET", ""))
	if len(jwtSecret) < 32 {
		panic("LICENSE_JWT_SECRET must be set to at least 32 bytes")
	}

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
	licenseGroup := adminGroup(api, "license")
	licenseHandler.RegisterRoutes(licenseGroup)

	// ── Center routes (/api/v1/instances/*) ───────────────────────────────
	centerStore := center.NewPgxStore(pool)
	centerServer := center.NewServer(centerStore)
	centerAPI := center.NewAdminAPI(centerServer, centerStore)
	instancesGroup := adminGroup(api, "")
	instanceClientGroup := api.Group("/instances")
	centerAPI.RegisterRoutes(instancesGroup)

	// ── Register endpoint ─────────────────────────────────────────────────
	registerHandler := NewRegisterHandler(licenseStore, centerStore, serverPrivKey)
	registerHandler.RegisterRoutes(instanceClientGroup)

	// ── Refresh token endpoint ────────────────────────────────────────────
	refreshHandler := NewRefreshHandler(centerStore, serverPrivKey)
	instanceClientGroup.POST("/refresh", refreshHandler.HandleRefresh)

	// ── Heartbeat endpoint ────────────────────────────────────────────────
	heartbeatHandler := NewHeartbeatHandler(centerStore, serverPubKey)
	heartbeatHandler.RegisterRoutes(instanceClientGroup)

	// ── Autoupdate routes (/api/v1/updates/*) ─────────────────────────────
	updateStore := autoupdate.NewPgxStore(pool)
	downloader := autoupdate.NewDownloader("/var/lib/kx-gateway/downloads")
	installer := autoupdate.NewInstaller("/var/lib/kx-gateway/kx-gateway", "/var/lib/kx-gateway/backups", "/var/lib/kx-gateway")
	rollback := autoupdate.NewRollback("/var/lib/kx-gateway/kx-gateway", "/var/lib/kx-gateway/backups", "/var/lib/kx-gateway")
	updateAPI := autoupdate.NewAdminAPI(updateStore, downloader, installer, rollback)
	updatesGroup := api.Group("/updates")
	updateAdminGroup := adminGroup(api, "updates")
	clientUpdatesGroup := instanceTokenGroup(updatesGroup, serverPubKey)
	updateAPI.RegisterRoutes(updateAdminGroup)

	// ── Update check endpoint ─────────────────────────────────────────────
	updateHandler := NewUpdateHandler(updateStore, serverPubKey)
	updateHandler.RegisterRoutes(clientUpdatesGroup)

	// ── Update report endpoint ────────────────────────────────────────────
	updateReportHandler := NewUpdateReportHandler(updateStore)
	updateReportHandler.RegisterRoutes(clientUpdatesGroup)

	// ── Manifest endpoint ─────────────────────────────────────────────────
	manifestHandler := NewManifestHandler(updateStore)
	manifestHandler.RegisterRoutes(clientUpdatesGroup)

	// ── Rollback endpoint ─────────────────────────────────────────────────
	rollbackHandler := NewRollbackHandler(updateStore)
	rollbackHandler.RegisterRoutes(updateAdminGroup)

}

func signedGroup(parent *echo.Group, lookup mw.ClientPublicKeyLookup, redisClient *redis.Client) *echo.Group {
	group := parent.Group("")
	if redisClient != nil {
		group.Use(mw.SignatureVerifierWithRedis(lookup, redisClient))
	} else {
		group.Use(mw.SignatureVerifier(lookup))
	}
	return group
}

func instanceTokenGroup(parent *echo.Group, serverPubKey ed25519.PublicKey) *echo.Group {
	group := parent.Group("")
	group.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			auth := c.Request().Header.Get("Authorization")
			if len(auth) <= len("Bearer ") || auth[:len("Bearer ")] != "Bearer " {
				return echo.NewHTTPError(401, "unauthorized")
			}
			claims, err := VerifyInstanceToken(auth[len("Bearer "):], serverPubKey)
			if err != nil {
				return echo.NewHTTPError(401, "unauthorized")
			}
			c.Set("instance_id", claims.Subject)
			return next(c)
		}
	})
	return group
}

func adminGroup(api *echo.Group, path string) *echo.Group {
	token := os.Getenv("LICENSE_AUTHORITY_ADMIN_TOKEN")
	if len(token) < 32 {
		panic("LICENSE_AUTHORITY_ADMIN_TOKEN must be set to at least 32 bytes")
	}
	if path == "" {
		return api.Group("", adminTokenMiddleware(token))
	}
	return api.Group("/"+path, adminTokenMiddleware(token))
}

func adminTokenMiddleware(expected string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			const prefix = "Bearer "
			auth := c.Request().Header.Get("Authorization")
			if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix || !equalSecret(auth[len(prefix):], expected) {
				return echo.NewHTTPError(401, "unauthorized")
			}
			return next(c)
		}
	}
}

func equalSecret(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	var diff byte
	for i := range got {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}

func loadSecretKey(name string, size int) ([]byte, error) {
	value := os.Getenv(name)
	if value == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("%s must be base64 and decode to %d bytes", name, size)
	}
	return decoded, nil
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
