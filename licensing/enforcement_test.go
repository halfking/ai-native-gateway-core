package licensing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforceAtStartup_ValidLicense(t *testing.T) {
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	pubKeyPath := filepath.Join(tmpDir, "server.pub")

	// Generate test keys
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create valid license
	lic := &License{
		LicenseKey:       "test-key",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"base"},
		CreatedAt:        time.Now().Add(-24 * time.Hour),
	}

	signed, err := crypto.SignLicense(lic)
	if err != nil {
		t.Fatal(err)
	}

	// Save license file
	b64, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(licensePath, []byte(b64), 0644); err != nil {
		t.Fatal(err)
	}

	// Save public key
	pubPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(pubKeyPath, []byte(pubPEM), 0644); err != nil {
		t.Fatal(err)
	}

	// Test enforcement
	err = EnforceAtStartup(licensePath, pubKeyPath, tmpDir)
	if err != nil {
		t.Errorf("expected no error for valid license, got: %v", err)
	}
}

func TestEnforceAtStartup_MissingLicense(t *testing.T) {
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "nonexistent.dat")
	pubKeyPath := filepath.Join(tmpDir, "server.pub")

	err := EnforceAtStartup(licensePath, pubKeyPath, tmpDir)
	if err == nil {
		t.Error("expected error for missing license file")
	}
}

func TestEnforceAtStartup_ExpiredLicense(t *testing.T) {
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	pubKeyPath := filepath.Join(tmpDir, "server.pub")

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create expired license
	lic := &License{
		LicenseKey:       "test-key",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		ExpiresAt:        time.Now().Add(-24 * time.Hour), // expired
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"base"},
		CreatedAt:        time.Now().Add(-48 * time.Hour),
	}

	signed, err := crypto.SignLicense(lic)
	if err != nil {
		t.Fatal(err)
	}

	b64, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(licensePath, []byte(b64), 0644); err != nil {
		t.Fatal(err)
	}

	pubPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(pubKeyPath, []byte(pubPEM), 0644); err != nil {
		t.Fatal(err)
	}

	err = EnforceAtStartup(licensePath, pubKeyPath, tmpDir)
	if err == nil {
		t.Error("expected error for expired license")
	}
}

func TestEnforceAtStartup_InvalidSignature(t *testing.T) {
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	pubKeyPath := filepath.Join(tmpDir, "server.pub")

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	lic := &License{
		LicenseKey:       "test-key",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"base"},
		CreatedAt:        time.Now().Add(-24 * time.Hour),
	}

	signed, err := crypto.SignLicense(lic)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt signature
	signed.Signature[0] ^= 0xFF

	b64, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(licensePath, []byte(b64), 0644); err != nil {
		t.Fatal(err)
	}

	pubPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(pubKeyPath, []byte(pubPEM), 0644); err != nil {
		t.Fatal(err)
	}

	err = EnforceAtStartup(licensePath, pubKeyPath, tmpDir)
	if err == nil {
		t.Error("expected error for invalid signature")
	}
}

// Helper function to export public key to PEM format
func exportPublicKeyToPEM(pubKey *rsa.PublicKey) string {
	pubASN1, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		panic(err)
	}

	pubBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubASN1,
	})

	return string(pubBytes)
}
