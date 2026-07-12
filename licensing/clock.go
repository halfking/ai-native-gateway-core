package licensing

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	lastBootFile       = "last_boot.txt"
	clockSkewTolerance = 300 * time.Second // 5 minutes tolerance
)

// CheckClockTampering detects system clock rollback by comparing current time
// with the last recorded boot time. Returns ErrClockRollback if tampering detected.
func CheckClockTampering(dataDir string) error {
	if dataDir == "" {
		dataDir = "/var/lib/kx-gateway"
	}

	bootFilePath := filepath.Join(dataDir, lastBootFile)
	now := time.Now()

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Read last boot time
	lastBoot, err := readLastBootTime(bootFilePath)
	if err != nil {
		// First run or corrupted file - initialize and continue
		return writeLastBootTime(bootFilePath, now)
	}

	// Check for clock rollback (with tolerance for minor clock skew)
	if now.Before(lastBoot.Add(-clockSkewTolerance)) {
		return fmt.Errorf("%w: current time %s is before last boot %s",
			ErrClockRollback, now.Format(time.RFC3339), lastBoot.Format(time.RFC3339))
	}

	// Update last boot time
	return writeLastBootTime(bootFilePath, now)
}

func readLastBootTime(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}

	timestamp := strings.TrimSpace(string(data))
	unixTime, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp: %w", err)
	}

	return time.Unix(unixTime, 0), nil
}

func writeLastBootTime(path string, t time.Time) error {
	data := fmt.Sprintf("%d\n", t.Unix())
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		return fmt.Errorf("write last boot time: %w", err)
	}
	return nil
}
