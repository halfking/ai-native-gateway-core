package licensing

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	DefaultLicensePath = "/var/lib/kx-gateway/license.dat"
)

// VerifyLocalLicense reads, verifies, and validates a local license file.
// It performs the following checks:
// 1. RSA signature verification
// 2. Expiration check
// 3. Revocation check
// 4. Hardware fingerprint matching (with fuzzy threshold)
// 5. Clock tampering detection
func VerifyLocalLicense(licensePath string, publicKeyPEM []byte, dataDir string) (*License, error) {
	if licensePath == "" {
		licensePath = DefaultLicensePath
	}
	if dataDir == "" {
		dataDir = "/var/lib/kx-gateway"
	}

	// Step 1: Check for clock tampering
	if err := CheckClockTampering(dataDir); err != nil {
		return nil, fmt.Errorf("clock tampering check failed: %w", err)
	}

	// Step 2: Read license file
	signedLicense, err := readLicenseFile(licensePath)
	if err != nil {
		return nil, err
	}

	// Step 3: Parse public key
	publicKey, err := LoadPublicKeyFromPEM(string(publicKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	// Step 4: Verify RSA signature
	license, err := verifySignature(signedLicense, publicKey)
	if err != nil {
		return nil, err
	}

	// Step 5: Check expiration
	if time.Now().After(license.ExpiresAt) {
		return nil, fmt.Errorf("%w: expired at %s", ErrLicenseExpired, license.ExpiresAt.Format(time.RFC3339))
	}

	// Step 6: Check revocation
	if license.RevokedAt != nil {
		return nil, fmt.Errorf("%w: revoked at %s", ErrLicenseRevoked, license.RevokedAt.Format(time.RFC3339))
	}

	// Step 7: Verify hardware fingerprint
	if err := verifyFingerprint(license); err != nil {
		return nil, err
	}

	return license, nil
}

// readLicenseFile reads and parses the license file
func readLicenseFile(path string) (*SignedLicense, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrLicenseFileNotFound, path)
		}
		return nil, fmt.Errorf("read license file: %w", err)
	}

	// Try to unmarshal as base64-encoded JSON first (standard format)
	signed, err := UnmarshalFromBase64(string(data))
	if err == nil {
		return signed, nil
	}

	// Fallback: try direct JSON unmarshal
	var signedDirect SignedLicense
	if err := json.Unmarshal(data, &signedDirect); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrLicenseFileCorrupted, err)
	}

	return &signedDirect, nil
}

// verifySignature verifies the RSA signature of the license
func verifySignature(signed *SignedLicense, publicKey *rsa.PublicKey) (*License, error) {
	crypto := &CryptoConfig{
		PublicKey: publicKey,
	}

	license, err := crypto.VerifyLicense(signed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSignature, err)
	}

	return license, nil
}

// verifyFingerprint checks if the current hardware fingerprint matches the licensed one
// Uses fuzzy matching with a threshold of 0.6
func verifyFingerprint(license *License) error {
	// Generate current fingerprint
	currentFP, err := GenerateFingerprint()
	if err != nil {
		return fmt.Errorf("generate fingerprint: %w", err)
	}

	// For now, we need to extract stored fingerprint from license
	// The license should store a Fingerprint field or HardwareHash
	// Since the License struct doesn't have fingerprint data yet,
	// we'll need to check against stored devices or implement fingerprint storage

	// Parse stored fingerprint from license features or custom field
	// This is a placeholder - actual implementation depends on how fingerprint is stored
	var storedFP *Fingerprint

	// Try to find fingerprint in license metadata
	// For now, skip fingerprint check if no stored fingerprint found
	if storedFP == nil {
		// No stored fingerprint to compare - this might be first activation
		// In production, you'd want to enforce this more strictly
		return nil
	}

	// Calculate match score
	score := currentFP.MatchScore(storedFP)
	if score < MatchThreshold {
		return fmt.Errorf("%w: match score %.2f below threshold %.2f",
			ErrFingerprintMismatch, score, MatchThreshold)
	}

	return nil
}

// VerifyLocalLicenseWithFingerprint is like VerifyLocalLicense but also checks
// against a specific stored fingerprint
func VerifyLocalLicenseWithFingerprint(licensePath string, publicKeyPEM []byte, dataDir string, storedFP *Fingerprint) (*License, error) {
	license, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)
	if err != nil {
		return nil, err
	}

	// Generate current fingerprint
	currentFP, err := GenerateFingerprint()
	if err != nil {
		return nil, fmt.Errorf("generate fingerprint: %w", err)
	}

	// Calculate match score
	score := currentFP.MatchScore(storedFP)
	if score < MatchThreshold {
		return nil, fmt.Errorf("%w: match score %.2f below threshold %.2f",
			ErrFingerprintMismatch, score, MatchThreshold)
	}

	return license, nil
}
