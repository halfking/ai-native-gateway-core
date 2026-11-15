package casdoor

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// MFAManager handles Multi-Factor Authentication operations.
type MFAManager struct {
	issuer string
}

// NewMFAManager creates a new MFA manager.
func NewMFAManager(issuer string) *MFAManager {
	if issuer == "" {
		issuer = "LLM Gateway"
	}
	return &MFAManager{
		issuer: issuer,
	}
}

// MFASecret represents an MFA secret configuration.
type MFASecret struct {
	Secret    string    `json:"secret"`
	QRCodeURL string    `json:"qr_code_url"`
	Issuer    string    `json:"issuer"`
	Account   string    `json:"account"`
	CreatedAt time.Time `json:"created_at"`
}

// MFAVerifyRequest represents an MFA verification request.
type MFAVerifyRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

// GenerateMFASecret generates a new MFA secret for a user.
func (m *MFAManager) GenerateMFASecret(ctx context.Context, username string) (*MFASecret, error) {
	// Generate TOTP key
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      m.issuer,
		AccountName: username,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp key: %w", err)
	}

	return &MFASecret{
		Secret:    key.Secret(),
		QRCodeURL: key.URL(),
		Issuer:    m.issuer,
		Account:   username,
		CreatedAt: time.Now(),
	}, nil
}

// VerifyMFACode verifies an MFA code against a secret.
func (m *MFAManager) VerifyMFACode(ctx context.Context, secret, code string) (bool, error) {
	// Verify TOTP code
	valid := totp.Validate(code, secret)
	return valid, nil
}

// VerifyMFACodeWithWindow verifies an MFA code with time window tolerance.
// Window allows codes from +/- N time periods (default 1 = 30s before/after).
func (m *MFAManager) VerifyMFACodeWithWindow(ctx context.Context, secret, code string, window uint) (bool, error) {
	// Verify with time window
	valid, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      window,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return false, fmt.Errorf("validate totp: %w", err)
	}
	return valid, nil
}

// GenerateBackupCodes generates backup codes for MFA recovery.
func (m *MFAManager) GenerateBackupCodes(ctx context.Context, count int) ([]string, error) {
	if count <= 0 {
		count = 10 // Default 10 backup codes
	}

	codes := make([]string, count)
	for i := 0; i < count; i++ {
		code, err := generateRandomCode(8)
		if err != nil {
			return nil, fmt.Errorf("generate backup code: %w", err)
		}
		codes[i] = code
	}

	return codes, nil
}

// generateRandomCode generates a random alphanumeric code.
func generateRandomCode(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes)[:length], nil
}

// MFAConfig represents MFA configuration for a user.
type MFAConfig struct {
	Enabled      bool      `json:"enabled"`
	Secret       string    `json:"secret,omitempty"`
	BackupCodes  []string  `json:"backup_codes,omitempty"`
	LastVerified time.Time `json:"last_verified,omitempty"`
}

// EnableMFA enables MFA for a user and returns the secret.
func (m *MFAManager) EnableMFA(ctx context.Context, username string) (*MFASecret, []string, error) {
	// Generate MFA secret
	secret, err := m.GenerateMFASecret(ctx, username)
	if err != nil {
		return nil, nil, err
	}

	// Generate backup codes
	backupCodes, err := m.GenerateBackupCodes(ctx, 10)
	if err != nil {
		return nil, nil, err
	}

	return secret, backupCodes, nil
}

// DisableMFA disables MFA for a user.
func (m *MFAManager) DisableMFA(ctx context.Context, username string) error {
	// In a real implementation, this would remove MFA configuration from storage
	// For now, this is a placeholder
	return nil
}

// GetMFAStatus returns the MFA status for a user.
func (m *MFAManager) GetMFAStatus(ctx context.Context, username string) (*MFAConfig, error) {
	// In a real implementation, this would fetch MFA config from storage
	// For now, return a placeholder
	return &MFAConfig{
		Enabled: false,
	}, nil
}
