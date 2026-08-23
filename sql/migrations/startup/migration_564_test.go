package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration564SafeBackfill(t *testing.T) {
	up, err := os.ReadFile("564_session_summary_backfill_safe.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)
	for _, needle := range []string{
		"GREATEST(ss.request_count, a.req_n)",
		"FROM request_logs_hot h",
		"ON CONFLICT (session_key) DO NOTHING",
		"Idempotent: YES",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("564 missing %q", needle)
		}
	}
	if strings.Contains(body, "request_count = EXCLUDED.request_count") {
		t.Error("564 must not REPLACE request_count with EXCLUDED")
	}
}
