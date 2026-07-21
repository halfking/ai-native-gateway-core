package licensing

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func (h *BootstrapHandler) handleImportOffline(c echo.Context) error {
	var input struct {
		InstanceID     string `json:"instance_id"`
		HardwareHash   string `json:"hardware_hash"`
		SignedLicense  string `json:"signed_license"`
		ActivationCode string `json:"activation_code"`
	}
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
	}
	payload := strings.TrimSpace(input.SignedLicense)
	if payload == "" {
		payload = strings.TrimSpace(input.ActivationCode)
	}
	if payload == "" || h.Offline == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "signed_license_or_activation_code required"})
	}
	// One physical machine == one instance ID. Always derive from server state.
	instanceID := resolveLocalInstanceID()
	if strings.TrimSpace(input.InstanceID) != "" && input.InstanceID != instanceID {
		slog.Warn("bootstrap import-offline received foreign instance_id; using local",
			"local", instanceID, "remote", input.InstanceID)
	}
	lic, err := h.Offline.VerifyOfflineLicense(c.Request().Context(), payload)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": err.Error(), "activated": false})
	}
	if h.Activator != nil && input.HardwareHash != "" {
		_, _ = h.Activator.Activate(c.Request().Context(), &ActivationRequest{
			LicenseKey: lic.LicenseKey, HardwareHash: input.HardwareHash,
			InstanceID: instanceID, DeviceName: "offline-import",
		})
	}
	h.startCenterAgent(instanceID, lic.LicenseKey, input.HardwareHash)
	return c.JSON(http.StatusOK, map[string]any{
		"activated": true, "mode": "offline", "center_online": false, "registered": false,
		"instance_id": instanceID,
		"license_key": maskBootstrapLicenseKey(lic.LicenseKey),
		"message":     "离线激活成功；联网后将自动向中心补注册",
	})
}

func (h *BootstrapHandler) handleRegisterCenter(c echo.Context) error {
	var input struct {
		InstanceID   string `json:"instance_id"`
		LicenseKey   string `json:"license_key"`
		HardwareHash string `json:"hardware_hash"`
		Hostname     string `json:"hostname"`
		Version      string `json:"version"`
		Region       string `json:"region"`
	}
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
	}
	// One physical machine == one instance ID. Always derive from server state.
	instanceID := resolveLocalInstanceID()
	if strings.TrimSpace(input.InstanceID) != "" && input.InstanceID != instanceID {
		slog.Warn("bootstrap register-center received foreign instance_id; using local",
			"local", instanceID, "remote", input.InstanceID)
	}
	if !h.probeCenter() {
		h.startCenterAgent(instanceID, input.LicenseKey, input.HardwareHash)
		return c.JSON(http.StatusOK, map[string]any{
			"registered": false, "deferred": true,
			"message": "中心不可达，已记录待补注册（不阻塞本地使用）",
		})
	}
	ok, body, err := h.registerOnce(instanceID, input.LicenseKey, input.HardwareHash, input.Hostname, input.Version, input.Region)
	h.startCenterAgent(instanceID, input.LicenseKey, input.HardwareHash)
	if err != nil || !ok {
		return c.JSON(http.StatusOK, map[string]any{
			"registered": false, "deferred": true,
			"error": bootstrapErrString(err), "body": body, "message": "注册失败，将后台重试",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"registered": true, "instance_id": instanceID, "body": body})
}
