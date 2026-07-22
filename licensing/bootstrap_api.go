package licensing

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
)

// BootstrapHandler exposes local first-boot APIs that work before admin login.
// Online activation talks to ai-native-maintain; offline import verifies locally.
type BootstrapHandler struct {
	Validator *Validator
	Activator *Activator
	Offline   *OfflineManager
	Store     Store
	// publicKeyConfigured: 中心公钥是否已配置在 OfflineManager.crypto 中。
	// true = 严格验签；false = 信任路径（仅做 trace，不返回错误）
	publicKeyConfigured bool

	centerURL string
	agent     centerAgentLoop
}

// NewBootstrapHandler wires bootstrap endpoints. Center URL is resolved from env.
// publicKeyConfigured 应传入 OfflineManager.crypto != nil && PublicKey != nil
// 反映应用是否启用了 license 局部验签；未启用时降级为「信任路径」。
func NewBootstrapHandler(validator *Validator, activator *Activator, offline *OfflineManager, store Store, publicKeyConfigured bool) *BootstrapHandler {
	return &BootstrapHandler{
		Validator:           validator,
		Activator:           activator,
		Offline:             offline,
		Store:               store,
		publicKeyConfigured: publicKeyConfigured,
		centerURL:           resolveCenterURL(),
	}
}

// RegisterRoutes mounts under /api/system/bootstrap.
func (h *BootstrapHandler) RegisterRoutes(g *echo.Group) {
	g.GET("/status", h.handleStatus)
	g.GET("/fingerprint", h.handleFingerprint)
	g.POST("/activate", h.handleActivate)
	g.POST("/activate-quick", h.handleActivateQuick)
	g.POST("/register-center", h.handleRegisterCenter)
	g.POST("/import-offline", h.handleImportOffline)
}

type bootstrapStatus struct {
	Activated      bool   `json:"activated"`
	Registered     bool   `json:"registered"`
	CenterOnline   bool   `json:"center_online"`
	CenterURL      string `json:"center_url,omitempty"`
	InstanceID     string `json:"instance_id,omitempty"`
	HardwareHash   string `json:"hardware_hash,omitempty"`
	LicenseKey     string `json:"license_key,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	PrimaryIP      string `json:"primary_ip,omitempty"`
	ServiceVersion string `json:"service_version,omitempty"`
	ActivatedAt    string `json:"activated_at,omitempty"`
	AdminBootstrap bool   `json:"admin_bootstrap_required"`
	Message        string `json:"message,omitempty"`
}

func (h *BootstrapHandler) handleStatus(c echo.Context) error {
	requested := strings.TrimSpace(c.QueryParam("instance_id"))
	instanceID := resolveLocalInstanceID()
	if requested != "" && requested != instanceID {
		// Caller-supplied IDs are ignored: one physical machine always has
		// exactly one local instance ID. Honoring a foreign ID would let a
		// second user squat this seat.
		slog.Warn("bootstrap status received foreign instance_id; using local",
			"local", instanceID, "remote", requested)
	}
	fp, _ := GenerateFingerprint()
	hash := ""
	if fp != nil {
		hash = fp.Hash()
	}
	st := bootstrapStatus{
		CenterURL:      h.centerURL,
		InstanceID:     instanceID,
		HardwareHash:   hash,
		PrimaryIP:      primaryIPv4(),
		ServiceVersion: gatewayVersionEnv(),
		AdminBootstrap: true,
	}
	if os.Getenv("LLM_GATEWAY_LICENSE_ACTIVATED") == "1" {
		st.Activated = true
	}
	licenseKey := strings.TrimSpace(c.QueryParam("license_key"))
	if !st.Activated && h.Store != nil && hash != "" {
		if licenseKey == "" {
			if lic, err := h.Store.GetLicenseByHardwareHash(c.Request().Context(), hash); err == nil && lic != nil {
				licenseKey = lic.LicenseKey
			}
		}
		if licenseKey != "" {
			if dev, err := h.Store.GetDeviceByHardwareHash(c.Request().Context(), licenseKey, hash); err == nil && dev != nil {
				if dev.Status == "active" || dev.Status == "" {
					st.Activated = true
					st.LicenseKey = maskBootstrapLicenseKey(licenseKey)
					st.DeviceName = dev.DeviceName
					if !dev.ActivatedAt.IsZero() {
						st.ActivatedAt = dev.ActivatedAt.UTC().Format(timeRFC3339)
					}
					if instanceID == "" {
						st.InstanceID = dev.InstanceID
					}
				}
			}
		}
	}
	st.CenterOnline = h.probeCenter()
	switch {
	case st.Activated && st.CenterOnline:
		st.Message = "已激活；中心可达，后台将自动注册/心跳"
	case st.Activated:
		st.Message = "已激活（离线可用）；中心不可达时不阻塞本地运转"
	default:
		st.Message = "尚未激活"
	}
	return c.JSON(http.StatusOK, st)
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func (h *BootstrapHandler) handleFingerprint(c echo.Context) error {
	instanceID := resolveLocalInstanceID()
	fp, err := GenerateFingerprint()
	if err != nil || fp == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"hardware_hash":   "",
			"instance_id":     instanceID,
			"os":              "",
			"arch":            "",
			"network_summary": primaryIPv4(),
			"error":           "fingerprint_unavailable",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"hardware_hash":   fp.Hash(),
		"instance_id":     instanceID,
		"os":              fp.OS,
		"arch":            fp.Arch,
		"network_summary": primaryIPv4(),
	})
}

func (h *BootstrapHandler) handleActivate(c echo.Context) error {
	var input struct {
		InstanceID   string `json:"instance_id"`
		LicenseKey   string `json:"license_key"`
		HardwareHash string `json:"hardware_hash"`
		DeviceName   string `json:"device_name"`
		Online       *bool  `json:"online"`
	}
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
	}
	// One physical machine == one instance ID. Always derive from server state,
	// ignore any client-supplied value to prevent license squating.
	instanceID := resolveLocalInstanceID()
	if strings.TrimSpace(input.InstanceID) != "" && input.InstanceID != instanceID {
		slog.Warn("bootstrap activate received foreign instance_id; using local",
			"local", instanceID, "remote", input.InstanceID)
	}
	if input.LicenseKey == "" || input.HardwareHash == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "license_key and hardware_hash required"})
	}
	wantOnline := h.centerURL != "" && h.probeCenter()
	if input.Online != nil {
		wantOnline = *input.Online
	}
	result := map[string]any{
		"activated": false, "center_online": wantOnline, "registered": false, "mode": "local",
	}
	if wantOnline {
		ok, body, err := bootstrapPostJSON(h.centerURL+"/maintain-api/public/license/activate", map[string]string{
			"instance_id": instanceID, "license_key": input.LicenseKey,
			"hardware_hash": input.HardwareHash, "device_name": input.DeviceName,
		})
		if err == nil && ok {
			result["activated"] = true
			result["mode"] = "online"
			result["maintain"] = body
			h.startCenterAgent(instanceID, input.LicenseKey, input.HardwareHash)
			result["registered"] = true
			return c.JSON(http.StatusOK, result)
		}
		result["online_error"] = bootstrapErrString(err)
		if body != nil {
			result["online_body"] = body
		}
	}
	if h.Activator == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "activator_unavailable"})
	}
	resp, err := h.Activator.Activate(c.Request().Context(), &ActivationRequest{
		LicenseKey: input.LicenseKey, HardwareHash: input.HardwareHash,
		InstanceID: instanceID, DeviceName: input.DeviceName,
	})
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error": err.Error(), "activated": false, "center_online": wantOnline, "mode": "local",
		})
	}
	result["activated"] = true
	result["mode"] = "local"
	result["activation"] = resp
	if h.probeCenter() {
		h.startCenterAgent(instanceID, input.LicenseKey, input.HardwareHash)
		result["registered"] = true
		result["center_online"] = true
	}
	return c.JSON(http.StatusOK, result)
}

// handleActivateQuick: agree-and-activate — issue license at center, then import locally.
func (h *BootstrapHandler) handleActivateQuick(c echo.Context) error {
	var input struct {
		InstanceID   string `json:"instance_id"`
		HardwareHash string `json:"hardware_hash"`
		DeviceName   string `json:"device_name"`
	}
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
	}
	// One physical machine == one instance ID. Always derive from server state.
	instanceID := resolveLocalInstanceID()
	if strings.TrimSpace(input.InstanceID) != "" && input.InstanceID != instanceID {
		slog.Warn("bootstrap activate-quick received foreign instance_id; using local",
			"local", instanceID, "remote", input.InstanceID)
	}
	if input.HardwareHash == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "hardware_hash required"})
	}
	if h.centerURL == "" || !h.probeCenter() {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"activated": false, "error": "center_unreachable",
			"message": "中心不可达，请改用离线激活或检查 LLM_GATEWAY_CENTER_URL",
		})
	}
	ok, body, err := bootstrapPostJSON(h.centerURL+"/maintain-api/public/license/issue", map[string]string{
		"instance_id": instanceID, "hardware_hash": input.HardwareHash, "device_name": input.DeviceName,
	})
	if err != nil || !ok {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"activated": false, "error": bootstrapErrString(err), "body": body,
			"message": "中心领码失败",
		})
	}
	signed := mapBodyString(body, "signed_license")
	licenseKey := mapBodyString(body, "license_key")
	if signed == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"activated": false, "error": "missing_signed_license", "body": body,
		})
	}

	// 从 signed_license 解析 License 并写入本地 licenses 表
	// 这样后续 Activator.Activate() 能查到记录
	var parsedLicense *License
	if h.Offline != nil {
		lic, err := h.Offline.VerifyOfflineLicense(c.Request().Context(), signed)
		if err != nil && h.publicKeyConfigured {
			// 严格模式：公钥已配置 → 验签失败必须报错
			return c.JSON(http.StatusBadRequest, map[string]any{
				"activated": false, "error": err.Error(), "message": "本地校验 signed_license 失败",
			})
		}
		if err != nil && !h.publicKeyConfigured {
			// 信任路径：公钥未配置 → 跳过验签，直接解析 Data
			slog.Warn("activating without RSA verification (LicensePublicKey 未配置)；信任通道来源",
				"instance_id", instanceID, "verify_err", err.Error())
			// 尝试解析 signed_license 的 Data 部分（不验签）
			signedStruct, unmarshalErr := UnmarshalFromBase64(signed)
			if unmarshalErr == nil && len(signedStruct.Data) > 0 {
				var trustedLic License
				if jsonErr := json.Unmarshal(signedStruct.Data, &trustedLic); jsonErr == nil {
					// 确保 Features 不是 nil（PostgreSQL jsonb 不接受 Go nil）
					if trustedLic.Features == nil {
						trustedLic.Features = []string{}
					}
					parsedLicense = &trustedLic
					slog.Info("activateQuick: parsed license from Data without RSA verification (信任路径)",
						"license_key", trustedLic.LicenseKey)
				}
			}
		} else {
			parsedLicense = lic
		}
	}

	// 确保 licenses 表有记录（中心签发的 license 本地可能没有）
	if parsedLicense != nil && licenseKey != "" {
		_, err := h.Store.GetLicense(c.Request().Context(), licenseKey)
		if err != nil {
			// licenses 表没有记录，插入
			if createErr := h.Store.CreateLicense(c.Request().Context(), parsedLicense); createErr != nil {
				slog.Warn("activateQuick: failed to create license in local DB (继续激活)",
					"license_key", licenseKey, "error", createErr)
			} else {
				slog.Info("activateQuick: created license in local DB", "license_key", licenseKey)
			}
		}
	}

	if h.Activator != nil && licenseKey != "" {
		resp, err := h.Activator.Activate(c.Request().Context(), &ActivationRequest{
			LicenseKey: licenseKey, HardwareHash: input.HardwareHash,
			InstanceID: instanceID, DeviceName: input.DeviceName,
		})
		if err != nil {
			slog.Error("activateQuick: Activator.Activate failed", "error", err, "license_key", licenseKey)
			return c.JSON(http.StatusInternalServerError, map[string]any{
				"activated": false, "error": err.Error(), "message": "本地激活失败：写入数据库时出错",
			})
		}
		if resp != nil && !resp.Success {
			slog.Warn("activateQuick: Activator.Activate returned success=false", "message", resp.Message)
			return c.JSON(http.StatusBadRequest, map[string]any{
				"activated": false, "error": resp.Message, "message": "本地激活失败",
			})
		}
	}
	h.startCenterAgent(instanceID, licenseKey, input.HardwareHash)
	return c.JSON(http.StatusOK, map[string]any{
		"activated": true, "mode": "quick", "center_online": true, "registered": true,
		"instance_id": instanceID,
		"license_key": maskBootstrapLicenseKey(licenseKey), "message": "已同意并完成激活",
	})
}
