package licensing

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type AdminHandler struct {
	store          Store
	crypto         *CryptoConfig
	activator      *Activator
	offlineManager *OfflineManager
	validator      *Validator
}

func NewAdminHandler(store Store, crypto *CryptoConfig, activator *Activator, offline *OfflineManager, validator *Validator) *AdminHandler {
	return &AdminHandler{
		store:          store,
		crypto:         crypto,
		activator:      activator,
		offlineManager: offline,
		validator:      validator,
	}
}

func (h *AdminHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/licenses", h.CreateLicense)
	g.GET("/licenses", h.ListLicenses)
	g.GET("/licenses/:key", h.GetLicense)
	g.POST("/licenses/:key/revoke", h.RevokeLicense)
	g.GET("/licenses/:key/devices", h.ListDevices)
	g.POST("/licenses/:key/devices/:hash/deactivate", h.DeactivateDevice)
	g.POST("/offline-requests/:id/approve", h.ApproveOfflineRequest)
}

func (h *AdminHandler) CreateLicense(c echo.Context) error {
	var req struct {
		LicenseKey       string   `json:"license_key"`
		CustomerName     string   `json:"customer_name"`
		CustomerEmail    string   `json:"customer_email"`
		MaxDevices       int      `json:"max_devices"`
		SubscriptionTier string   `json:"subscription_tier"`
		Features         []string `json:"features"`
		ExpiresAt        string   `json:"expires_at"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid expires_at format"})
	}

	lic := &License{
		LicenseKey:       req.LicenseKey,
		CustomerName:     req.CustomerName,
		CustomerEmail:    req.CustomerEmail,
		MaxDevices:       req.MaxDevices,
		SubscriptionTier: req.SubscriptionTier,
		Features:         req.Features,
		ExpiresAt:        expiresAt,
	}

	if err := h.store.CreateLicense(c.Request().Context(), lic); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusCreated, lic)
}

func (h *AdminHandler) ListLicenses(c echo.Context) error {
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit == 0 {
		limit = 20
	}

	licenses, total, err := h.store.ListAllLicenses(c.Request().Context(), offset, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"licenses": licenses,
		"total":    total,
		"offset":   offset,
		"limit":    limit,
	})
}

func (h *AdminHandler) GetLicense(c echo.Context) error {
	licenseKey := c.Param("key")

	lic, err := h.store.GetLicense(c.Request().Context(), licenseKey)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, lic)
}

func (h *AdminHandler) RevokeLicense(c echo.Context) error {
	licenseKey := c.Param("key")

	if err := h.store.RevokeLicense(c.Request().Context(), licenseKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.validator.InvalidateCache(licenseKey)

	return c.JSON(http.StatusOK, map[string]string{"message": "license revoked"})
}

func (h *AdminHandler) ListDevices(c echo.Context) error {
	licenseKey := c.Param("key")

	devices, err := h.store.ListAllDevices(c.Request().Context(), licenseKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, devices)
}

func (h *AdminHandler) DeactivateDevice(c echo.Context) error {
	licenseKey := c.Param("key")
	hardwareHash := c.Param("hash")

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	deactivateReq := &DeactivateRequest{
		LicenseKey:   licenseKey,
		HardwareHash: hardwareHash,
		Reason:       req.Reason,
	}

	if err := h.activator.Deactivate(c.Request().Context(), deactivateReq); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "device deactivated"})
}

func (h *AdminHandler) ApproveOfflineRequest(c echo.Context) error {
	requestID := c.Param("id")

	signedLicense, err := h.offlineManager.ApproveOfflineRequest(c.Request().Context(), requestID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	signedJSON, _ := json.Marshal(signedLicense)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"signed_license": signedLicense,
		"base64":         string(signedJSON),
	})
}
