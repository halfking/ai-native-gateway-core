package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration625KeepsHistoricalViewDefinitionForUpgradeCompatibility(t *testing.T) {
	data, err := os.ReadFile("614_session_bodies_hot.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"SELECT",
		"FROM public.session_bodies",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("migration 614 missing historical view contract %q", want)
		}
	}
	if strings.Contains(s, "security_invoker = true") {
		t.Fatal("migration 614 must remain the original SELECT-* view; migration 625 applies security_invoker")
	}
}

func TestMigration625BuildsExplicitSessionBodiesUnified(t *testing.T) {
	data, err := os.ReadFile("625_session_bodies_unified_explicit.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"ALTER VIEW public.session_bodies_unified SET (security_invoker = true)",
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

func TestMigration625DownRecreatesSelectStarView(t *testing.T) {
	data, err := os.ReadFile("625_session_bodies_unified_explicit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"FROM public.session_bodies_hot",
		"FROM public.session_bodies",
		"ALTER VIEW public.session_bodies_unified RESET (security_invoker)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("migration 625 down missing %q", want)
		}
	}
	if strings.Contains(s, "DROP VIEW") {
		t.Fatal("migration 625 down must NOT drop the view — five Go readers depend on it")
	}
	if strings.Contains(s, "DROP TABLE") || strings.Contains(s, "session_bodies_hot") && strings.Contains(s, "DROP TABLE session_bodies_hot") {
		t.Fatal("migration 625 down must not touch unrelated tables")
	}
}
