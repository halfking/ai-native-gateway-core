package licensing

import (
	"crypto/rsa"
	"fmt"
	"os"
)

// EnforceAtStartup performs license verification at startup.
// It verifies the local license file and checks for clock tampering.
// Returns an error if verification fails, allowing the caller to decide
// whether to enter restricted mode or block startup entirely.
func EnforceAtStartup(licensePath, publicKeyPath, dataDir string) error {
	// Load public key
	pubKeyPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}

	// Verify local license (this internally calls CheckClockTampering)
	_, err = VerifyLocalLicense(licensePath, pubKeyPEM, dataDir)
	if err != nil {
		return fmt.Errorf("verify license: %w", err)
	}

	return nil
}

// verifyLocalLicenseWithKey is a helper for testing that accepts an RSA public key directly.
func verifyLocalLicenseWithKey(licensePath string, pubKey *rsa.PublicKey, dataDir string) error {
	// Check for clock tampering first
	if err := CheckClockTampering(dataDir); err != nil {
		return fmt.Errorf("clock tampering check: %w", err)
	}

	// Read license file
	data, err := os.ReadFile(licensePath)
	if err != nil {
		return fmt.Errorf("read license file: %w", err)
	}

	// Unmarshal base64-encoded signed license
	signed, err := UnmarshalFromBase64(string(data))
	if err != nil {
		return fmt.Errorf("unmarshal license: %w", err)
	}

	// Create crypto config with public key
	crypto := &CryptoConfig{
		PublicKey: pubKey,
	}

	// Verify signature and expiration
	lic, err := crypto.VerifyLicense(signed)
	if err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}

	// Check revocation
	if lic.RevokedAt != nil {
		return ErrLicenseRevoked
	}

	return nil
}
