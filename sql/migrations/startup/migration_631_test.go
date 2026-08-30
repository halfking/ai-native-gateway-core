package startup

import (
	"os"
	"strings"
	"testing"
)

// stripSQLComments drops line comments and block comments so substring
// searches aren't confused by inline annotations.
func stripSQLCommentsFor627(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestMigration631AddsDeletedStatusAndProviderDeletedAt locks in the
// two schema changes behind the 2026-08-31 soft-delete work:
//
//   - credentials.status CHECK gains the terminal 'deleted' value
//   - providers.deleted_at timestamptz + partial index for live rows
//
// Future migrations that need to remove the constraint or column must
// intentionally update this test (or write their own down-migration).
func TestMigration631AddsDeletedStatusAndProviderDeletedAt(t *testing.T) {
	data, err := os.ReadFile("631_provider_credential_soft_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLCommentsFor627(string(data))

	mustContain := []string{
		// 凭据 status 终态值
		"ALTER TABLE public.credentials",
		"DROP CONSTRAINT IF EXISTS credentials_status_check",
		"ADD CONSTRAINT credentials_status_check",
		"'deleted'::text",
		// 供应商 deleted_at + 偏索引
		"ALTER TABLE public.providers",
		"ADD COLUMN IF NOT EXISTS deleted_at timestamptz",
		"CREATE INDEX IF NOT EXISTS idx_providers_live",
		"WHERE deleted_at IS NULL",
		// 事务包裹
		"BEGIN",
		"COMMIT",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("migration 631 missing %q", want)
		}
	}

	// 反向断言：迁移不应该改动 credentials 之外的 status 约束（即
	// lifecycle_status、availability_state 等不受影响）。
	if strings.Contains(body, "credentials_lifecycle_status_check") {
		t.Errorf("migration 631 must not touch credentials_lifecycle_status_check")
	}
	if strings.Contains(body, "DROP TABLE") {
		t.Errorf("migration 631 must not drop any table")
	}
}
