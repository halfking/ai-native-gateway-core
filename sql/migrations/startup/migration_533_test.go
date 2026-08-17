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
		"DELETE FROM public.request_wal_bodies body",
		"row_number() OVER",
		"PARTITION BY request_id",
		"ORDER BY created_at DESC, ctid DESC",
		"GET DIAGNOSTICS duplicate_rows = ROW_COUNT",
		"RAISE NOTICE '533: removed % duplicate request_wal_bodies rows",
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
	if strings.Contains(upSQL, "RAISE EXCEPTION '533: request_wal_bodies contains") {
		t.Error("migration 533 must reconcile duplicate body records instead of blocking deployment")
	}

	downSQL := string(down)
	if !strings.Contains(downSQL, "intentionally non-destructive") ||
		strings.Contains(downSQL, "DROP CONSTRAINT") {
		t.Error("migration 533 down must preserve the request_id conflict arbiter")
	}
}
