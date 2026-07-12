package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
)

var ErrLicenseNotFound = errors.New("license not found")

// ── Mock license store ──────────────────────────────────────────────────

type mockLicenseStore struct {
	licenses map[string]*licensing.License
	devices  map[string][]licensing.Device
}

func (m *mockLicenseStore) GetLicense(ctx context.Context, licenseKey string) (*licensing.License, error) {
	if lic, ok := m.licenses[licenseKey]; ok {
		return lic, nil
	}
	return nil, ErrLicenseNotFound
}

func (m *mockLicenseStore) CountActiveDevices(ctx context.Context, licenseKey string) (int, error) {
	if devs, ok := m.devices[licenseKey]; ok {
		return len(devs), nil
	}
	return 0, nil
}

func (m *mockLicenseStore) GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*licensing.Device, error) {
	if devs, ok := m.devices[licenseKey]; ok {
		for _, dev := range devs {
			if dev.HardwareHash == hardwareHash {
				return &dev, nil
			}
		}
	}
	return nil, nil
}

func (m *mockLicenseStore) ActivateDevice(ctx context.Context, dev *licensing.Device) error {
	// Find the license by ID and store device under its key
	for licKey, lic := range m.licenses {
		if lic.ID == dev.LicenseID {
			if m.devices[licKey] == nil {
				m.devices[licKey] = []licensing.Device{}
			}
			m.devices[licKey] = append(m.devices[licKey], *dev)
			return nil
		}
	}
	return errors.New("license not found for ActivateDevice")
}

// Stub implementations for unused methods
func (m *mockLicenseStore) GetLicenseByID(ctx context.Context, id int64) (*licensing.License, error) {
	return nil, nil
}
func (m *mockLicenseStore) GetLicenseByHardwareHash(ctx context.Context, hardwareHash string) (*licensing.License, error) {
	// Look through devices to find one with matching hardware hash
	for licKey, devs := range m.devices {
		for _, dev := range devs {
			if dev.HardwareHash == hardwareHash && dev.Status == "active" {
				return m.licenses[licKey], nil
			}
		}
	}
	return nil, nil
}
func (m *mockLicenseStore) CreateLicense(ctx context.Context, lic *licensing.License) error {
	return nil
}
func (m *mockLicenseStore) UpdateLicense(ctx context.Context, lic *licensing.License) error {
	return nil
}
func (m *mockLicenseStore) RevokeLicense(ctx context.Context, licenseKey string) error {
	return nil
}
func (m *mockLicenseStore) GetActiveDevices(ctx context.Context, licenseKey string) ([]licensing.Device, error) {
	return nil, nil
}
func (m *mockLicenseStore) DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error {
	return nil
}
func (m *mockLicenseStore) UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error {
	return nil
}
func (m *mockLicenseStore) CreateOfflineRequest(ctx context.Context, req *licensing.OfflineRequest) error {
	return nil
}
func (m *mockLicenseStore) GetOfflineRequest(ctx context.Context, requestID string) (*licensing.OfflineRequest, error) {
	return nil, nil
}
func (m *mockLicenseStore) ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *licensing.SignedLicense) error {
	return nil
}
func (m *mockLicenseStore) ListOfflineRequests(ctx context.Context) ([]licensing.OfflineRequest, error) {
	return nil, nil
}
func (m *mockLicenseStore) RejectOfflineRequest(ctx context.Context, requestID, reason string) error {
	return nil
}
func (m *mockLicenseStore) ListAllLicenses(ctx context.Context, offset, limit int, query, statusFilter string) ([]licensing.License, int, error) {
	return nil, 0, nil
}
func (m *mockLicenseStore) ListAllDevices(ctx context.Context, licenseKey string) ([]licensing.Device, error) {
	return nil, nil
}
func (m *mockLicenseStore) GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*licensing.LicenseModule, error) {
	return nil, nil
}
func (m *mockLicenseStore) ListProductModules(ctx context.Context) ([]licensing.ProductModule, error) {
	return nil, nil
}
func (m *mockLicenseStore) ListProductModuleFeatures(ctx context.Context) ([]licensing.ProductModuleFeature, error) {
	return nil, nil
}
func (m *mockLicenseStore) ListSubscriptionTiers(ctx context.Context) ([]licensing.SubscriptionTier, error) {
	return nil, nil
}
func (m *mockLicenseStore) ListTierModuleMaps(ctx context.Context) ([]licensing.TierModuleMap, error) {
	return nil, nil
}
func (m *mockLicenseStore) ListLicenseModulesByID(ctx context.Context, licenseID int64) ([]licensing.LicenseModule, error) {
	return nil, nil
}
func (m *mockLicenseStore) UpsertLicenseModule(ctx context.Context, lm *licensing.LicenseModule) error {
	return nil
}
func (m *mockLicenseStore) DeleteLicenseModule(ctx context.Context, licenseID int64, moduleKey string) error {
	return nil
}

func TestSignInstanceToken(t *testing.T) {
	_, serverPrivKey, _ := ed25519.GenerateKey(nil)

	token, err := SignInstanceToken("instance-001", "license-hash-123", serverPrivKey)
	if err != nil {
		t.Fatalf("SignInstanceToken failed: %v", err)
	}

	if token == "" {
		t.Error("expected non-empty token")
	}
}

func TestGenerateRefreshTokenFunc(t *testing.T) {
	token, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken failed: %v", err)
	}

	if token == "" {
		t.Error("expected non-empty refresh token")
	}

	// Verify it's hex
	if len(token) != 64 {
		t.Errorf("expected hex token of length 64, got %d", len(token))
	}
}

func TestBase64Encoding(t *testing.T) {
	_, serverPrivKey, _ := ed25519.GenerateKey(nil)
	serverPubKey := serverPrivKey.Public().(ed25519.PublicKey)

	encoded := base64.StdEncoding.EncodeToString(serverPubKey)
	if encoded == "" {
		t.Error("expected non-empty base64 encoded public key")
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Errorf("failed to decode base64: %v", err)
	}

	if len(decoded) != ed25519.PublicKeySize {
		t.Errorf("expected public key size %d, got %d", ed25519.PublicKeySize, len(decoded))
	}
}

// ── Mock center store ──────────────────────────────────────────────────

type mockCenterStore struct {
	registeredInstances []*center.InstanceInfo
	registerErr         error
}

func (m *mockCenterStore) RegisterInstance(ctx context.Context, instance *center.InstanceInfo) error {
	if m.registerErr != nil {
		return m.registerErr
	}
	m.registeredInstances = append(m.registeredInstances, instance)
	return nil
}
func (m *mockCenterStore) GetInstance(ctx context.Context, instanceID string) (*center.InstanceInfo, error) {
	return nil, nil
}
func (m *mockCenterStore) ListInstances(ctx context.Context, status string, offset, limit int) ([]center.InstanceInfo, int, error) {
	return nil, 0, nil
}
func (m *mockCenterStore) UpdateInstanceStatus(ctx context.Context, instanceID, status string) error {
	return nil
}
func (m *mockCenterStore) DeleteInstance(ctx context.Context, instanceID string) error {
	return nil
}
func (m *mockCenterStore) RecordHeartbeat(ctx context.Context, instanceID string, payload *center.HeartbeatPayload) error {
	return nil
}
func (m *mockCenterStore) GetLastHeartbeat(ctx context.Context, instanceID string) (time.Time, error) {
	return time.Time{}, nil
}
func (m *mockCenterStore) GetHeartbeatHistory(ctx context.Context, instanceID string, since time.Time, limit int) ([]center.HeartbeatRecord, error) {
	return nil, nil
}
func (m *mockCenterStore) CreateCommand(ctx context.Context, cmd *center.Command) error {
	return nil
}
func (m *mockCenterStore) GetCommand(ctx context.Context, commandID string) (*center.Command, error) {
	return nil, nil
}
func (m *mockCenterStore) ListPendingCommands(ctx context.Context, instanceID string) ([]center.Command, error) {
	return nil, nil
}
func (m *mockCenterStore) UpdateCommandStatus(ctx context.Context, commandID, status string, result *center.CommandResult) error {
	return nil
}
func (m *mockCenterStore) GetCommandHistory(ctx context.Context, instanceID string, limit int) ([]center.Command, error) {
	return nil, nil
}
func (m *mockCenterStore) RecordStatusReport(ctx context.Context, instanceID string, payload *center.StatusReportPayload) error {
	return nil
}
func (m *mockCenterStore) GetLatestStatus(ctx context.Context, instanceID string) (*center.StatusReportPayload, error) {
	return nil, nil
}
func (m *mockCenterStore) GetInstanceByRefreshToken(ctx context.Context, refreshToken string) (*center.InstanceInfo, error) {
	return nil, nil
}
func (m *mockCenterStore) UpdateRefreshToken(ctx context.Context, instanceID, refreshToken string, expiresAt time.Time) error {
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────

func setupRegisterTestEnv(t *testing.T) (*RegisterHandler, *mockLicenseStore, *mockCenterStore, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_ = pub
	licStore := &mockLicenseStore{
		licenses: map[string]*licensing.License{},
		devices:  map[string][]licensing.Device{},
	}
	centerStore := &mockCenterStore{}
	h := NewRegisterHandler(licStore, centerStore, priv)
	return h, licStore, centerStore, priv
}

func makeRegisterReq(t *testing.T, body map[string]interface{}) *http.Request {
	t.Helper()
	jsonBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/register", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeRegisterResp(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

// ── HandleRegister 测试用例 ──────────────────────────────────────────────

func TestHandleRegister_MissingFields(t *testing.T) {
	h, _, _, _ := setupRegisterTestEnv(t)
	e := echo.New()
	req := makeRegisterReq(t, map[string]interface{}{
		"instance_id": "inst-1",
		// 故意缺 license_key_hash / hardware_hash / public_key
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeRegisterResp(t, rec)
	if !strings.Contains(strings.ToLower(fmt.Sprint(resp["error"])), "missing") {
		t.Errorf("expected 'missing required fields' error, got %v", resp)
	}
}

func TestHandleRegister_LicenseNotFound(t *testing.T) {
	h, licStore, centerStore, _ := setupRegisterTestEnv(t)
	// 不预先放入 license — GetLicenseByHardwareHash 返回 nil, GetLicense 也返回 nil → 404
	e := echo.New()
	req := makeRegisterReq(t, map[string]interface{}{
		"instance_id":      "inst-1",
		"hardware_hash":    "hw-unknown",
		"license_key_hash": "lic-nonexistent",
		"public_key":       "pk-b64",
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(centerStore.registeredInstances) != 0 {
		t.Errorf("center.RegisterInstance should not be called, got %d", len(centerStore.registeredInstances))
	}
	_ = licStore
}

func TestHandleRegister_NewDevice_DeviceLimitExceeded(t *testing.T) {
	h, licStore, _, _ := setupRegisterTestEnv(t)
	// 已有 license，但设备数已满
	licStore.licenses["LIC-EXISTING"] = &licensing.License{
		ID:               1,
		LicenseKey:       "LIC-EXISTING",
		MaxDevices:       1,
		SubscriptionTier: "basic",
	}
	licStore.devices["LIC-EXISTING"] = []licensing.Device{
		{HardwareHash: "hw-existing", Status: "active"},
	}

	e := echo.New()
	req := makeRegisterReq(t, map[string]interface{}{
		"instance_id":      "inst-new",
		"hardware_hash":    "hw-new-different",
		"license_key_hash": "LIC-EXISTING", // 当成完整 key 查找（兼容旧版安装器）
		"public_key":       "pk-b64",
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("want 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeRegisterResp(t, rec)
	if !strings.Contains(strings.ToLower(fmt.Sprint(resp["error"])), "device_limit") {
		t.Errorf("expected device_limit_exceeded, got %v", resp)
	}
}

func TestHandleRegister_ExistingDevice_HappyPath(t *testing.T) {
	h, licStore, centerStore, _ := setupRegisterTestEnv(t)
	// 预先放入 license + 已激活 device（hardware_hash 匹配）
	licStore.licenses["LIC-KNOWN"] = &licensing.License{
		ID:               42,
		LicenseKey:       "LIC-KNOWN",
		CustomerName:     "test-corp",
		MaxDevices:       5,
		SubscriptionTier: "pro",
	}
	licStore.devices["LIC-KNOWN"] = []licensing.Device{
		{ID: 7, LicenseID: 42, InstanceID: "inst-old", HardwareHash: "hw-known", Status: "active"},
	}

	e := echo.New()
	req := makeRegisterReq(t, map[string]interface{}{
		"instance_id":      "inst-reinstall",
		"hardware_hash":    "hw-known", // 匹配已有 device → 走 hardware_hash 路径
		"license_key_hash": "LIC-KNOWN",
		"public_key":       "pk-b64",
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeRegisterResp(t, rec)
	if resp["instance_token"] == nil || resp["instance_token"] == "" {
		t.Errorf("expected instance_token in response, got %v", resp)
	}
	if resp["refresh_token"] == nil || resp["refresh_token"] == "" {
		t.Errorf("expected refresh_token in response, got %v", resp)
	}
	if resp["server_public_key"] == nil {
		t.Errorf("expected server_public_key in response, got %v", resp)
	}
	if len(centerStore.registeredInstances) != 1 {
		t.Fatalf("expected 1 instance registered, got %d", len(centerStore.registeredInstances))
	}
	got := centerStore.registeredInstances[0]
	if got.HardwareHash != "hw-known" {
		t.Errorf("expected hardware_hash hw-known, got %s", got.HardwareHash)
	}
	if got.LicenseKeyHash != "LIC-KNOWN" {
		t.Errorf("expected license_key LIC-KNOWN (实际 key 而非 hash), got %s", got.LicenseKeyHash)
	}
	// existingDevice != nil → 不应再调用 ActivateDevice
	if len(licStore.devices["LIC-KNOWN"]) != 1 {
		t.Errorf("expected 1 device (no new activation), got %d", len(licStore.devices["LIC-KNOWN"]))
	}
}

func TestHandleRegister_NewDevice_HappyPath(t *testing.T) {
	h, licStore, centerStore, _ := setupRegisterTestEnv(t)
	// license 存在，但该 hardware_hash 没设备 → 走新设备路径
	licStore.licenses["LIC-FRESH"] = &licensing.License{
		ID:               99,
		LicenseKey:       "LIC-FRESH",
		MaxDevices:       3,
		SubscriptionTier: "pro",
	}

	e := echo.New()
	req := makeRegisterReq(t, map[string]interface{}{
		"instance_id":      "inst-fresh",
		"hardware_hash":    "hw-fresh-never-seen",
		"license_key_hash": "LIC-FRESH",
		"public_key":       "pk-b64",
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(centerStore.registeredInstances) != 1 {
		t.Fatalf("expected 1 instance registered, got %d", len(centerStore.registeredInstances))
	}
	if got := centerStore.registeredInstances[0].LicenseKeyHash; got != "LIC-FRESH" {
		t.Errorf("expected license_key=LIC-FRESH, got %s", got)
	}
	// 新设备应触发 ActivateDevice
	if len(licStore.devices["LIC-FRESH"]) != 1 {
		t.Errorf("expected 1 device after ActivateDevice, got %d", len(licStore.devices["LIC-FRESH"]))
	}
	if dev := licStore.devices["LIC-FRESH"][0]; dev.HardwareHash != "hw-fresh-never-seen" {
		t.Errorf("expected device hardware_hash hw-fresh-never-seen, got %s", dev.HardwareHash)
	}
}
