package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration693EmbeddedMatchesCanonical guards the installer mirror of
// the provider_models.canonical_cleared_at migration (migration 693, admin
// unbind marker). The canonical file lives here; the installer embeds its
// own copy so fresh one-click installs get the column before the gateway
// binary — whose discovery upsert SQL now references canonical_cleared_at —
// ever boots against the new schema.
func TestMigration693EmbeddedMatchesCanonical(t *testing.T) {
	for _, name := range []string{
		"693_provider_models_canonical_cleared_at.sql",
		"693_provider_models_canonical_cleared_at.down.sql",
	} {
		canonical, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read canonical %s: %v", name, err)
		}
		embedded, err := os.ReadFile(filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if string(canonical) != string(embedded) {
			t.Errorf("canonical and embedded %s differ", name)
		}
	}
}

func TestMigration693Contract(t *testing.T) {
	up, err := os.ReadFile("693_provider_models_canonical_cleared_at.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	sql := string(up)
	for _, marker := range []string{
		"BEGIN;",
		// Idempotency guard — the migration must be re-runnable.
		"IF NOT EXISTS (",
		"information_schema.columns",
		"table_name = 'provider_models'",
		"column_name = 'canonical_cleared_at'",
		// Nullable timestamptz: NULL = never unbound; non-NULL = admin
		// unbind timestamp. Never NOT NULL, never a DEFAULT.
		"ADD COLUMN canonical_cleared_at timestamp with time zone;",
		"COMMIT;",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("693 up migration missing %q", marker)
		}
	}
	if strings.Contains(sql, "NOT NULL DEFAULT") {
		t.Error("693 up migration must add a nullable marker column without a default")
	}

	down, err := os.ReadFile("693_provider_models_canonical_cleared_at.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	dsql := string(down)
	for _, marker := range []string{
		"BEGIN;",
		"ALTER TABLE public.provider_models DROP COLUMN IF EXISTS canonical_cleared_at;",
		"DELETE FROM public.schema_migrations WHERE version = '693';",
		"COMMIT;",
	} {
		if !strings.Contains(dsql, marker) {
			t.Errorf("693 down migration missing %q", marker)
		}
	}
}
