package admin

import (
	"os"
	"strings"
	"testing"
)

func TestStatsUIBCContract(t *testing.T) {
	tenants := mustReadSource(t, "tenants.go")
	if !strings.Contains(tenants, "cache_read_tokens") || !strings.Contains(tenants, "COALESCE(AVG(latency_ms), 0)") {
		t.Fatal("tenant stats credits query must project token split and average latency")
	}
	logs := mustReadSource(t, "logs.go")
	if !strings.Contains(logs, `addFilter("rl.api_key_owner_user = $%d", v)`) {
		t.Fatal("request logs must filter owner_user on the request row")
	}
	if strings.Contains(logs, "ak.owner_user = $%d") {
		t.Fatal("owner filter must not require the api_keys join")
	}
	usage := mustReadSource(t, "user_usage_stats.go")
	if !strings.Contains(usage, "SELECT COUNT(*) FROM api_keys WHERE owner_user = $1 AND tenant_id = $2") {
		t.Fatal("user stats must count api keys for the drawer")
	}
}

func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
