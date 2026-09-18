package db

import (
	"os"
	"strings"
	"testing"
)

// TestApiKeyAutoProfileIdentityEnsureWired guards the startup repair for the
// sticky-profile table. The table is present in the schema snapshot, but old
// installations lack its api_key_id identity; the ensure must run on every
// normal startup, independently of unrelated schema-repair error branches.
func TestApiKeyAutoProfileIdentityEnsureWired(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	call := "if err := db.ensureApiKeyAutoProfileIdentity(migCtx); err != nil"
	callIdx := strings.Index(text, call)
	if callIdx < 0 {
		t.Fatal("db.go must call ensureApiKeyAutoProfileIdentity in applyMigrationsOnce")
	}
	backoffIdx := strings.Index(text, "if err := db.ensureCredentialPlanQuotaProbeBackoff(migCtx); err != nil")
	if backoffIdx < 0 || backoffIdx > callIdx {
		t.Fatal("ensureApiKeyAutoProfileIdentity must run after ensureCredentialPlanQuotaProbeBackoff")
	}
	const independentCallSequence = `if err := db.ensureCredentialPlanQuotaProbeBackoff(migCtx); err != nil {
		return err
	}
	// api_key_auto_profile was created without the unique identity required by
	// DBProfileStore.Put's ON CONFLICT (api_key_id). Run this independently of
	// unrelated schema self-heals so normal startup repairs existing databases.
	if err := db.ensureApiKeyAutoProfileIdentity(migCtx); err != nil {`
	if !strings.Contains(text, independentCallSequence) {
		t.Fatal("api key profile ensure must be an independent startup step")
	}

	const fnSig = "func (d *DB) ensureApiKeyAutoProfileIdentity("
	fnStart := strings.Index(text, fnSig)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureApiKeyAutoProfileIdentity")
	}
	nextFn := strings.Index(text[fnStart+1:], "\nfunc ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureApiKeyAutoProfileIdentity")
	}
	body := text[fnStart : fnStart+nextFn]
	for _, want := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS api_key_auto_profile_api_key_id_key",
		"ON public.api_key_auto_profile (api_key_id)",
		"SET lock_timeout = '2s'",
		"RESET lock_timeout",
		"conn, err := d.pool.Acquire(ctx)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("api key profile identity ensure missing %q", want)
		}
	}
	if strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Fatal("api key profile identity ensure must not use SET LOCAL outside an explicit transaction")
	}
}

// TestApiKeyAutoProfileSchemaHasIdentity prevents fresh installs from relying
// on the startup repair before ON CONFLICT (api_key_id) can work.
// R43 (2026-09-18): the installer's embedded 01-schema copy is in scope too —
// it drifted from canonical for weeks (missing the PK) and no gate noticed.
func TestApiKeyAutoProfileSchemaHasIdentity(t *testing.T) {
	for _, path := range []string{
		"../sql/objects/tables/api_key_auto_profile.sql",
		"../sql/schema/01-schema.sql",
		"../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "CONSTRAINT api_key_auto_profile_pkey PRIMARY KEY (api_key_id)") {
			t.Fatalf("%s must declare api_key_auto_profile's api_key_id primary key", path)
		}
	}
}
