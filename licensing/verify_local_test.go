package licensing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test helpers
func generateTestKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	return privateKey, &privateKey.PublicKey
}

func createTestLicense(t *testing.T, privateKey *rsa.PrivateKey, expiresAt time.Time, revokedAt *time.Time) *SignedLicense {
	t.Helper()

	crypto := &CryptoConfig{
		PrivateKey: privateKey,
	}

	license := &License{
		ID:               1,
		LicenseKey:       "TEST-LICENSE-KEY-123",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"feature1", "feature2"},
		ExpiresAt:        expiresAt,
		CreatedAt:        time.Now().Add(-30 * 24 * time.Hour),
		RevokedAt:        revokedAt,
	}

	signed, err := crypto.SignLicense(license)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}

	return signed
}

func writeLicenseFile(t *testing.T, path string, signed *SignedLicense) {
	t.Helper()

	data, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal license: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create dir: %v", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write license file: %v", err)
	}
}

func TestVerifyLocalLicense_Success(t *testing.T) {
	// Setup
	privateKey, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create valid license (expires in 1 year)
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, nil)
	writeLicenseFile(t, licensePath, signed)

	// Get public key PEM
	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Test
	license, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)

	// Verify
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if license == nil {
		t.Fatal("expected license, got nil")
	}
	if license.LicenseKey != "TEST-LICENSE-KEY-123" {
		t.Errorf("expected license key TEST-LICENSE-KEY-123, got %s", license.LicenseKey)
	}
}

func TestVerifyLocalLicense_Expired(t *testing.T) {
	// Setup
	privateKey, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create expired license
	expiresAt := time.Now().Add(-24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, nil)
	writeLicenseFile(t, licensePath, signed)

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Test
	_, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)

	// Verify
	if err == nil {
		t.Fatal("expected error for expired license, got nil")
	}
	if !errors.Is(err, ErrLicenseExpired) && !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expired license error, got: %v", err)
	}
}

func TestVerifyLocalLicense_Revoked(t *testing.T) {
	// Setup
	privateKey, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create revoked license
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	revokedAt := time.Now().Add(-24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, &revokedAt)
	writeLicenseFile(t, licensePath, signed)

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Test
	_, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)

	// Verify
	if err == nil {
		t.Fatal("expected error for revoked license, got nil")
	}
	if !errors.Is(err, ErrLicenseRevoked) && !strings.Contains(err.Error(), "revoked") {
		t.Errorf("expected revoked license error, got: %v", err)
	}
}

func TestVerifyLocalLicense_InvalidSignature(t *testing.T) {
	// Setup
	privateKey, _ := generateTestKeyPair(t)
	_, wrongPublicKey := generateTestKeyPair(t) // Different key pair
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create license signed with one key
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, nil)
	writeLicenseFile(t, licensePath, signed)

	// Try to verify with different public key
	wrongPublicKeyPEM := exportPublicKeyPEM(t, wrongPublicKey)

	// Test
	_, err := VerifyLocalLicense(licensePath, wrongPublicKeyPEM, dataDir)

	// Verify
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) && !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected signature error, got: %v", err)
	}
}

func TestVerifyLocalLicense_FileNotFound(t *testing.T) {
	// Setup
	_, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "nonexistent.dat")
	dataDir := filepath.Join(tempDir, "data")

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Test
	_, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)

	// Verify
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !errors.Is(err, ErrLicenseFileNotFound) && !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected file not found error, got: %v", err)
	}
}

func TestVerifyLocalLicense_CorruptedFile(t *testing.T) {
	// Setup
	_, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Write corrupted data
	if err := os.MkdirAll(filepath.Dir(licensePath), 0755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(licensePath, []byte("corrupted data"), 0644); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Test
	_, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)

	// Verify
	if err == nil {
		t.Fatal("expected error for corrupted file, got nil")
	}
	if !errors.Is(err, ErrLicenseFileCorrupted) && !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("expected corrupted file error, got: %v", err)
	}
}

func TestVerifyLocalLicenseWithFingerprint_Success(t *testing.T) {
	// Setup
	privateKey, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create valid license
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, nil)
	writeLicenseFile(t, licensePath, signed)

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Get current fingerprint as "stored" fingerprint
	storedFP, err := GenerateFingerprint()
	if err != nil {
		t.Fatalf("generate fingerprint: %v", err)
	}

	// Test
	license, err := VerifyLocalLicenseWithFingerprint(licensePath, publicKeyPEM, dataDir, storedFP)

	// Verify
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if license == nil {
		t.Fatal("expected license, got nil")
	}
}

func TestVerifyLocalLicenseWithFingerprint_Mismatch(t *testing.T) {
	// Setup
	privateKey, publicKey := generateTestKeyPair(t)
	tempDir := t.TempDir()
	licensePath := filepath.Join(tempDir, "license.dat")
	dataDir := filepath.Join(tempDir, "data")

	// Create valid license
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	signed := createTestLicense(t, privateKey, expiresAt, nil)
	writeLicenseFile(t, licensePath, signed)

	publicKeyPEM := exportPublicKeyPEM(t, publicKey)

	// Create completely different fingerprint
	storedFP := &Fingerprint{
		MachineID:  "different-machine-id",
		CPUInfo:    "different-cpu",
		HostID:     "different-host",
		PrimaryMAC: "00:11:22:33:44:55",
	}

	// Test
	_, err := VerifyLocalLicenseWithFingerprint(licensePath, publicKeyPEM, dataDir, storedFP)

	// Verify
	if err == nil {
		t.Fatal("expected error for fingerprint mismatch, got nil")
	}
	if !errors.Is(err, ErrFingerprintMismatch) && !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("expected fingerprint mismatch error, got: %v", err)
	}
}

// Helper to export public key as PEM
func exportPublicKeyPEM(t *testing.T, publicKey *rsa.PublicKey) []byte {
	t.Helper()

	pubASN1, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubASN1,
	})

	return pubPEM
}
