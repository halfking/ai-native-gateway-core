package licensing

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfflineVerificationDaemon_Success(t *testing.T) {
	// Setup: create temp dir with valid license
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	publicKeyPath := filepath.Join(tmpDir, "server.pub")
	dataDir := filepath.Join(tmpDir, "data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	// Generate test key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create a valid license (expires in 1 year)
	license := &License{
		ID:               1,
		LicenseKey:       "LIC-TEST-KEY",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"core", "advanced"},
		ExpiresAt:        time.Now().Add(365 * 24 * time.Hour),
		CreatedAt:        time.Now(),
		RevokedAt:        nil,
	}

	// Sign and save license
	signed, err := crypto.SignLicense(license)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}

	encoded, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatalf("marshal license: %v", err)
	}

	if err := os.WriteFile(licensePath, []byte(encoded), 0644); err != nil {
		t.Fatalf("write license file: %v", err)
	}

	// Save public key
	pubKeyPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(publicKeyPath, []byte(pubKeyPEM), 0644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	// Test: start daemon with short interval
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	interval := 100 * time.Millisecond
	err = OfflineVerificationDaemon(ctx, interval, licensePath, publicKeyPath, dataDir)

	// Should return nil when context is cancelled (not an error condition)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Errorf("daemon failed: %v", err)
	}

	// Community mode should NOT be entered
	if IsCommunityMode() {
		t.Error("should not enter community mode with valid license")
	}
}

func TestOfflineVerificationDaemon_ExpiredLicense(t *testing.T) {
	// Setup: create temp dir with expired license
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	publicKeyPath := filepath.Join(tmpDir, "server.pub")
	dataDir := filepath.Join(tmpDir, "data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	// Override community mode file to tmpDir for testing
	origCommunityModeFile := CommunityModeFile
	CommunityModeFile = filepath.Join(tmpDir, "community.mode")
	defer func() { CommunityModeFile = origCommunityModeFile }()

	// Generate test key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create an EXPIRED license
	license := &License{
		ID:               1,
		LicenseKey:       "LIC-EXPIRED-KEY",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"core", "advanced"},
		ExpiresAt:        time.Now().Add(-24 * time.Hour), // expired yesterday
		CreatedAt:        time.Now().Add(-400 * 24 * time.Hour),
		RevokedAt:        nil,
	}

	// Sign and save license
	signed, err := crypto.SignLicense(license)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}

	encoded, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatalf("marshal license: %v", err)
	}

	if err := os.WriteFile(licensePath, []byte(encoded), 0644); err != nil {
		t.Fatalf("write license file: %v", err)
	}

	// Save public key
	pubKeyPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(publicKeyPath, []byte(pubKeyPEM), 0644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	// Clear community mode first
	clearCommunityMode(t)

	// Test: start daemon with short interval
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	interval := 50 * time.Millisecond
	err = OfflineVerificationDaemon(ctx, interval, licensePath, publicKeyPath, dataDir)

	// Daemon should run until context cancellation
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Errorf("daemon failed: %v", err)
	}

	// Community mode SHOULD be entered after first verification
	time.Sleep(100 * time.Millisecond) // give it time to write the file
	if !IsCommunityMode() {
		t.Error("should enter community mode with expired license")
	}
}

func TestOfflineVerificationDaemon_RevokedLicense(t *testing.T) {
	// Setup: create temp dir with revoked license
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	publicKeyPath := filepath.Join(tmpDir, "server.pub")
	dataDir := filepath.Join(tmpDir, "data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	// Override community mode file to tmpDir for testing
	origCommunityModeFile := CommunityModeFile
	CommunityModeFile = filepath.Join(tmpDir, "community.mode")
	defer func() { CommunityModeFile = origCommunityModeFile }()

	// Generate test key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create a REVOKED license
	revokedAt := time.Now().Add(-1 * time.Hour)
	license := &License{
		ID:               1,
		LicenseKey:       "LIC-REVOKED-KEY",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"core", "advanced"},
		ExpiresAt:        time.Now().Add(365 * 24 * time.Hour),
		CreatedAt:        time.Now().Add(-100 * 24 * time.Hour),
		RevokedAt:        &revokedAt,
	}

	// Sign and save license
	signed, err := crypto.SignLicense(license)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}

	encoded, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatalf("marshal license: %v", err)
	}

	if err := os.WriteFile(licensePath, []byte(encoded), 0644); err != nil {
		t.Fatalf("write license file: %v", err)
	}

	// Save public key
	pubKeyPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(publicKeyPath, []byte(pubKeyPEM), 0644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	// Clear community mode first
	clearCommunityMode(t)

	// Test: start daemon with short interval
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	interval := 50 * time.Millisecond
	err = OfflineVerificationDaemon(ctx, interval, licensePath, publicKeyPath, dataDir)

	// Daemon should run until context cancellation
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Errorf("daemon failed: %v", err)
	}

	// Community mode SHOULD be entered after first verification
	time.Sleep(100 * time.Millisecond)
	if !IsCommunityMode() {
		t.Error("should enter community mode with revoked license")
	}
}

func TestOfflineVerificationDaemon_FingerprintMismatch(t *testing.T) {
	// This test verifies that fingerprint mismatch is logged as error
	// but doesn't block daemon from continuing (only logs slog.Error)
	// For now, VerifyLocalLicense skips fingerprint check if no stored FP
	t.Skip("fingerprint check not fully implemented yet")
}

func TestOfflineVerificationDaemon_ContextCancellation(t *testing.T) {
	// Setup: create temp dir with valid license
	tmpDir := t.TempDir()
	licensePath := filepath.Join(tmpDir, "license.dat")
	publicKeyPath := filepath.Join(tmpDir, "server.pub")
	dataDir := filepath.Join(tmpDir, "data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	// Generate test key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	crypto := &CryptoConfig{
		PrivateKey: privKey,
		PublicKey:  &privKey.PublicKey,
	}

	// Create a valid license
	license := &License{
		ID:               1,
		LicenseKey:       "LIC-TEST-KEY",
		CustomerName:     "Test Customer",
		CustomerEmail:    "test@example.com",
		MaxDevices:       5,
		SubscriptionTier: "enterprise",
		Features:         []string{"core", "advanced"},
		ExpiresAt:        time.Now().Add(365 * 24 * time.Hour),
		CreatedAt:        time.Now(),
		RevokedAt:        nil,
	}

	// Sign and save license
	signed, err := crypto.SignLicense(license)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}

	encoded, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatalf("marshal license: %v", err)
	}

	if err := os.WriteFile(licensePath, []byte(encoded), 0644); err != nil {
		t.Fatalf("write license file: %v", err)
	}

	// Save public key
	pubKeyPEM := exportPublicKeyToPEM(&privKey.PublicKey)
	if err := os.WriteFile(publicKeyPath, []byte(pubKeyPEM), 0644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	// Test: cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	interval := 1 * time.Hour // long interval
	err = OfflineVerificationDaemon(ctx, interval, licensePath, publicKeyPath, dataDir)

	// Should return immediately without error (context already cancelled)
	if err != nil && err != context.Canceled {
		t.Errorf("expected context.Canceled or nil, got: %v", err)
	}
}

func TestIsCommunityMode(t *testing.T) {
	// Override community mode file to tmpDir for testing
	tmpDir := t.TempDir()
	origCommunityModeFile := CommunityModeFile
	CommunityModeFile = filepath.Join(tmpDir, "community.mode")
	defer func() { CommunityModeFile = origCommunityModeFile }()

	// Clear any existing mode
	clearCommunityMode(t)

	if IsCommunityMode() {
		t.Error("should not be in community mode initially")
	}

	// Enter community mode
	if err := EnterCommunityMode(); err != nil {
		t.Fatalf("enter community mode: %v", err)
	}

	if !IsCommunityMode() {
		t.Error("should be in community mode after entering")
	}

	// Clean up
	clearCommunityMode(t)
}

// Helper function to clear community mode for testing
func clearCommunityMode(t *testing.T) {
	t.Helper()
	if err := os.Remove(CommunityModeFile); err != nil && !os.IsNotExist(err) {
		t.Logf("warning: failed to clear community mode: %v", err)
	}
}
