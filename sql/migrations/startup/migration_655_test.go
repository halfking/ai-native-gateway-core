package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration655AutoRouteSelectionsHotContract(t *testing.T) {
	upPath := filepath.Join("655_auto_route_selections_hot.sql")
	downPath := filepath.Join("655_auto_route_selections_hot.down.sql")
	upBytes, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	down := string(downBytes)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.auto_route_selections_hot",
		"CREATE OR REPLACE VIEW public.auto_route_selections_all AS",
		"UNION ALL",
		"CREATE OR REPLACE FUNCTION public.ensure_auto_route_selections_partition",
		"CREATE OR REPLACE FUNCTION public.promote_auto_route_selections_hot_to_partition",
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM public.auto_route_selections_hot",
		"RETURNING h.id",
		"INSERT INTO public.auto_route_selections (",
		"DEFAULT interval '8 hours'",
		"ts TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		"partition_date DATE NOT NULL DEFAULT CURRENT_DATE",
		"pg_advisory_xact_lock",
		"DETACH PARTITION public.auto_route_selections_default",
		"ATTACH PARTITION public.auto_route_selections_default DEFAULT",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 655 missing %q", want)
		}
	}
	if strings.Contains(strings.ToUpper(up), "SELECT *") {
		t.Error("migration 655 must use explicit columns, not SELECT *")
	}
	if strings.Contains(strings.ToUpper(up), "EXCEPTION WHEN") {
		t.Error("migration 655 promote must propagate errors")
	}
	copyPos := strings.Index(down, "INSERT INTO public.auto_route_selections (")
	dropPos := strings.Index(down, "DROP TABLE IF EXISTS public.auto_route_selections_hot")
	if copyPos < 0 || dropPos < 0 || copyPos >= dropPos {
		t.Error("down migration must copy hot rows to parent before dropping hot table")
	}
	if strings.Contains(strings.ToUpper(down), "SELECT *") {
		t.Error("down migration must use explicit columns")
	}
}
