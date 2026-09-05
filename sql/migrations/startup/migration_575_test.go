package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration575AppendsCustomerIDToRequestLogView(t *testing.T) {
	up, err := os.ReadFile("575_request_logs_view_customer_id.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("575_request_logs_view_customer_id.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for _, sql := range []string{string(up), string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Fatal("view migration must be transactional")
		}
	}
	for _, needle := range []string{
		"RENAME TO request_logs_with_current_month_without_customer_id",
		"CREATE VIEW public.request_logs_with_current_month",
		"customer_id",
		"request_logs_hot",
		"request_logs",
	} {
		if !strings.Contains(string(up), needle) {
			t.Errorf("migration missing %q", needle)
		}
	}
}
