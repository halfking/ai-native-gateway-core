package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration847EnsureWiredAndMirrorsSQL guards routing_scope ensure wiring
// (2026-10-08): binary deploys only run the ensure chain; missing ensure
// leaves candidate SQL reading c.routing_scope → 42703 on every chat route.
func TestMigration847EnsureWiredAndMirrorsSQL(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	callIdx := strings.Index(text, "ensureCredentialRoutingScope(migCtx)")
	if callIdx < 0 {
		t.Fatal("db.go must call ensureCredentialRoutingScope in applyMigrationsOnce")
	}
	// Keep adjacent to api_key_auto_profile (704→api_key adjacency is gated
	// elsewhere); 845/846 must still run after 704 on every startup.
	apiKeyIdx := strings.Index(text, "ensureApiKeyAutoProfileIdentity(migCtx)")
	if apiKeyIdx < 0 || apiKeyIdx > callIdx {
		t.Fatal("ensureCredentialRoutingScope must be wired after ensureApiKeyAutoProfileIdentity")
	}

	const sqlConst = "func (d *DB) ensureCredentialRoutingScope("
	fnStart := strings.Index(text, sqlConst)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureCredentialRoutingScope wrapper")
	}
	nextFn := strings.Index(text[fnStart+1:], "func (d *DB) ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureCredentialRoutingScope")
	}
	body := text[fnStart : fnStart+1+nextFn]

	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS routing_scope text NOT NULL DEFAULT 'all'") {
		t.Fatal("ensure SQL must ADD COLUMN IF NOT EXISTS routing_scope")
	}
	if !strings.Contains(body, "VALUES ('847', 'credentials.routing_scope for media/chat credential isolation')") {
		t.Fatal("ensure SQL must stamp schema_migrations version 845")
	}
	if !strings.Contains(body, "credentials_routing_scope_check") {
		t.Fatal("ensure SQL must add credentials_routing_scope_check")
	}

	migBytes, err := os.ReadFile("../sql/migrations/startup/847_credential_routing_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	if !strings.Contains(mig, "ADD COLUMN IF NOT EXISTS routing_scope text NOT NULL DEFAULT 'all'") {
		t.Fatal("migration 847 file altered; keep ensureCredentialRoutingScope mirrored")
	}
	if !strings.Contains(mig, "media_only") || !strings.Contains(mig, "chat_only") {
		t.Fatal("migration 847 must enumerate all|chat_only|media_only")
	}
}
