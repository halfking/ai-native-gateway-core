package licensing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	g.GET("/licenses/:id", h.GetLicense)
	g.POST("/licenses/:id/revoke", h.RevokeLicense)
	g.GET("/licenses/:id/devices", h.ListDevices)
	g.POST("/licenses/:id/devices/:hash/deactivate", h.DeactivateDevice)

	// Offline activation requests (前端期望在 /licenses/ 下)
	g.GET("/licenses/offline-requests", h.ListOfflineRequests)
	g.POST("/licenses/offline-requests/:id/approve", h.ApproveOfflineRequest)
	g.POST("/licenses/offline-requests/:id/reject", h.RejectOfflineRequest)
}

func generateLicenseKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("LIC-%s", hex.EncodeToString(b))
}

func (h *AdminHandler) CreateLicense(c echo.Context) error {
	var req struct {
		Customer   string `json:"customer"`
		MaxDevices int    `json:"max_devices"`
		ExpiresAt  string `json:"expires_at"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid expires_at format"})
	}

	lic := &License{
		LicenseKey:   generateLicenseKey(),
		CustomerName: req.Customer,
		MaxDevices:   req.MaxDevices,
		ExpiresAt:    expiresAt,
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

	if licenses == nil {
		licenses = []License{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"licenses": licenses,
		"total":    total,
		"offset":   offset,
		"limit":    limit,
	})
}

func (h *AdminHandler) GetLicense(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}

	lic, err := h.store.GetLicenseByID(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, lic)
}

func (h *AdminHandler) RevokeLicense(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}

	lic, err := h.store.GetLicenseByID(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "license not found"})
	}

	if err := h.store.RevokeLicense(c.Request().Context(), lic.LicenseKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.validator.InvalidateCache(lic.LicenseKey)

	return c.JSON(http.StatusOK, map[string]string{"message": "license revoked"})
}

func (h *AdminHandler) ListDevices(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}

	lic, err := h.store.GetLicenseByID(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "license not found"})
	}

	devices, err := h.store.ListAllDevices(c.Request().Context(), lic.LicenseKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if devices == nil {
		devices = []Device{}
	}

	return c.JSON(http.StatusOK, devices)
}

func (h *AdminHandler) DeactivateDevice(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}
	hardwareHash := c.Param("hash")

	lic, err := h.store.GetLicenseByID(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "license not found"})
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	deactivateReq := &DeactivateRequest{
		LicenseKey:   lic.LicenseKey,
		HardwareHash: hardwareHash,
		Reason:       req.Reason,
	}

	if err := h.activator.Deactivate(c.Request().Context(), deactivateReq); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "device deactivated"})
}

func (h *AdminHandler) ListOfflineRequests(c echo.Context) error {
	requests, err := h.store.ListOfflineRequests(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if requests == nil {
		requests = []OfflineRequest{}
	}
	return c.JSON(http.StatusOK, requests)
}

func (h *AdminHandler) ApproveOfflineRequest(c echo.Context) error {
	requestID := c.Param("id")

	signedLicense, err := h.offlineManager.ApproveOfflineRequest(c.Request().Context(), requestID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Generate a human-readable activation code from the signed license data
	activationCode := hex.EncodeToString(signedLicense.Data[:8])

	return c.JSON(http.StatusOK, map[string]interface{}{
		"activation_code": activationCode,
		"signed_license":  signedLicense,
	})
}

func (h *AdminHandler) RejectOfflineRequest(c echo.Context) error {
	requestID := c.Param("id")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.store.RejectOfflineRequest(c.Request().Context(), requestID, req.Reason); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message":    "offline request rejected",
		"request_id": requestID,
		"reason":     req.Reason,
	})
}
