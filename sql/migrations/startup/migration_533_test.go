package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration533RequestWALBodiesUniqueRequestIDContract(t *testing.T) {
	up, err := os.ReadFile("533_request_wal_bodies_unique_request_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("533_request_wal_bodies_unique_request_id.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"BEGIN;",
		"LOCK TABLE public.request_wal_bodies IN SHARE ROW EXCLUSIVE MODE",
		"HAVING count(*) > 1",
		"RAISE EXCEPTION '533: request_wal_bodies contains",
		"i.indisvalid",
		"i.indisunique",
		"i.indpred IS NULL",
		"i.indnkeyatts = 1",
		"ADD CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)",
		"COMMIT;",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("migration 533 missing %q", required)
		}
	}
	if strings.Contains(upSQL, "DELETE FROM public.request_wal_bodies") {
		t.Error("migration 533 must not silently discard duplicate body records")
	}

	downSQL := string(down)
	if !strings.Contains(downSQL, "intentionally non-destructive") ||
		strings.Contains(downSQL, "DROP CONSTRAINT") {
		t.Error("migration 533 down must preserve the request_id conflict arbiter")
	}
}
