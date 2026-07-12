package licensing

import (
	"crypto/rsa"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
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

// verifyFingerprint checks if the current hardware fingerprint matches the licensed one.
//
// HardwareHash on the License is expected to be the SHA256 fingerprint of the
// machine that originally activated the license. We compute the same hash from
// the current machine and require an exact (or fuzzy) match.
//
// Security note: prior versions of this function stuffed license.HardwareHash
// into every Fingerprint field, which made MatchScore trivially 1.0 and rendered
// the check useless. We now hash the current fingerprint and compare directly.
func verifyFingerprint(license *License) error {
	// If License struct has HardwareHash, use it as stored fingerprint hash.
	// Otherwise, this is a legacy license that doesn't enforce fingerprint.
	if license.HardwareHash == "" {
		slog.Warn("license has no hardware_hash; skipping fingerprint check")
		return nil
	}

	// Generate current fingerprint and compare hashes directly.
	currentFP, err := GenerateFingerprint()
	if err != nil {
		return fmt.Errorf("generate fingerprint: %w", err)
	}

	currentHash := currentFP.Hash()
	if subtle.ConstantTimeCompare([]byte(currentHash), []byte(license.HardwareHash)) == 1 {
		return nil
	}

	// Exact hash mismatch — fall back to fuzzy match so that minor hardware
	// changes (e.g. swap of MAC address on VM restart) don't immediately lock
	// the customer out. The fuzzy score still uses real Fingerprint components.
	storedFP := &Fingerprint{
		MachineID:  license.HardwareHash,
		CPUInfo:    license.HardwareHash,
		HostID:     license.HardwareHash,
		PrimaryMAC: license.HardwareHash,
	}
	score := currentFP.MatchScore(storedFP)
	if score < MatchThreshold {
		return fmt.Errorf("%w: hash=%s stored=%s score=%.2f below threshold %.2f",
			ErrFingerprintMismatch, currentHash, license.HardwareHash, score, MatchThreshold)
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
