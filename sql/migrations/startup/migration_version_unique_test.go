package startup

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestNumericUpMigrationVersionsAreUnique(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	versionPattern := regexp.MustCompile(`^([0-9]+)_.+\.sql$`)
	seen := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		match := versionPattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		// Versions before 492 contain audited historical collisions. The current
		// migration line is collision-free and must stay that way.
		if err != nil || version < 492 {
			continue
		}
		if previous, exists := seen[match[1]]; exists {
			t.Fatalf("startup migration version %s is duplicated by %s and %s", match[1], previous, name)
		}
		seen[match[1]] = name
	}
}
