package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration625BuildsExplicitSessionBodiesUnified(t *testing.T) {
	data, err := os.ReadFile("625_session_bodies_unified_explicit.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"security_invoker = true",
		"FROM public.session_bodies_hot",
		"FROM public.session_bodies",
		"partition_date <= CURRENT_DATE - INTERVAL '1 day'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("migration 625 missing %q", want)
		}
	}
}

func TestMigration625DownDropsOnlyUnifiedView(t *testing.T) {
	data, err := os.ReadFile("625_session_bodies_unified_explicit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "DROP VIEW IF EXISTS public.session_bodies_unified") {
		t.Fatal("migration 625 down must drop session_bodies_unified")
	}
	if strings.Contains(s, "DROP TABLE") || strings.Contains(s, "session_bodies_hot") {
		t.Fatal("migration 625 down must not touch unrelated tables")
	}
}
