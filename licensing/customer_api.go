package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// CustomerAPI exposes customer-facing license endpoints under /api/system/license/*.
//
// These endpoints are intentionally unauthenticated because the customer must be
// able to query license status and complete activation BEFORE any admin login.
// In restricted mode (license verification failure), only these endpoints remain
// reachable — see licensing/restricted_mode.go.
type CustomerAPI struct {
	store          Store
	activator      *Activator
	offlineManager *OfflineManager
	trialURL       string
}

// SetTrialAuthorityURL enables the customer gateway to proxy trial requests
// to the License Authority without exposing a cross-origin browser endpoint.
func (api *CustomerAPI) SetTrialAuthorityURL(rawURL string) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !isDevelopmentEnvironment()) {
		api.trialURL = ""
		return
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	api.trialURL = strings.TrimRight(parsed.String(), "/")
}

func isDevelopmentEnvironment() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	return value == "dev" || value == "development" || value == "test"
}

// NewCustomerAPI wires the customer-facing handlers around existing components.
func NewCustomerAPI(store Store, activator *Activator, offlineManager *OfflineManager) *CustomerAPI {
	return &CustomerAPI{
		store:          store,
		activator:      activator,
		offlineManager: offlineManager,
	}
}

// RegisterRoutes mounts customer-facing handlers under the given Echo group.
// Caller is expected to pass the /api/system/license group.
func (api *CustomerAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/status", api.handleStatus)
	g.GET("/info", api.handleInfo)
	g.POST("/activate", api.handleActivate)
	g.POST("/trial", api.handleTrial)
	g.POST("/offline-activate", api.handleOfflineActivate)
	g.POST("/offline-request", api.handleOfflineRequest)
	g.POST("/heartbeat", api.handleHeartbeat)
}

type trialRequest struct {
	Email string `json:"email"`
}

func (api *CustomerAPI) handleTrial(c echo.Context) error {
	if api.trialURL == "" {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "trial activation is not configured"})
	}
	var req trialRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email is required"})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode trial request"})
	}
	request, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, api.trialURL+"/api/v1/license/trial", bytes.NewReader(body))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create trial request"})
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "license authority unavailable"})
	}
	defer response.Body.Close()
	limitedBody := io.LimitReader(response.Body, 64<<10)
	var result map[string]interface{}
	if err := json.NewDecoder(limitedBody).Decode(&result); err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "invalid license authority response"})
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return c.JSON(response.StatusCode, result)
	}
	return c.JSON(response.StatusCode, result)
}

// CustomerStatusResponse is the payload for /api/system/license/status.
// State values:
//
//	none      — no license has ever been activated on this hardware
//	active    — license is valid and active
//	grace     — license expired within the last 7 days (offline grace period)
//	expired   — license expired beyond the grace window
//	revoked   — license was revoked by the admin
//	restricted— service is in restricted mode (see restricted_mode.go)
type CustomerStatusResponse struct {
	State            string     `json:"state"`
	LicenseKey       string     `json:"license_key,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	DaysRemaining    int        `json:"days_remaining,omitempty"`
	SubscriptionTier string     `json:"subscription_tier,omitempty"`
	Mode             string     `json:"mode"` // "licensed" | "community" | "restricted"
	GraceDaysLeft    int        `json:"grace_days_left,omitempty"`
}

const (
	graceWindow = 7 * 24 * time.Hour
)

func (api *CustomerAPI) handleStatus(c echo.Context) error {
	ctx := c.Request().Context()

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	lic, err := api.store.GetLicenseByHardwareHash(ctx, hwHash)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	mode := "licensed"
	if IsCommunityMode() {
		mode = "community"
	}

	resp := CustomerStatusResponse{Mode: mode}

	if lic == nil {
		resp.State = "none"
		return c.JSON(http.StatusOK, resp)
	}

	resp.LicenseKey = maskLicenseKey(lic.LicenseKey)
	resp.ExpiresAt = &lic.ExpiresAt
	resp.SubscriptionTier = lic.SubscriptionTier
	resp.DaysRemaining = int(time.Until(lic.ExpiresAt).Hours() / 24)

	switch {
	case lic.RevokedAt != nil:
		resp.State = "revoked"
	case time.Now().After(lic.ExpiresAt.Add(graceWindow)):
		resp.State = "expired"
	case time.Now().After(lic.ExpiresAt):
		resp.State = "grace"
		resp.GraceDaysLeft = int(time.Until(lic.ExpiresAt.Add(graceWindow)).Hours() / 24)
	default:
		resp.State = "active"
	}

	return c.JSON(http.StatusOK, resp)
}

type CustomerInfoResponse struct {
	CustomerStatusResponse
	Features      []string   `json:"features,omitempty"`
	MaxDevices    int        `json:"max_devices,omitempty"`
	ActiveDevices int        `json:"active_devices,omitempty"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
}

func (api *CustomerAPI) handleInfo(c echo.Context) error {
	ctx := c.Request().Context()

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	lic, err := api.store.GetLicenseByHardwareHash(ctx, hwHash)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if lic == nil {
		return c.JSON(http.StatusOK, CustomerInfoResponse{
			CustomerStatusResponse: CustomerStatusResponse{State: "none"},
		})
	}

	status, _ := buildCustomerStatus(ctx, api.store, lic, hwHash)

	activeDevices, err := api.store.GetActiveDevices(ctx, lic.LicenseKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	var lastHB *time.Time
	var activatedAt *time.Time
	for i := range activeDevices {
		if activeDevices[i].HardwareHash != hwHash {
			continue
		}
		if activeDevices[i].LastHeartbeat != nil {
			lastHB = activeDevices[i].LastHeartbeat
		}
		t := activeDevices[i].ActivatedAt
		activatedAt = &t
	}

	resp := CustomerInfoResponse{
		CustomerStatusResponse: status,
		Features:               lic.Features,
		MaxDevices:             lic.MaxDevices,
		ActiveDevices:          len(activeDevices),
		ActivatedAt:            activatedAt,
		LastHeartbeat:          lastHB,
	}
	return c.JSON(http.StatusOK, resp)
}

func buildCustomerStatus(ctx context.Context, store Store, lic *License, hwHash string) (CustomerStatusResponse, error) {
	mode := "licensed"
	if IsCommunityMode() {
		mode = "community"
	}
	resp := CustomerStatusResponse{
		Mode:             mode,
		LicenseKey:       maskLicenseKey(lic.LicenseKey),
		ExpiresAt:        &lic.ExpiresAt,
		SubscriptionTier: lic.SubscriptionTier,
		DaysRemaining:    int(time.Until(lic.ExpiresAt).Hours() / 24),
	}
	switch {
	case lic.RevokedAt != nil:
		resp.State = "revoked"
	case time.Now().After(lic.ExpiresAt.Add(graceWindow)):
		resp.State = "expired"
	case time.Now().After(lic.ExpiresAt):
		resp.State = "grace"
		resp.GraceDaysLeft = int(time.Until(lic.ExpiresAt.Add(graceWindow)).Hours() / 24)
	default:
		resp.State = "active"
	}
	return resp, nil
}

type ActivateRequest struct {
	LicenseKey string `json:"license_key"`
	DeviceName string `json:"device_name,omitempty"`
}

func (api *CustomerAPI) handleActivate(c echo.Context) error {
	ctx := c.Request().Context()

	var req ActivateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if strings.TrimSpace(req.LicenseKey) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "license_key is required"})
	}

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	activationReq := &ActivationRequest{
		LicenseKey:   req.LicenseKey,
		HardwareHash: hwHash,
		InstanceID:   uuid.New().String(),
		DeviceName:   defaultDeviceName(req.DeviceName, fp),
	}

	resp, err := api.activator.Activate(ctx, activationReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if !resp.Success {
		status := http.StatusBadRequest
		if resp.NeedDeactivate {
			status = http.StatusConflict
		}
		return c.JSON(status, resp)
	}

	slog.Info("customer license activated", "license_key", req.LicenseKey, "hardware_hash", hwHash)
	return c.JSON(http.StatusOK, resp)
}

type OfflineActivateRequest struct {
	// SignedLicense is the base64-encoded SignedLicense envelope produced by
	// the License Authority offline approval flow. The customer pastes this
	// along with the request id and one-time activation code.
	SignedLicense  string `json:"signed_license"`
	RequestID      string `json:"request_id"`
	ActivationCode string `json:"activation_code"`
}

func (api *CustomerAPI) handleOfflineActivate(c echo.Context) error {
	ctx := c.Request().Context()

	var req OfflineActivateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if strings.TrimSpace(req.SignedLicense) == "" || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.ActivationCode) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "signed_license, request_id, and activation_code are required"})
	}

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	offlineReq, err := api.store.GetOfflineRequest(ctx, strings.TrimSpace(req.RequestID))
	if err != nil || offlineReq == nil || offlineReq.Status != "approved" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offline request is not approved"})
	}
	if offlineReq.HardwareHash != hwHash || NormalizeActivationCode(req.ActivationCode) != NormalizeActivationCode(offlineReq.ActivationCode) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offline approval does not match this device"})
	}

	lic, err := api.offlineManager.VerifyOfflineLicense(ctx, req.SignedLicense)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if lic.LicenseKey != offlineReq.LicenseKey || offlineReq.ApprovedLicense == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offline license does not match the approved request"})
	}
	approved, err := MarshalToBase64(offlineReq.ApprovedLicense)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "invalid approved license"})
	}
	provided, err := UnmarshalFromBase64(req.SignedLicense)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid signed license"})
	}
	providedEncoded, err := MarshalToBase64(provided)
	if err != nil || providedEncoded != approved {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "signed license does not match the approved request"})
	}

	device := &Device{
		LicenseID:    lic.ID,
		InstanceID:   offlineReq.InstanceID,
		HardwareHash: hwHash,
		DeviceName:   offlineReq.DeviceName,
		Status:       "active",
	}
	switch err := api.store.ActivateDeviceIfUnderLimit(ctx, device, lic.MaxDevices); {
	case errors.Is(err, ErrDeviceAlreadyActivated):
		return c.JSON(http.StatusConflict, map[string]string{"error": "device is already activated"})
	case errors.Is(err, ErrDeviceLimitExceeded):
		return c.JSON(http.StatusConflict, map[string]string{"error": "device limit exceeded"})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	slog.Info("customer offline license activated", "license_key", lic.LicenseKey, "hardware_hash", hwHash)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":           true,
		"expires_at":        lic.ExpiresAt,
		"customer_name":     lic.CustomerName,
		"subscription_tier": lic.SubscriptionTier,
		"message":           "offline activation successful",
	})
}

type OfflineRequestPayload struct {
	LicenseKey string `json:"license_key"`
	DeviceName string `json:"device_name,omitempty"`
}

func (api *CustomerAPI) handleOfflineRequest(c echo.Context) error {
	ctx := c.Request().Context()

	var req OfflineRequestPayload
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if strings.TrimSpace(req.LicenseKey) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "license_key is required"})
	}

	if _, err := api.store.GetLicense(ctx, req.LicenseKey); err != nil {
		if err.Error() == "license not found" {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "license_key not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	offlineReq := &OfflineRequest{
		LicenseKey:   req.LicenseKey,
		HardwareHash: hwHash,
		InstanceID:   uuid.New().String(),
		DeviceName:   defaultDeviceName(req.DeviceName, fp),
		Status:       "pending",
	}

	signedReq, err := api.offlineManager.CreateOfflineRequest(ctx, offlineReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"request_id":     offlineReq.RequestID,
		"signed_request": signedReq,
		"message":        "submit this request to your license authority to receive an activation code",
	})
}

func (api *CustomerAPI) handleHeartbeat(c echo.Context) error {
	ctx := c.Request().Context()

	fp, err := GenerateFingerprint()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fingerprint generation failed"})
	}
	hwHash := fp.Hash()

	lic, err := api.store.GetLicenseByHardwareHash(ctx, hwHash)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if lic == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "no license bound to this hardware"})
	}

	if err := api.activator.Heartbeat(ctx, lic.LicenseKey, hwHash); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	now := time.Now()
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":        true,
		"last_heartbeat": now,
	})
}

func defaultDeviceName(provided string, fp *Fingerprint) string {
	if provided = strings.TrimSpace(provided); provided != "" {
		return provided
	}
	if fp != nil && fp.HostID != "" {
		return fp.HostID
	}
	return "gateway"
}

// maskLicenseKey keeps the public status response useful for identifying the
// bound license without returning the activation credential itself.
func maskLicenseKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
