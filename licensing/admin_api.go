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

	// Offline activation requests (前端期望在 /licenses/ 下)
	g.GET("/licenses/offline-requests", h.ListOfflineRequests)
	g.POST("/licenses/offline-requests/:id/approve", h.ApproveOfflineRequest)
	g.POST("/licenses/offline-requests/:id/reject", h.RejectOfflineRequest)
}

func (h *AdminHandler) CreateLicense(c echo.Context) error {
	var req struct {
		LicenseKey       string   `json:"license_key"`
		CustomerName     string   `json:"customer_name"`
		Customer         string   `json:"customer"`
		CustomerEmail    string   `json:"customer_email"`
		MaxDevices       int      `json:"max_devices"`
		SubscriptionTier string   `json:"subscription_tier"`
		Features         []string `json:"features"`
		ExpiresAt        string   `json:"expires_at"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	// 兼容前端的 customer 字段
	if req.CustomerName == "" && req.Customer != "" {
		req.CustomerName = req.Customer
	}

	expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid expires_at format, expect RFC3339"})
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

	// 转换为前端期望的 DTO（包含 customer、active_devices、status 等字段）
	type LicenseDTO struct {
		ID               int64     `json:"id"`
		LicenseKey       string    `json:"license_key"`
		Customer         string    `json:"customer"`
		CustomerEmail    string    `json:"customer_email,omitempty"`
		MaxDevices       int       `json:"max_devices"`
		ActiveDevices    int       `json:"active_devices"`
		ExpiresAt        time.Time `json:"expires_at"`
		Status           string    `json:"status"`
		CreatedAt        time.Time `json:"created_at"`
		UpdatedAt        time.Time `json:"updated_at"`
		SubscriptionTier string    `json:"subscription_tier,omitempty"`
	}

	dtos := make([]LicenseDTO, 0, len(licenses))
	now := time.Now()
	for _, lic := range licenses {
		// 查询当前活跃设备数
		active, _ := h.store.CountActiveDevices(c.Request().Context(), lic.LicenseKey)
		// 推导 status
		status := "active"
		if lic.RevokedAt != nil {
			status = "revoked"
		} else if !lic.ExpiresAt.IsZero() && lic.ExpiresAt.Before(now) {
			status = "expired"
		}
		dtos = append(dtos, LicenseDTO{
			ID:               lic.ID,
			LicenseKey:       lic.LicenseKey,
			Customer:         lic.CustomerName,
			CustomerEmail:    lic.CustomerEmail,
			MaxDevices:       lic.MaxDevices,
			ActiveDevices:    active,
			ExpiresAt:        lic.ExpiresAt,
			Status:           status,
			CreatedAt:        lic.CreatedAt,
			UpdatedAt:        lic.CreatedAt, // 后端未维护 updated_at，回退到 created_at
			SubscriptionTier: lic.SubscriptionTier,
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  dtos,
		"total":  total,
		"offset": offset,
		"limit":  limit,
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

	// 转换为前端期望的 DTO
	type DeviceDTO struct {
		ID          int64      `json:"id"`
		LicenseID   int64      `json:"license_id"`
		DeviceID    string     `json:"device_id"`
		Hostname    string     `json:"hostname"`
		ActivatedAt time.Time  `json:"activated_at"`
		LastSeen    *time.Time `json:"last_seen,omitempty"`
		Status      string     `json:"status"`
	}

	dtos := make([]DeviceDTO, 0, len(devices))
	for _, d := range devices {
		// device_id = instance_id + ':' + hardware_hash 前 8 位
		shortHash := d.HardwareHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		dtos = append(dtos, DeviceDTO{
			ID:          d.ID,
			LicenseID:   d.LicenseID,
			DeviceID:    d.InstanceID + ":" + shortHash,
			Hostname:    d.DeviceName,
			ActivatedAt: d.ActivatedAt,
			LastSeen:    d.LastHeartbeat,
			Status:      d.Status,
		})
	}

	return c.JSON(http.StatusOK, dtos)
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

func (h *AdminHandler) ListOfflineRequests(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit == 0 {
		limit = 100
	}
	rows, err := h.store.ListOfflineRequests(c.Request().Context(), limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// 转换为前端期望的 DTO
	type OfflineReqDTO struct {
		ID             int64      `json:"id"`
		LicenseKey     string     `json:"license_key"`
		DeviceID       string     `json:"device_id"`
		RequestCode    string     `json:"request_code"`
		Status         string     `json:"status"`
		CreatedAt      time.Time  `json:"created_at"`
		ApprovedAt     *time.Time `json:"approved_at,omitempty"`
		ActivationCode string     `json:"activation_code,omitempty"`
	}

	dtos := make([]OfflineReqDTO, 0, len(rows))
	for _, r := range rows {
		dtos = append(dtos, OfflineReqDTO{
			ID:             r.ID,
			LicenseKey:     r.LicenseKey,
			DeviceID:       r.InstanceID,
			RequestCode:    r.RequestID,
			Status:         r.Status,
			CreatedAt:      r.CreatedAt,
			ApprovedAt:     r.ApprovedAt,
			ActivationCode: string(r.SignedLicense),
		})
	}

	return c.JSON(http.StatusOK, dtos)
}

func (h *AdminHandler) ApproveOfflineRequest(c echo.Context) error {
	requestID := c.Param("id")

	signedLicense, err := h.offlineManager.ApproveOfflineRequest(c.Request().Context(), requestID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	signedJSON, _ := json.Marshal(signedLicense)

	// 前端期望 { activation_code: string }，返回 base64 编码
	return c.JSON(http.StatusOK, map[string]interface{}{
		"activation_code": string(signedJSON),
		"signed_license":  signedLicense,
		"base64":          string(signedJSON),
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
