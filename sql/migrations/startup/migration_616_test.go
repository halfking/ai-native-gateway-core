package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration616DownUsesValidPLpgSQLNotice(t *testing.T) {
	down, err := os.ReadFile("616_provider_error_details_unique_constraint.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(down))
	if strings.Contains(text, "\nraise notice") {
		t.Fatal("migration 616 down script contains a top-level RAISE NOTICE")
	}
	if !strings.Contains(text, "do $$") || !strings.Contains(text, "raise notice") {
		t.Fatal("migration 616 down script must wrap RAISE NOTICE in DO block")
	}
}
