package telemetry

import (
	"os"
	"strings"
	"testing"
)

func TestRequestClassMigrationPreservesAndRestoresViewWrapper(t *testing.T) {
	up, err := os.ReadFile("../../../../sql/migrations/startup/608_request_class_due_at.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("../../../../sql/migrations/startup/608_request_class_due_at.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	for _, want := range []string{
		"ALTER VIEW public.request_logs_with_current_month\n      RENAME TO request_logs_with_current_month_without_request_class_due_at",
		"FROM public.request_logs_with_current_month_without_request_class_due_at v",
		"a.attname NOT IN ('request_class', 'due_at')",
		"constraint_name := tbl || '_request_class_due_at_check'",
		"ADD CONSTRAINT %I CHECK ((request_class = ''immediate'' AND due_at IS NULL) OR (request_class = ''scheduled'' AND due_at IS NOT NULL))",
	} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("up migration missing %q", want)
		}
	}
	for _, want := range []string{
		"DROP VIEW IF EXISTS public.request_logs_with_current_month",
		"ALTER VIEW public.request_logs_with_current_month_without_request_class_due_at",
		"RENAME TO request_logs_with_current_month",
		"a.attname NOT IN ('request_class', 'due_at')",
		"DROP CONSTRAINT IF EXISTS request_logs_hot_request_class_due_at_check",
	} {
		if !strings.Contains(string(down), want) {
			t.Fatalf("down migration missing %q", want)
		}
	}
	if strings.Contains(string(down), "CREATE OR REPLACE VIEW") {
		t.Fatal("down migration must not use CREATE OR REPLACE VIEW to remove columns")
	}
}
