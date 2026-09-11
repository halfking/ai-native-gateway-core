package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Migration 697 wires the system_fingerprint write path through promote:
// the X-System-Fingerprint response header is captured by the integrity
// detector but was only ever persisted to the detector's context JSONB —
// the dedicated request_logs_hot.system_fingerprint column (migration 603)
// stayed NULL forever and the 7-day drift scanner ran on an empty set since
// 2026-07-28. With the telemetry write path now stamping the column, the
// promote function must carry it into the monthly partitions or promoted
// rows would silently drop it (parent default NULL) and the drift window
// would go blind for every row older than the 8h hot retention.
//
// The function body is the 695 self-heal body with system_fingerprint
// appended to all three explicit column lists (RETURNING / INSERT / SELECT —
// the 2026-08-25 positional-drift incident class).
func TestMigration697PromoteSystemFingerprint(t *testing.T) {
	migration, err := os.ReadFile("697_request_logs_promote_system_fingerprint.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := stripSQLComments(string(migration))

	for _, required := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition",
		// 688 alignment: SQL DEFAULT must keep matching the Go scheduler (8h).
		"DEFAULT '8 hours'::interval",
		// 695 self-heal demote must survive the re-install.
		"SET is_final_success = FALSE",
		"am.amname = 'heap'",
		// Atomic CTE semantics from 602 must survive the re-install.
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM public.request_logs_hot",
		"moved_rows AS (",
		"INSERT INTO public.request_logs",
		"COMMIT;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 697 missing %q", required)
		}
	}

	// The whole point of 697: the fingerprint column must ride all three
	// column lists. Positional drift is checked by the shared alignment test
	// below via migration602Columns.
	listTails := regexp.MustCompile(`token_band,\s*system_fingerprint\b`).FindAllString(body, -1)
	if len(listTails) != 3 {
		t.Fatalf("migration 697 must append system_fingerprint to all three column "+
			"lists (RETURNING/INSERT/SELECT), found %d list tails", len(listTails))
	}

	// The upgrade channel must carry 697 (693-class gap).
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("read apply-db-revision-sequence.sh: %v", err)
	}
	if !strings.Contains(string(seq), "697_request_logs_promote_system_fingerprint.sql") {
		t.Fatalf("apply-db-revision-sequence.sh does not carry migration 697 — " +
			"upgrade-database deployments would never apply it")
	}

	// Installer embeddata must ship the migration for fresh installs. 695
	// and 696 were found missing there (pre-existing embeddata drift) — 697
	// must not repeat the gap, and the two siblings ride along.
	for _, name := range []string{
		"695_request_logs_promote_final_success_self_heal.sql",
		"696_request_logs_view_system_fingerprint.sql",
		"697_request_logs_promote_system_fingerprint.sql",
	} {
		if _, err := os.Stat("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + name); err != nil {
			t.Fatalf("installer embeddata/startup missing %s (fresh installs would never apply it): %v", name, err)
		}
	}
}

// The three explicit column lists must stay positionally identical after the
// 697 re-install (2026-08-25 positional-drift incident class).
func TestMigration697ColumnListsAligned(t *testing.T) {
	migration, err := os.ReadFile("697_request_logs_promote_system_fingerprint.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	lists := migration602Columns(t, stripSQLComments(string(migration)))
	returning, insertCols, selectCols := lists[0], lists[1], lists[2]

	if len(returning) != len(insertCols) || len(insertCols) != len(selectCols) {
		t.Fatalf("column list length drift: returning=%d insert=%d select=%d", len(returning), len(insertCols), len(selectCols))
	}
	for i := range returning {
		if returning[i] != insertCols[i] || insertCols[i] != selectCols[i] {
			t.Fatalf("column %d misaligned: returning=%q insert=%q select=%q", i, returning[i], insertCols[i], selectCols[i])
		}
	}

	// The 697 column is the LAST entry of each list.
	last := returning[len(returning)-1]
	if last != "system_fingerprint" {
		t.Fatalf("last promote column = %q, want system_fingerprint", last)
	}
}
