package licensing

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type CenterHandler struct {
	activator      *Activator
	offlineManager *OfflineManager
	validator      *Validator
}

func NewCenterHandler(activator *Activator, offline *OfflineManager, validator *Validator) *CenterHandler {
	return &CenterHandler{
		activator:      activator,
		offlineManager: offline,
		validator:      validator,
	}
}

func (h *CenterHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/activate", h.Activate)
	g.POST("/deactivate", h.Deactivate)
	g.POST("/heartbeat", h.Heartbeat)
	g.POST("/validate", h.Validate)
	g.POST("/offline/request", h.CreateOfflineRequest)
	g.POST("/offline/verify", h.VerifyOfflineLicense)
}

func (h *CenterHandler) Activate(c echo.Context) error {
	var req ActivationRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	resp, err := h.activator.Activate(c.Request().Context(), &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, resp)
}

func (h *CenterHandler) Deactivate(c echo.Context) error {
	var req DeactivateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.activator.Deactivate(c.Request().Context(), &req); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "deactivated"})
}

func (h *CenterHandler) Heartbeat(c echo.Context) error {
	var req struct {
		LicenseKey   string `json:"license_key"`
		HardwareHash string `json:"hardware_hash"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.activator.Heartbeat(c.Request().Context(), req.LicenseKey, req.HardwareHash); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (h *CenterHandler) Validate(c echo.Context) error {
	var req struct {
		LicenseKey   string `json:"license_key"`
		HardwareHash string `json:"hardware_hash"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	signedLicense, err := h.activator.ValidateAndRefresh(c.Request().Context(), req.LicenseKey, req.HardwareHash)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"valid":          true,
		"signed_license": signedLicense,
	})
}

func (h *CenterHandler) CreateOfflineRequest(c echo.Context) error {
	var req OfflineRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	requestData, err := h.offlineManager.CreateOfflineRequest(c.Request().Context(), &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"request_id":   req.RequestID,
		"request_data": requestData,
	})
}

func (h *CenterHandler) VerifyOfflineLicense(c echo.Context) error {
	var req struct {
		SignedLicense string `json:"signed_license"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	lic, err := h.offlineManager.VerifyOfflineLicense(c.Request().Context(), req.SignedLicense)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"valid":   true,
		"license": lic,
	})
}
