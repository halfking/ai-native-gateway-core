package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/internal/collector"
	"github.com/labstack/echo/v4"
)

// CollectHandler ingests allowlisted runtime metrics from gateway instances.
type CollectHandler struct {
	store *center.PgxStore
}

// NewCollectHandler creates a runtime metrics ingest handler.
func NewCollectHandler(store *center.PgxStore) *CollectHandler {
	return &CollectHandler{store: store}
}

// HandleRuntimeMetrics handles POST /api/v1/collect/runtime.
func (h *CollectHandler) HandleRuntimeMetrics(c echo.Context) error {
	rawInstanceID, _ := c.Get("instance_id").(string)
	instanceID := strings.TrimPrefix(rawInstanceID, "instance:")
	if instanceID == "" || instanceID == rawInstanceID {
		return echo.NewHTTPError(http.StatusUnauthorized, "missing instance identity")
	}

	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "unable to read body")
	}
	if err := collector.ValidatePayload(body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var metrics collector.RuntimeMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid metrics payload")
	}
	if metrics.InstanceID != instanceID {
		return echo.NewHTTPError(http.StatusForbidden, "instance_id mismatch")
	}

	hardwareHash, err := h.store.GetInstanceHardwareHash(c.Request().Context(), instanceID)
	if err != nil {
		slog.Error("collect hardware hash lookup failed", "error", err, "instance_id", instanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "instance lookup failed")
	}
	if hardwareHash == "" {
		return echo.NewHTTPError(http.StatusNotFound, "instance not registered")
	}

	licenseID, enabled, found, err := h.store.LookupTelemetryPreference(c.Request().Context(), hardwareHash)
	if err != nil {
		slog.Error("collect telemetry preference lookup failed", "error", err, "instance_id", instanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "telemetry preference lookup failed")
	}
	if !found || !enabled {
		return echo.NewHTTPError(http.StatusForbidden, "runtime telemetry not enabled")
	}

	var licenseIDPtr *int64
	if licenseID > 0 {
		licenseIDPtr = &licenseID
	}
	if err := h.store.InsertRuntimeMetrics(c.Request().Context(), instanceID, licenseIDPtr, metrics); err != nil {
		slog.Error("collect insert metrics failed", "error", err, "instance_id", instanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store metrics")
	}
	if err := h.store.ProcessRuntimeAlerts(c.Request().Context(), metrics); err != nil {
		slog.Error("collect alert evaluation failed", "error", err, "instance_id", instanceID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to evaluate alerts")
	}

	return c.JSON(http.StatusOK, map[string]any{"accepted": true})
}

// RegisterRoutes registers collect ingest routes on the instance token group.
func (h *CollectHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/collect/runtime", h.HandleRuntimeMetrics)
}
