package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// RollbackRequest represents rollback request payload
type RollbackRequest struct {
	InstanceID  string `json:"instance_id"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Reason      string `json:"reason"`
}

// RollbackHandler handles rollback requests
type RollbackHandler struct {
	updateStore autoupdate.Store
}

// NewRollbackHandler creates a new rollback handler
func NewRollbackHandler(updateStore autoupdate.Store) *RollbackHandler {
	return &RollbackHandler{
		updateStore: updateStore,
	}
}

// HandleRollback handles POST /api/v1/updates/rollback
func (h *RollbackHandler) HandleRollback(c echo.Context) error {
	var req RollbackRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	// Validate required fields
	if req.InstanceID == "" || req.ToVersion == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing required fields: instance_id, to_version")
	}

	// Record rollback in upgrade_logs
	now := time.Now()
	logID, err := h.updateStore.CreateUpgradeLog(c.Request().Context(), req.InstanceID, req.FromVersion, req.ToVersion)
	if err != nil {
		slog.Error("failed to create rollback log",
			"error", err,
			"instance_id", req.InstanceID,
			"from_version", req.FromVersion,
			"to_version", req.ToVersion)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to record rollback")
	}

	// Update log with rolled_back status
	if err := h.updateStore.UpdateUpgradeLog(c.Request().Context(), logID, autoupdate.StatusRollback, req.Reason, now); err != nil {
		slog.Error("failed to update rollback log",
			"error", err,
			"log_id", logID,
			"instance_id", req.InstanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update rollback log")
	}

	// Update instance_release_status
	releaseStatus := &autoupdate.ReleaseStatus{
		InstanceID:  req.InstanceID,
		Status:      autoupdate.StatusRollback,
		Version:     req.ToVersion,
		StartedAt:   now,
		CompletedAt: &now,
		Error:       req.Reason,
		RetryCount:  0,
	}

	if err := h.updateStore.UpdateInstanceStatus(c.Request().Context(), releaseStatus); err != nil {
		slog.Error("failed to update instance status",
			"error", err,
			"instance_id", req.InstanceID,
			"status", autoupdate.StatusRollback)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update instance status")
	}

	slog.Info("rollback recorded",
		"instance_id", req.InstanceID,
		"from_version", req.FromVersion,
		"to_version", req.ToVersion,
		"reason", req.Reason)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"ack":     true,
		"message": "rollback recorded successfully",
	})
}

// RegisterRoutes registers rollback routes
func (h *RollbackHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/rollback", h.HandleRollback)
}
