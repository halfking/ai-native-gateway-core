package licensing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// fakeStore is an in-memory implementation of Store for unit tests.
// Only the methods exercised by CustomerAPI tests are populated.
type fakeStore struct {
	mu       sync.Mutex
	licenses map[string]*License
	devices  map[string][]*Device // keyed by license_key
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		licenses: map[string]*License{},
		devices:  map[string][]*Device{},
	}
}

func (s *fakeStore) GetLicense(ctx context.Context, licenseKey string) (*License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lic, ok := s.licenses[licenseKey]
	if !ok {
		return nil, fmt.Errorf("license not found")
	}
	return lic, nil
}

func (s *fakeStore) GetLicenseByID(ctx context.Context, id int64) (*License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lic := range s.licenses {
		if lic.ID == id {
			return lic, nil
		}
	}
	return nil, fmt.Errorf("license not found")
}

func (s *fakeStore) GetLicenseByHardwareHash(ctx context.Context, hardwareHash string) (*License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lic := range s.licenses {
		for _, dev := range s.devices[lic.LicenseKey] {
			if dev.HardwareHash == hardwareHash && dev.Status == "active" {
				return lic, nil
			}
		}
	}
	return nil, nil
}

func (s *fakeStore) CreateLicense(ctx context.Context, lic *License) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.licenses[lic.LicenseKey]; exists {
		return fmt.Errorf("license already exists")
	}
	s.licenses[lic.LicenseKey] = lic
	return nil
}

func (s *fakeStore) UpdateLicense(ctx context.Context, lic *License) error {
	s.licenses[lic.LicenseKey] = lic
	return nil
}

func (s *fakeStore) RevokeLicense(ctx context.Context, licenseKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lic, ok := s.licenses[licenseKey]; ok {
		now := time.Now()
		lic.RevokedAt = &now
	}
	return nil
}

func (s *fakeStore) GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Device{}
	for _, dev := range s.devices[licenseKey] {
		if dev.Status == "active" {
			out = append(out, *dev)
		}
	}
	return out, nil
}

func (s *fakeStore) GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dev := range s.devices[licenseKey] {
		if dev.HardwareHash == hardwareHash {
			return dev, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) ActivateDevice(ctx context.Context, dev *Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[dev.HardwareHash] = append(s.devices[dev.HardwareHash], dev)
	s.devices[licenseKeyFromLicenseID(s.licenses, dev.LicenseID)] = append(
		s.devices[licenseKeyFromLicenseID(s.licenses, dev.LicenseID)], dev,
	)
	return nil
}

// ActivateDeviceIfUnderLimit mirrors the production store in-memory: enforce
// the per-license MaxDevices ceiling plus dedupe on active hardware_hash.
func (s *fakeStore) ActivateDeviceIfUnderLimit(ctx context.Context, dev *Device, maxDevices int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := licenseKeyFromLicenseID(s.licenses, dev.LicenseID)
	for _, existing := range s.devices[key] {
		if existing.HardwareHash == dev.HardwareHash && existing.Status == "active" {
			return ErrDeviceAlreadyActivated
		}
	}
	if maxDevices > 0 {
		active := 0
		for _, existing := range s.devices[key] {
			if existing.Status == "active" {
				active++
			}
		}
		if active >= maxDevices {
			return ErrDeviceLimitExceeded
		}
	}
	s.devices[dev.HardwareHash] = append(s.devices[dev.HardwareHash], dev)
	s.devices[key] = append(s.devices[key], dev)
	return nil
}

func licenseKeyFromLicenseID(licenses map[string]*License, id int64) string {
	for k, lic := range licenses {
		if lic.ID == id {
			return k
		}
	}
	return ""
}

func (s *fakeStore) DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error {
	return nil
}

func (s *fakeStore) UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, dev := range s.devices[licenseKey] {
		if dev.HardwareHash == hardwareHash && dev.Status == "active" {
			dev.LastHeartbeat = &now
		}
	}
	return nil
}

func (s *fakeStore) CountActiveDevices(ctx context.Context, licenseKey string) (int, error) {
	devs, err := s.GetActiveDevices(ctx, licenseKey)
	if err != nil {
		return 0, err
	}
	return len(devs), nil
}

func (s *fakeStore) ListAllLicenses(ctx context.Context, offset, limit int, query, statusFilter string) ([]License, int, error) {
	return nil, 0, nil
}
func (s *fakeStore) ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error) {
	return nil, nil
}
func (s *fakeStore) CreateOfflineRequest(ctx context.Context, req *OfflineRequest) error { return nil }
func (s *fakeStore) GetOfflineRequest(ctx context.Context, requestID string) (*OfflineRequest, error) {
	return nil, nil
}
func (s *fakeStore) ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense, activationCode string) error {
	return nil
}
func (s *fakeStore) GetOfflineActivationCode(ctx context.Context, requestID string) (string, error) {
	return "", nil
}
func (s *fakeStore) ListOfflineRequests(ctx context.Context) ([]OfflineRequest, error) {
	return nil, nil
}
func (s *fakeStore) RejectOfflineRequest(ctx context.Context, requestID, reason string) error {
	return nil
}
func (s *fakeStore) GetLicenseModules(ctx context.Context, licenseKey string) (map[string]*LicenseModule, error) {
	return nil, nil
}
func (s *fakeStore) ListProductModules(ctx context.Context) ([]ProductModule, error) { return nil, nil }
func (s *fakeStore) ListProductModuleFeatures(ctx context.Context) ([]ProductModuleFeature, error) {
	return nil, nil
}
func (s *fakeStore) ListSubscriptionTiers(ctx context.Context) ([]SubscriptionTier, error) {
	return nil, nil
}
func (s *fakeStore) ListTierModuleMaps(ctx context.Context) ([]TierModuleMap, error) { return nil, nil }
func (s *fakeStore) ListLicenseModulesByID(ctx context.Context, licenseID int64) ([]LicenseModule, error) {
	return nil, nil
}
func (s *fakeStore) UpsertLicenseModule(ctx context.Context, lm *LicenseModule) error { return nil }
func (s *fakeStore) DeleteLicenseModule(ctx context.Context, licenseID int64, moduleKey string) error {
	return nil
}

func newTestCrypto(t *testing.T) *CryptoConfig {
	t.Helper()
	priv, err := generateTestKeyPairFromRSA(t)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &CryptoConfig{
		PrivateKey: priv,
		PublicKey:  &priv.PublicKey,
	}
}

func generateTestKeyPairFromRSA(t *testing.T) (*rsa.PrivateKey, error) {
	t.Helper()
	return rsa.GenerateKey(rand.Reader, 2048)
}

func newTestCustomerAPI(t *testing.T) (*CustomerAPI, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	crypto := newTestCrypto(t)
	validator := NewValidator(crypto, store)
	deviceManager := NewDeviceManager(store, validator)
	activator := NewActivator(crypto, store, deviceManager)
	offlineMgr := NewOfflineManager(crypto, store)
	return NewCustomerAPI(store, activator, offlineMgr), store
}

func setupEcho(api *CustomerAPI) *echo.Echo {
	e := echo.New()
	g := e.Group("/api/system/license")
	api.RegisterRoutes(g)
	return e
}

func TestCustomerAPI_Status_NoLicense(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp CustomerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "none" {
		t.Errorf("expected state=none, got %q", resp.State)
	}
	if resp.Mode != "licensed" && resp.Mode != "community" {
		t.Errorf("expected mode to be licensed or community, got %q", resp.Mode)
	}
}

func TestCustomerAPI_Activate_MissingLicenseKey(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	body, _ := json.Marshal(map[string]string{"device_name": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/system/license/activate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomerAPI_OfflineActivate_MissingSignedLicense(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	body, _ := json.Marshal(map[string]string{"activation_code": "ABCDEFGH"})
	req := httptest.NewRequest(http.MethodPost, "/api/system/license/offline-activate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomerAPI_OfflineRequest_LicenseNotFound(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	body, _ := json.Marshal(map[string]string{"license_key": "LIC-NONEXISTENT"})
	req := httptest.NewRequest(http.MethodPost, "/api/system/license/offline-request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomerAPI_Heartbeat_NoLicense(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	req := httptest.NewRequest(http.MethodPost, "/api/system/license/heartbeat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomerAPI_Status_ActiveLicense(t *testing.T) {
	api, store := newTestCustomerAPI(t)
	e := setupEcho(api)

	// Seed an active license with a device bound to current hardware.
	hwHash := currentTestFingerprintHash(t)

	lic := &License{
		ID:               1,
		LicenseKey:       "LIC-TEST-001",
		CustomerName:     "Acme",
		CustomerEmail:    "ops@acme.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"base", "advanced_routing"},
		ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
		CreatedAt:        time.Now().Add(-24 * time.Hour),
	}
	store.licenses[lic.LicenseKey] = lic
	now := time.Now()
	store.devices[lic.LicenseKey] = []*Device{
		{
			ID: 1, LicenseID: lic.ID, InstanceID: "inst-1",
			HardwareHash: hwHash, DeviceName: "test-host",
			ActivatedAt: now, Status: "active", LastHeartbeat: &now,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp CustomerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "active" {
		t.Errorf("expected state=active, got %q", resp.State)
	}
	if resp.SubscriptionTier != "enterprise" {
		t.Errorf("expected tier=enterprise, got %q", resp.SubscriptionTier)
	}
	if resp.DaysRemaining < 29 || resp.DaysRemaining > 31 {
		t.Errorf("expected days_remaining ~30, got %d", resp.DaysRemaining)
	}
}

func TestCustomerAPI_Status_ExpiredLicense(t *testing.T) {
	api, store := newTestCustomerAPI(t)
	e := setupEcho(api)

	hwHash := currentTestFingerprintHash(t)
	lic := &License{
		ID: 1, LicenseKey: "LIC-OLD", CustomerName: "Old",
		MaxDevices: 1, SubscriptionTier: "starter",
		ExpiresAt: time.Now().Add(-30 * 24 * time.Hour),
		CreatedAt: time.Now().Add(-90 * 24 * time.Hour),
	}
	store.licenses[lic.LicenseKey] = lic
	store.devices[lic.LicenseKey] = []*Device{
		{ID: 1, LicenseID: lic.ID, HardwareHash: hwHash, Status: "active"},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp CustomerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "expired" {
		t.Errorf("expected state=expired, got %q", resp.State)
	}
}

func TestCustomerAPI_Status_GraceLicense(t *testing.T) {
	api, store := newTestCustomerAPI(t)
	e := setupEcho(api)

	hwHash := currentTestFingerprintHash(t)
	lic := &License{
		ID: 1, LicenseKey: "LIC-GRACE", CustomerName: "G",
		MaxDevices: 1, SubscriptionTier: "starter",
		ExpiresAt: time.Now().Add(-3 * 24 * time.Hour), // 3 days into grace
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}
	store.licenses[lic.LicenseKey] = lic
	store.devices[lic.LicenseKey] = []*Device{
		{ID: 1, LicenseID: lic.ID, HardwareHash: hwHash, Status: "active"},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp CustomerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "grace" {
		t.Errorf("expected state=grace, got %q", resp.State)
	}
	if resp.GraceDaysLeft < 1 || resp.GraceDaysLeft > 4 {
		t.Errorf("expected grace_days_left 1-4, got %d", resp.GraceDaysLeft)
	}
}

func TestCustomerAPI_Status_RevokedLicense(t *testing.T) {
	api, store := newTestCustomerAPI(t)
	e := setupEcho(api)

	hwHash := currentTestFingerprintHash(t)
	revoked := time.Now().Add(-2 * 24 * time.Hour)
	lic := &License{
		ID: 1, LicenseKey: "LIC-REV", CustomerName: "R",
		MaxDevices: 1, SubscriptionTier: "starter",
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		RevokedAt: &revoked,
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}
	store.licenses[lic.LicenseKey] = lic
	store.devices[lic.LicenseKey] = []*Device{
		{ID: 1, LicenseID: lic.ID, HardwareHash: hwHash, Status: "active"},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp CustomerStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "revoked" {
		t.Errorf("expected state=revoked, got %q", resp.State)
	}
}

func TestCustomerAPI_Info_NoLicense(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	req := httptest.NewRequest(http.MethodGet, "/api/system/license/info", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp CustomerInfoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "none" {
		t.Errorf("expected state=none, got %q", resp.State)
	}
}

func TestCustomerAPI_Heartbeat_ActiveLicense(t *testing.T) {
	api, store := newTestCustomerAPI(t)
	e := setupEcho(api)

	hwHash := currentTestFingerprintHash(t)
	lic := &License{
		ID: 1, LicenseKey: "LIC-HB", CustomerName: "H",
		MaxDevices: 1, SubscriptionTier: "starter",
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	store.licenses[lic.LicenseKey] = lic
	store.devices[lic.LicenseKey] = []*Device{
		{ID: 1, LicenseID: lic.ID, HardwareHash: hwHash, Status: "active"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/system/license/heartbeat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCustomerAPI_OfflineActivate_InvalidSignature(t *testing.T) {
	api, _ := newTestCustomerAPI(t)
	e := setupEcho(api)

	body, _ := json.Marshal(map[string]string{
		"signed_license":  "not-a-real-base64-payload",
		"activation_code": "ABCDEFGH",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/system/license/offline-activate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid signed_license, got %d: %s", rec.Code, rec.Body.String())
	}
}

// currentTestFingerprintHash returns the same fingerprint hash the API handlers
// will compute on the test host. Avoids hard-coding hardware-specific data.
func currentTestFingerprintHash(t *testing.T) string {
	t.Helper()
	fp, err := GenerateFingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fp.Hash()
}
