package startup

import (
	"crypto/sha256"
	"fmt"
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

func TestDeployedMigration528ChecksumIsStable(t *testing.T) {
	const (
		name = "528_request_logs_bodies_expired_hot_cleanup.sql"
		want = "6998edec4991e2cd261061c5094e21829b85d699becabeaadc3a86dfc5d56cd1"
	)
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		t.Fatalf("deployed startup migration %s checksum changed: got %s want %s", name, got, want)
	}
}

func TestDeployedMigration529ChecksumIsStable(t *testing.T) {
	const (
		name = "529_repair_shared_pg_sticky_and_bodies_2026_07.sql"
		want = "3a21c6943da9ede672c4a0f2074bcbfee3e94efcd4e84dec09b12df3154d41c6"
	)
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		t.Fatalf("deployed startup migration %s checksum changed: got %s want %s", name, got, want)
	}
}
