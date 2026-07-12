package licensing

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

var (
	// CommunityModeFile is the marker file that indicates community mode is active
	// Exported as a variable so tests can override it
	CommunityModeFile = "/var/lib/kx-gateway/community.mode"

	// MaxCommunityTenants is the maximum number of tenants allowed in community mode
	MaxCommunityTenants = 2
)

// EnterCommunityMode creates the community mode marker file.
// In community mode, the gateway operates with limited functionality:
//   - Maximum 2 tenants allowed
//   - Only basic API access (no advanced features)
//   - Suitable for expired/revoked licenses or trial mode
//
// Community mode does NOT block startup - it only restricts features.
func EnterCommunityMode() error {
	// Ensure parent directory exists
	dir := filepath.Dir(CommunityModeFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	// Create marker file with timestamp
	content := fmt.Sprintf("community_mode_entered_at=%d\n", os.Getpid())
	if err := os.WriteFile(CommunityModeFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("write marker file: %w", err)
	}

	slog.Warn("entered community mode",
		"max_tenants", MaxCommunityTenants,
		"restrictions", "basic API only",
		"marker_file", CommunityModeFile)

	return nil
}

// IsCommunityMode checks if community mode is currently active by checking
// for the presence of the community mode marker file.
func IsCommunityMode() bool {
	_, err := os.Stat(CommunityModeFile)
	return err == nil
}

// ExitCommunityMode removes the community mode marker file.
// This should be called after a valid license is activated.
func ExitCommunityMode() error {
	if !IsCommunityMode() {
		return nil // already not in community mode
	}

	if err := os.Remove(CommunityModeFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove marker file: %w", err)
	}

	slog.Info("exited community mode", "marker_file", CommunityModeFile)
	return nil
}

// GetCommunityModeRestrictions returns a description of community mode restrictions
// for user-facing error messages or status displays.
func GetCommunityModeRestrictions() map[string]interface{} {
	return map[string]interface{}{
		"mode":        "community",
		"max_tenants": MaxCommunityTenants,
		"features":    []string{"basic_api"},
		"restrictions": []string{
			"limited to 2 tenants",
			"basic API access only",
			"no advanced features",
		},
		"upgrade_hint": "activate a valid license to unlock full features",
	}
}
