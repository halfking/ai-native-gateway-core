package compressor

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidateSessionID checks if gwSessionID follows the expected format.
// Returns error if the ID is too short, contains only digits (likely a
// collision-prone auto-increment), or is a common placeholder.
//
// This validation prevents session cross-talk by rejecting weak session IDs
// that could collide across different clients or tenants.
func ValidateSessionID(gwSessionID string) error {
	if len(gwSessionID) < 8 {
		return fmt.Errorf("session_id too short: %s", gwSessionID)
	}

	// Reject numeric-only IDs (e.g., "12345678") as they are collision-prone
	// if multiple clients use simple auto-increment.
	if matched, _ := regexp.MatchString(`^\d+$`, gwSessionID); matched {
		return fmt.Errorf("session_id is numeric-only (collision risk): %s", gwSessionID)
	}

	// Reject common placeholders that indicate the client didn't generate
	// a proper unique ID.
	forbidden := []string{"test", "demo", "default", "session", "temp", "placeholder"}
	lower := strings.ToLower(gwSessionID)
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			return fmt.Errorf("session_id contains forbidden keyword '%s': %s", f, gwSessionID)
		}
	}

	return nil
}
