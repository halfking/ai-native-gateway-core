package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration692DownRecreatesDependentView(t *testing.T) {
	canonicalPath := "692_session_summaries_user_intent_widen.down.sql"
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical down migration: %v", err)
	}
	embeddedPath := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", canonicalPath)
	embedded, err := os.ReadFile(embeddedPath)
	if err != nil {
		t.Fatalf("read embedded down migration: %v", err)
	}
	if string(canonical) != string(embedded) {
		t.Fatal("canonical and embedded 692 down migrations differ")
	}

	sql := string(canonical)
	for _, marker := range []string{
		"BEGIN;",
		"DROP VIEW IF EXISTS public.v_session_flow;",
		"ALTER TABLE public.session_summaries",
		"ALTER COLUMN user_intent TYPE varchar(50);",
		"CREATE VIEW public.v_session_flow AS",
		"COMMENT ON VIEW public.v_session_flow",
		"DELETE FROM public.schema_migrations WHERE version = '692';",
		"COMMIT;",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("692 down migration missing %q", marker)
		}
	}

	drop := strings.Index(sql, "DROP VIEW IF EXISTS public.v_session_flow;")
	alter := strings.Index(sql, "ALTER TABLE public.session_summaries")
	create := strings.Index(sql, "CREATE VIEW public.v_session_flow AS")
	if drop < 0 || alter < 0 || create < 0 || !(drop < alter && alter < create) {
		t.Fatalf("692 down migration must order DROP VIEW → ALTER COLUMN → CREATE VIEW (drop=%d alter=%d create=%d)", drop, alter, create)
	}
	if !strings.Contains(sql, "PARTITION BY s.tenant_id, s.gw_project_id") {
		t.Fatal("692 down migration must preserve the project partition in v_session_flow")
	}
}
