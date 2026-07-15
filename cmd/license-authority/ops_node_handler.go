package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/kaixuan/llm-gateway-go/security/ipblocklist"
	"github.com/labstack/echo/v4"
)

// OpsNodeHandler handles ops node registration + heartbeat via llm.kxpms.cn API.
type OpsNodeHandler struct {
	licenseStore  licensing.Store
	centerStore   *center.PgxStore
	blocklist     *ipblocklist.Service
	serverPrivKey ed25519.PrivateKey
	serverPubKey  ed25519.PublicKey
}

func NewOpsNodeHandler(licenseStore licensing.Store, centerStore *center.PgxStore, blocklist *ipblocklist.Service, serverPrivKey ed25519.PrivateKey) *OpsNodeHandler {
	return &OpsNodeHandler{
		licenseStore:  licenseStore,
		centerStore:   centerStore,
		blocklist:     blocklist,
		serverPrivKey: serverPrivKey,
		serverPubKey:  serverPrivKey.Public().(ed25519.PublicKey),
	}
}

type opsRegisterReq struct {
	InstanceID   string `json:"instance_id"`
	Region       string `json:"region"`
	Hostname     string `json:"hostname"`
	IPAddress    string `json:"ip_address"`
	Version      string `json:"version"`
	BuildSeq     int    `json:"build_seq"`
	LicenseKey   string `json:"license_key"`
	AdminUser    string `json:"admin_user"`
	AdminEmail   string `json:"admin_email"`
	HardwareHash string `json:"hardware_hash"`
}

// HandleRegister POST /api/v1/ops/nodes/register
func (h *OpsNodeHandler) HandleRegister(c echo.Context) error {
	var req opsRegisterReq
	if err := c.Bind(&req); err != nil {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "bad_request")
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	req.InstanceID = strings.TrimSpace(req.InstanceID)
	req.Region = strings.TrimSpace(req.Region)
	req.LicenseKey = strings.TrimSpace(req.LicenseKey)
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	if req.InstanceID == "" || req.Region == "" || req.LicenseKey == "" || req.AdminUser == "" {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "missing_fields")
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "instance_id, region, license_key, admin_user required"})
	}

	license, err := h.licenseStore.GetLicense(c.Request().Context(), req.LicenseKey)
	if err != nil || license == nil || license.RevokedAt != nil {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "license_invalid")
		return c.JSON(http.StatusForbidden, map[string]string{"error": "license not found or revoked"})
	}

	instanceToken, err := SignInstanceToken(req.InstanceID, req.LicenseKey, h.serverPrivKey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to sign token"})
	}
	refreshToken, err := GenerateRefreshToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate refresh token"})
	}

	hw := strings.TrimSpace(req.HardwareHash)
	if hw == "" {
		hw = "ops:" + req.Region + ":" + req.InstanceID
	}
	now := time.Now()
	instance := &center.InstanceInfo{
		InstanceID:     req.InstanceID,
		InstanceType:   "gateway",
		DeploymentID:   req.Region,
		Region:         req.Region,
		Hostname:       req.Hostname,
		IPAddress:      req.IPAddress,
		Version:        req.Version,
		BuildSeq:       req.BuildSeq,
		LicenseKeyHash: req.LicenseKey,
		HardwareHash:   hw,
		InstanceToken:  instanceToken,
		RefreshToken:   refreshToken,
		Status:         center.StatusOnline,
		StartedAt:      now,
	}
	if err := h.centerStore.RegisterInstance(c.Request().Context(), instance); err != nil {
		slog.Error("ops node register failed", "error", err, "instance_id", req.InstanceID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "register failed"})
	}

	if err := h.upsertOpsRegistration(c, req, license.ID, now); err != nil {
		slog.Error("ops node registration record failed", "error", err, "instance_id", req.InstanceID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "registration record failed"})
	}

	serverPubKeyB64 := base64.StdEncoding.EncodeToString(h.serverPubKey)
	return c.JSON(http.StatusOK, map[string]any{
		"instance_token":    instanceToken,
		"refresh_token":     refreshToken,
		"server_public_key": serverPubKeyB64,
		"expires_at":        now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		"region":            req.Region,
	})
}

func (h *OpsNodeHandler) upsertOpsRegistration(c echo.Context, req opsRegisterReq, licenseID int64, now time.Time) error {
	_, err := h.centerStore.Pool().Exec(c.Request().Context(), `
		INSERT INTO ops_node_registrations
		    (instance_id, region, license_key, license_id, admin_user, admin_email,
		     hostname, ip_address, version, build_seq, registered_at, last_heartbeat, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,'active')
		ON CONFLICT (instance_id) DO UPDATE SET
		    region = EXCLUDED.region,
		    license_key = EXCLUDED.license_key,
		    license_id = EXCLUDED.license_id,
		    admin_user = EXCLUDED.admin_user,
		    admin_email = EXCLUDED.admin_email,
		    hostname = EXCLUDED.hostname,
		    ip_address = EXCLUDED.ip_address,
		    version = EXCLUDED.version,
		    build_seq = EXCLUDED.build_seq,
		    last_heartbeat = EXCLUDED.last_heartbeat,
		    status = 'active'`,
		req.InstanceID, req.Region, req.LicenseKey, licenseID, req.AdminUser, req.AdminEmail,
		req.Hostname, req.IPAddress, req.Version, req.BuildSeq, now)
	return err
}

// HandleHeartbeat POST /api/v1/ops/nodes/heartbeat
func (h *OpsNodeHandler) HandleHeartbeat(c echo.Context) error {
	authHeader := c.Request().Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "missing_auth")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing Authorization")
	}
	claims, err := VerifyInstanceToken(parts[1], h.serverPubKey)
	if err != nil {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "invalid_token")
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
	}
	instanceID := strings.TrimPrefix(claims.Subject, "instance:")
	if instanceID == "" || instanceID == claims.Subject {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "bad_subject")
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid token subject")
	}

	var registered bool
	if err := h.centerStore.Pool().QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM ops_node_registrations WHERE instance_id = $1 AND status = 'active')`,
		instanceID).Scan(&registered); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "registration lookup failed")
	}
	if !registered {
		ipblocklist.RecordEchoAuthFailure(h.blocklist, c, ipblocklist.ScopeOps, "not_registered")
		return echo.NewHTTPError(http.StatusForbidden, "node not registered")
	}

	var payload center.HeartbeatPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid payload")
	}
	if err := h.centerStore.RecordHeartbeat(c.Request().Context(), instanceID, &payload); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "heartbeat failed")
	}
	_, _ = h.centerStore.Pool().Exec(c.Request().Context(),
		`UPDATE ops_node_registrations SET last_heartbeat = now() WHERE instance_id = $1`, instanceID)

	return c.JSON(http.StatusOK, map[string]any{"ack": true, "next_heartbeat_in_secs": 60})
}

func (h *OpsNodeHandler) RegisterRoutes(g *echo.Group, tokenGroup *echo.Group) {
	g.POST("/nodes/register", h.HandleRegister)
	tokenGroup.POST("/nodes/heartbeat", h.HandleHeartbeat)
}
