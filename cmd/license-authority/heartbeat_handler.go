package main

import (
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/labstack/echo/v4"
)

// HeartbeatHandler handles instance heartbeat requests
type HeartbeatHandler struct {
	store        *center.PgxStore
	serverPubKey ed25519.PublicKey
}

// NewHeartbeatHandler creates a new heartbeat handler
func NewHeartbeatHandler(store *center.PgxStore, serverPubKey ed25519.PublicKey) *HeartbeatHandler {
	return &HeartbeatHandler{
		store:        store,
		serverPubKey: serverPubKey,
	}
}

// HandleHeartbeat handles POST /api/v1/instances/heartbeat
func (h *HeartbeatHandler) HandleHeartbeat(c echo.Context) error {
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
	claims, err := VerifyInstanceToken(instanceToken, h.serverPubKey)
	if err != nil {
		slog.Warn("invalid instance token", "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid instance token")
	}

	// Extract instance_id from JWT subject (format: "instance:<id>")
	instanceID := strings.TrimPrefix(claims.Subject, "instance:")
	if instanceID == claims.Subject {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid token subject format")
	}

	// Parse heartbeat payload
	var payload center.HeartbeatPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	// Record heartbeat (writes to instance_heartbeats + updates gateway_instances.last_heartbeat)
	if err := h.store.RecordHeartbeat(c.Request().Context(), instanceID, &payload); err != nil {
		slog.Error("failed to record heartbeat", "error", err, "instance_id", instanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to record heartbeat")
	}

	slog.Debug("heartbeat recorded", "instance_id", instanceID, "uptime_secs", payload.UptimeSecs)

	// Return acknowledgment
	return c.JSON(http.StatusOK, map[string]interface{}{
		"ack":                    true,
		"next_heartbeat_in_secs": 60,
	})
}

// RegisterRoutes registers the heartbeat handler routes
func (h *HeartbeatHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/heartbeat", h.HandleHeartbeat)
}
