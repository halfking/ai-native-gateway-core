package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 680 bootstraps the canonical request_logs query view when it has
// been dropped out-of-band (2026-09-07 /request-logs 42P01 incident). The
// contract test pins the staged wrapper-chain shape so a future edit cannot
// silently drift the column contract (base UNION + customer_id 577 +
// request_class/due_at 610).
func TestMigration680BootstrapsCurrentMonthView(t *testing.T) {
	up, err := os.ReadFile("680_request_logs_current_month_view_bootstrap.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("680_request_logs_current_month_view_bootstrap.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for _, sql := range []string{string(up), string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Fatal("view migration must be transactional")
		}
	}
	upText := string(up)
	// Purely additive: the bootstrap must never DROP or RENAME an existing
	// relation — the incident state it repairs is exactly a half-applied
	// DROP+CREATE sequence. Check executable statements only; the header
	// comments legitimately quote the 341-style replay that caused it.
	var ddlStatements []string
	for _, line := range strings.Split(upText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		ddlStatements = append(ddlStatements, strings.ToUpper(trimmed))
	}
	executable := strings.Join(ddlStatements, "\n")
	if strings.Contains(executable, "DROP VIEW") {
		t.Error("680 up must not DROP any view (purely additive bootstrap)")
	}
	if strings.Contains(executable, "RENAME") {
		t.Error("680 up must not RENAME any view (purely additive bootstrap)")
	}
	for _, needle := range []string{
		"request_logs_with_current_month",
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs_with_current_month_without_customer_id",
		"public.request_logs_hot",
		"public.request_logs",
		"customer_id",
		"request_class",
		"due_at",
		"NOT IN ('customer_id', 'request_class', 'due_at')",
		"LEFT JOIN LATERAL",
	} {
		if !strings.Contains(upText, needle) {
			t.Errorf("migration missing %q", needle)
		}
	}
}
