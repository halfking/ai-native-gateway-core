package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration601DropsRedundantBodyMetadata(t *testing.T) {
	migration, err := os.ReadFile("601_request_logs_bodies_drop_metadata.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	body := string(migration)
	for _, required := range []string{
		"BEGIN;",
		"ALTER TABLE public.request_logs_bodies_hot",
		"DROP COLUMN IF EXISTS tenant_id",
		"COMMIT;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}
