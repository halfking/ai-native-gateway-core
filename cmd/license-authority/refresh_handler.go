package main

import (
	"crypto/ed25519"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/labstack/echo/v4"
)

// RefreshHandler handles POST /api/v1/instances/refresh
type RefreshHandler struct {
	store         center.Store
	serverPrivKey ed25519.PrivateKey
}

// NewRefreshHandler creates a new refresh handler
func NewRefreshHandler(store center.Store, serverPrivKey ed25519.PrivateKey) *RefreshHandler {
	return &RefreshHandler{
		store:         store,
		serverPrivKey: serverPrivKey,
	}
}

// RefreshRequest is the request body for refresh endpoint
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// RefreshResponse is the response body for refresh endpoint
type RefreshResponse struct {
	InstanceToken string `json:"instance_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresIn     int    `json:"expires_in"` // seconds
}

// HandleRefresh handles the refresh token request
func (h *RefreshHandler) HandleRefresh(c echo.Context) error {
	ctx := c.Request().Context()

	// Parse request
	var req RefreshRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	if req.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "refresh_token is required"})
	}

	// Lookup instance by refresh_token
	instance, err := h.store.GetInstanceByRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired refresh_token"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "database error"})
	}

	newRefreshToken, err := GenerateRefreshToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate refresh token"})
	}
	// Rotate the bearer credential so a leaked token cannot be replayed indefinitely.
	if err := h.store.UpdateRefreshToken(ctx, instance.InstanceID, newRefreshToken, time.Now().Add(90*24*time.Hour)); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to rotate refresh token"})
	}
	instanceToken, err := SignInstanceToken(instance.InstanceID, instance.LicenseKeyHash, h.serverPrivKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign token"})
	}

	// Return new token
	return c.JSON(http.StatusOK, RefreshResponse{
		InstanceToken: instanceToken,
		RefreshToken:  newRefreshToken,
		ExpiresIn:     7 * 24 * 60 * 60, // 7 days in seconds
	})
}
