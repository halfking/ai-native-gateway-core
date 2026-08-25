package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration576CustomerIDAlignsHotAndParent(t *testing.T) {
	up, err := os.ReadFile("576_request_logs_customer_id_bigint.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("576_request_logs_customer_id_bigint.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for _, sql := range []string{string(up), string(down)} {
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Fatal("customer_id migration must be transactional")
		}
	}
	if !strings.Contains(string(up), "ALTER COLUMN customer_id TYPE bigint") {
		t.Fatal("up migration must make hot customer_id BIGINT")
	}
	if !strings.Contains(string(up), "NULLIF(BTRIM(customer_id), '')::bigint") {
		t.Fatal("up migration must handle nullable/blank historical values")
	}
}
