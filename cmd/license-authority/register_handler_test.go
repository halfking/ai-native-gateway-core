package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/licensing"
)

var ErrLicenseNotFound = errors.New("license not found")

// Mock stores for testing
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
	licKey := "test_license"
	if m.devices[licKey] == nil {
		m.devices[licKey] = []licensing.Device{}
	}
	m.devices[licKey] = append(m.devices[licKey], *dev)
	return nil
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
