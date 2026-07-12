package licensing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	// DefaultOfflineVerificationInterval is the default interval for offline license verification (6 hours)
	DefaultOfflineVerificationInterval = 6 * time.Hour

	// RenewalWarningThreshold is how far before expiry to start warning (30 days)
	RenewalWarningThreshold = 30 * 24 * time.Hour
)

// OfflineVerificationDaemon runs a periodic verification loop for offline license mode (M1).
// It verifies the local license.dat file every `interval` (default 6h) and handles failures:
//   - Expired or revoked license → enters community mode (EnterCommunityMode)
//   - Fingerprint mismatch → logs slog.Error (does not block startup in M1 mode)
//   - Clock tampering → logs slog.Error
//
// The daemon respects context cancellation and returns when ctx is done.
// In M1 mode, heartbeats are DISABLED (no network calls to the main control server).
func OfflineVerificationDaemon(ctx context.Context, interval time.Duration, licensePath, publicKeyPath, dataDir string) error {
	if interval <= 0 {
		interval = DefaultOfflineVerificationInterval
	}

	// Load public key once at startup
	publicKeyPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		slog.Error("offline_cron: failed to load public key, entering community mode",
			"path", publicKeyPath,
			"error", err)
		if err := EnterCommunityMode(); err != nil {
			slog.Error("offline_cron: failed to enter community mode", "error", err)
		}
		return fmt.Errorf("load public key: %w", err)
	}

	slog.Info("offline_cron: verification daemon started",
		"interval", interval,
		"license_path", licensePath,
		"data_dir", dataDir)

	// Run first verification immediately
	verifyOnce(licensePath, publicKeyPEM, dataDir)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("offline_cron: daemon stopped", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			verifyOnce(licensePath, publicKeyPEM, dataDir)
		}
	}
}

// verifyOnce performs a single license verification and handles failures
func verifyOnce(licensePath string, publicKeyPEM []byte, dataDir string) {
	license, err := VerifyLocalLicense(licensePath, publicKeyPEM, dataDir)
	if err != nil {
		slog.Error("offline_cron: license verification failed",
			"error", err,
			"action", "entering_community_mode")

		// Enter community mode on any verification failure
		// (expired, revoked, corrupted, signature invalid, etc.)
		if enterErr := EnterCommunityMode(); enterErr != nil {
			slog.Error("offline_cron: failed to enter community mode", "error", enterErr)
		}
		return
	}

	// License is valid - check if renewal warning needed
	timeUntilExpiry := time.Until(license.ExpiresAt)
	if timeUntilExpiry > 0 && timeUntilExpiry <= RenewalWarningThreshold {
		daysLeft := int(timeUntilExpiry.Hours() / 24)
		slog.Warn("offline_cron: license expiring soon, please renew",
			"expires_at", license.ExpiresAt.Format(time.RFC3339),
			"days_left", daysLeft,
			"license_key", license.LicenseKey)
	} else {
		slog.Info("offline_cron: license verification successful",
			"license_key", license.LicenseKey,
			"expires_at", license.ExpiresAt.Format(time.RFC3339))
	}
}
