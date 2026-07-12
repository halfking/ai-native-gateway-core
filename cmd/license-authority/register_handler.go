package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
)

// RegisterHandler handles instance registration
type RegisterHandler struct {
	licenseStore  licensing.Store
	centerStore   *center.PgxStore
	serverPrivKey ed25519.PrivateKey
	serverPubKey  ed25519.PublicKey
}

// NewRegisterHandler creates a new registration handler
func NewRegisterHandler(licenseStore licensing.Store, centerStore *center.PgxStore, serverPrivKey ed25519.PrivateKey) *RegisterHandler {
	serverPubKey := serverPrivKey.Public().(ed25519.PublicKey)
	return &RegisterHandler{
		licenseStore:  licenseStore,
		centerStore:   centerStore,
		serverPrivKey: serverPrivKey,
		serverPubKey:  serverPubKey,
	}
}

// HandleRegister handles POST /api/v1/instances/register
func (h *RegisterHandler) HandleRegister(c echo.Context) error {
	var req struct {
		InstanceID     string `json:"instance_id"`
		InstanceType   string `json:"instance_type"`
		DeploymentID   string `json:"deployment_id"`
		ReplicaCount   int    `json:"replica_count"`
		Hostname       string `json:"hostname"`
		IPAddress      string `json:"ip_address"`
		Version        string `json:"version"`
		LicenseKeyHash string `json:"license_key_hash"`
		HardwareHash   string `json:"hardware_hash"`
		PublicKey      string `json:"public_key"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	// 验证必填字段
	if req.InstanceID == "" || req.LicenseKeyHash == "" || req.HardwareHash == "" || req.PublicKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing required fields"})
	}

	// 验证 license 是否存在
	license, err := h.licenseStore.GetLicense(c.Request().Context(), req.LicenseKeyHash)
	if err != nil {
		slog.Error("get license failed", "error", err, "license_key_hash", req.LicenseKeyHash)
		return c.JSON(http.StatusNotFound, map[string]string{"error": "license not found"})
	}

	// 检查设备数限制
	activeDevices, err := h.licenseStore.CountActiveDevices(c.Request().Context(), req.LicenseKeyHash)
	if err != nil {
		slog.Error("count active devices failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}

	// 检查是否已存在该设备
	existingDevice, _ := h.licenseStore.GetDeviceByHardwareHash(c.Request().Context(), req.LicenseKeyHash, req.HardwareHash)

	// 如果是新设备且超过限制，返回 409
	if existingDevice == nil && activeDevices >= license.MaxDevices {
		return c.JSON(http.StatusConflict, map[string]string{"error": "device_limit_exceeded"})
	}

	// 生成 instance_token (7天有效期)
	instanceToken, err := SignInstanceToken(req.InstanceID, req.LicenseKeyHash, h.serverPrivKey)
	if err != nil {
		slog.Error("sign instance token failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign token"})
	}

	// 生成 refresh_token (30天有效期)
	refreshToken, err := GenerateRefreshToken()
	if err != nil {
		slog.Error("generate refresh token failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate refresh token"})
	}

	// 在同一事务中写入 gateway_instances 和 license_devices
	ctx := c.Request().Context()

	// 1. 注册实例
	instanceData := &center.InstanceInfo{
		InstanceID:     req.InstanceID,
		InstanceType:   req.InstanceType,
		DeploymentID:   req.DeploymentID,
		ReplicaCount:   req.ReplicaCount,
		Hostname:       req.Hostname,
		IPAddress:      req.IPAddress,
		Version:        req.Version,
		LicenseKeyHash: req.LicenseKeyHash,
		HardwareHash:   req.HardwareHash,
		PublicKey:      req.PublicKey,
		InstanceToken:  instanceToken,
		RefreshToken:   refreshToken,
		Status:         "online",
		StartedAt:      time.Now(),
	}

	if err := h.centerStore.RegisterInstance(ctx, instanceData); err != nil {
		slog.Error("register instance failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to register instance"})
	}

	// 2. 激活设备（如果尚未激活）
	if existingDevice == nil {
		device := &licensing.Device{
			LicenseID:    license.ID,
			InstanceID:   req.InstanceID,
			HardwareHash: req.HardwareHash,
			DeviceName:   req.Hostname,
			ActivatedAt:  time.Now(),
			Status:       "active",
		}

		if err := h.licenseStore.ActivateDevice(ctx, device); err != nil {
			slog.Error("activate device failed", "error", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to activate device"})
		}
	}

	// 返回响应
	serverPubKeyB64 := base64.StdEncoding.EncodeToString(h.serverPubKey)
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"instance_token":    instanceToken,
		"refresh_token":     refreshToken,
		"server_public_key": serverPubKeyB64,
		"expires_at":        expiresAt.Format(time.RFC3339),
	})
}

// RegisterRoutes registers the handler routes
func (h *RegisterHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/register", h.HandleRegister)
}
