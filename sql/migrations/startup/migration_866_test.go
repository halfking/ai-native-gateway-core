package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func TestMigration866MatchesRuntimeSchemaAndInstaller(t *testing.T) {
	const name = "866_sanitize_rule_management.sql"
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), sanitizerules.SchemaSQL) {
		t.Fatal("runtime schema drift")
	}
	embedded, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + name)
	if err != nil || string(raw) != string(embedded) {
		t.Fatal("installer schema drift", err)
	}
	rb, err := os.ReadFile("866_sanitize_rule_management.down.sql")
	if err != nil || !strings.Contains(string(rb), sanitizerules.RollbackSchemaSQL) {
		t.Fatal("rollback missing", err)
	}
}
