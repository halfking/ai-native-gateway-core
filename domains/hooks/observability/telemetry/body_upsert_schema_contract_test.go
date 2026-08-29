package telemetry

import (
	"os"
	"strings"
	"testing"
)

func TestUpsertRequestLogBodiesMatchesBodyTableSchema(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (c *Client) upsertRequestLogBodies")
	if start < 0 {
		t.Fatal("upsertRequestLogBodies not found")
	}
	end := strings.Index(body[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatal("upsertRequestLogBodies end not found")
	}
	body = body[start : start+1+end]

	for _, forbidden := range []string{"tenant_id", "rl.tenant_id", "EXCLUDED.tenant_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body upsert references removed column %q", forbidden)
		}
	}
	for _, required := range []string{
		"INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body, outbound_body)",
		"SELECT $1, rl.ts,",
		"ON CONFLICT (request_id) DO UPDATE",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("body upsert missing schema contract %q", required)
		}
	}
}
