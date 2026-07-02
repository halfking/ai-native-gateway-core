package migrations

import (
	"os"
	"testing"
)

// Migration 136 — credential plan type full standardization
// These tests are smoke tests: they confirm files exist with expected DDL
// constructs. The behavioral validation runs on live 184 k3s via the
// migrations/scripts/local-r112-migrate.sh + smoke tests in deploy step T1/T2.
//
// CI integration tests with a real PG are gated behind DATABASE_URL_TEST_MIG
// env var so this test passes in offline CI without PG available.

// TestMigration136_FilesExist verifies both up + down migrations are present.
// Without both files a deploy cannot proceed.
func TestMigration136_FilesExist(t *testing.T) {
	files := []string{
		"../migrations/136_credential_plan_type_full.sql",
		"../migrations/136_credential_plan_type_full.down.sql",
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("missing migration file %s: %v", f, err)
		}
	}
}

// TestMigration136_UpContainsRequiredDDL guards against the file being
// truncated or having steps removed. Asserts key SQL tokens are present.
func TestMigration136_UpContainsRequiredDDL(t *testing.T) {
	body, err := os.ReadFile("../migrations/136_credential_plan_type_full.sql")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	required := []string{
		"ALTER TABLE credentials DROP CONSTRAINT",
		"plan_type_origin", // column added
		"UPDATE credential_model_bindings cmb",
		"DROP VIEW IF EXISTS v_routable_credential_models CASCADE",
		"CREATE OR REPLACE VIEW v_routable_credential_models",
		"cmb.billing_mode",
		"plan_incompatible_cmb_requires_",
	}
	for _, r := range required {
		if !contains(s, r) {
			t.Errorf("migration missing required token: %q", r)
		}
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
