package main

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// UpdateHandler handles update check requests
type UpdateHandler struct {
	store        autoupdate.Store
	serverPubKey ed25519.PublicKey
}

// NewUpdateHandler creates a new update handler
func NewUpdateHandler(store autoupdate.Store, serverPubKey ed25519.PublicKey) *UpdateHandler {
	return &UpdateHandler{
		store:        store,
		serverPubKey: serverPubKey,
	}
}

// RegisterRoutes registers update routes
func (h *UpdateHandler) RegisterRoutes(g *echo.Group) {
	g.GET("/latest", h.HandleCheckUpdates)
}

// HandleCheckUpdates handles GET /api/v1/updates/latest
func (h *UpdateHandler) HandleCheckUpdates(c echo.Context) error {
	// Extract Authorization header
	authHeader := c.Request().Header.Get("Authorization")
	if authHeader == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "missing Authorization header")
	}

	// Parse Bearer token
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid Authorization header format")
	}
	instanceToken := parts[1]

	// Verify JWT token
	_, err := VerifyInstanceToken(instanceToken, h.serverPubKey)
	if err != nil {
		slog.Warn("invalid instance token", "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid instance token")
	}

	// Parse query parameters
	currentVersion := c.QueryParam("current_version")
	if currentVersion == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "current_version is required")
	}

	channel := c.QueryParam("channel")
	if channel == "" {
		channel = "stable"
	}

	// Extract build_seq from current version (format: v1.13.0 -> use version comparison)
	// For now, we parse build_seq from query or extract from version string
	currentBuildSeq, err := extractBuildSeq(currentVersion)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid current_version format: %v", err))
	}

	// Query latest release
	release, err := h.store.GetLatestReleaseAfter(c.Request().Context(), autoupdate.Channel(channel), currentBuildSeq)
	if err != nil {
		// No newer version available
		return c.JSON(http.StatusOK, map[string]interface{}{
			"up_to_date": true,
		})
	}

	// Build manifest URL
	manifestURL := fmt.Sprintf("https://llm.kxpms.cn/api/v1/updates/manifest?v=%s", release.Version)

	// Return update information
	return c.JSON(http.StatusOK, map[string]interface{}{
		"version":       release.Version,
		"build_seq":     release.BuildSeq,
		"channel":       release.Channel,
		"mandatory":     release.Mandatory,
		"manifest_url":  manifestURL,
		"sha256":        release.ImageDigest,
		"size_mb":       calculateSizeMB(release),
		"release_notes": release.Changelog,
	})
}

// extractBuildSeq extracts build sequence from version string
// Supports formats: v1.13.0, v1.13.0-770, or plain 770
func extractBuildSeq(version string) (int, error) {
	// Try parsing as plain number first
	if seq, err := strconv.Atoi(version); err == nil {
		return seq, nil
	}

	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Split by '-' to get build_seq suffix (e.g., "1.13.0-770")
	parts := strings.Split(version, "-")
	if len(parts) == 2 {
		seq, err := strconv.Atoi(parts[1])
		if err == nil {
			return seq, nil
		}
	}

	// Fall back to semver comparison (use build_seq from VERSION file or default mapping)
	// For MVP, we'll use a simple mapping: v1.13.0 -> 770, v1.14.0 -> 800
	versionMap := map[string]int{
		"1.13.0": 770,
		"1.14.0": 800,
		"1.15.0": 850,
	}

	if seq, ok := versionMap[version]; ok {
		return seq, nil
	}

	return 0, fmt.Errorf("unable to extract build_seq from version: %s", version)
}

// calculateSizeMB estimates package size in MB
// For now, return a placeholder value
func calculateSizeMB(release *autoupdate.Release) int {
	// TODO: Calculate actual size from manifest or store in releases table
	return 128
}
