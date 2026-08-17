package startup

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMigration531TenantUniquenessContract(t *testing.T) {
	up, err := os.ReadFile("531_request_journey_tenant_uniqueness.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)
	for _, required := range []string{
		"DROP INDEX IF EXISTS uq_state_transitions_request_seq",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_legacy_request_seq",
		"WHERE event_type IS NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_tenant_request_seq",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("531 missing %q", required)
		}
	}
	down, err := os.ReadFile("531_request_journey_tenant_uniqueness.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_request_seq") {
		t.Error("531 down must restore legacy global uniqueness")
	}
}

func TestMigration530DeployedChecksumStable(t *testing.T) {
	body, err := os.ReadFile("530_request_journey_contract.sql")
	if err != nil {
		t.Fatal(err)
	}
	const want = "70162427750db07a35b91e491ead8123e62df23ccb5bdb0dd73b34ae3321cd34"
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		t.Fatalf("530 checksum=%s want=%s", got, want)
	}
}
