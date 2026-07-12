package licensing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckClockTampering_FirstRun(t *testing.T) {
	tempDir := t.TempDir()

	err := CheckClockTampering(tempDir)
	if err != nil {
		t.Fatalf("first run should succeed: %v", err)
	}

	// Verify file was created
	bootFile := filepath.Join(tempDir, "last_boot.txt")
	if _, err := os.Stat(bootFile); os.IsNotExist(err) {
		t.Fatal("last_boot.txt should have been created")
	}
}

func TestCheckClockTampering_NormalBoot(t *testing.T) {
	tempDir := t.TempDir()

	// First boot
	if err := CheckClockTampering(tempDir); err != nil {
		t.Fatalf("first boot failed: %v", err)
	}

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Second boot (should succeed)
	if err := CheckClockTampering(tempDir); err != nil {
		t.Fatalf("second boot should succeed: %v", err)
	}
}

func TestCheckClockTampering_ClockRollback(t *testing.T) {
	tempDir := t.TempDir()
	bootFile := filepath.Join(tempDir, "last_boot.txt")

	// Write a future timestamp (simulating clock rollback)
	futureTime := time.Now().Add(1 * time.Hour)
	if err := writeLastBootTime(bootFile, futureTime); err != nil {
		t.Fatalf("write future time: %v", err)
	}

	// Check should fail due to rollback
	err := CheckClockTampering(tempDir)
	if err == nil {
		t.Fatal("expected error for clock rollback, got nil")
	}
	// Check if error is ErrClockRollback or contains "rollback"
	if err != ErrClockRollback && !contains(err.Error(), "rollback") {
		t.Errorf("expected clock rollback error, got: %v", err)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCheckClockTampering_WithinTolerance(t *testing.T) {
	tempDir := t.TempDir()
	bootFile := filepath.Join(tempDir, "last_boot.txt")

	// Write a timestamp slightly in the future (within 5 minute tolerance)
	slightlyFuture := time.Now().Add(2 * time.Minute)
	if err := writeLastBootTime(bootFile, slightlyFuture); err != nil {
		t.Fatalf("write slightly future time: %v", err)
	}

	// Should succeed because within tolerance
	if err := CheckClockTampering(tempDir); err != nil {
		t.Fatalf("should succeed within tolerance: %v", err)
	}
}

func TestCheckClockTampering_CorruptedFile(t *testing.T) {
	tempDir := t.TempDir()
	bootFile := filepath.Join(tempDir, "last_boot.txt")

	// Write corrupted data
	if err := os.WriteFile(bootFile, []byte("corrupted"), 0644); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}

	// Should handle gracefully and reinitialize
	if err := CheckClockTampering(tempDir); err != nil {
		t.Fatalf("should handle corrupted file gracefully: %v", err)
	}
}
