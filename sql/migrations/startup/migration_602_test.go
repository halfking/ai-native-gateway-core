package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// stripSQLComments removes -- comments so pattern assertions target real SQL,
// not the incident narrative quoted in the migration header.
func stripSQLComments(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// migration602Columns extracts the three explicit column lists (RETURNING,
// INSERT target, final SELECT) from the 602 function body and returns them so
// positional drift between the lists fails loudly in CI instead of silently
// rerouting columns at promote time (the 2026-08-25 incident class).
func migration602Columns(t *testing.T, body string) [3][]string {
	t.Helper()
	re := regexp.MustCompile(`(?s)RETURNING\n\s+(.*?)\n\s*\)\s*\n\s*INSERT INTO public\.request_logs \(\s*\n(.*?)\n\s*\)\s*\n\s*SELECT\n(.*?)\n\s*FROM moved_rows`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("could not locate RETURNING / INSERT / SELECT column lists in migration 602")
	}
	var out [3][]string
	for i, raw := range m[1:] {
		cols := []string{}
		for _, line := range strings.Split(raw, "\n") {
			for _, c := range strings.Split(line, ",") {
				if c = strings.TrimSpace(c); c != "" {
					cols = append(cols, c)
				}
			}
		}
		out[i] = cols
	}
	return out
}

func TestMigration602AtomicPromote(t *testing.T) {
	migration, err := os.ReadFile("602_request_logs_promote_atomic.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := stripSQLComments(string(migration))

	for _, required := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition",
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM public.request_logs_hot",
		"RETURNING",
		"INSERT INTO public.request_logs",
		"COMMIT;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration missing %q", required)
		}
	}

	// The incident root cause: delete-before-insert with an exception
	// sub-block that swallowed INSERT failures while the DELETE committed.
	// Neither pattern may come back.
	if strings.Contains(body, "EXCEPTION WHEN OTHERS") {
		t.Fatalf("migration must not swallow INSERT failures (EXCEPTION WHEN OTHERS reintroduces the 2026-08-25 data-loss window)")
	}
	if strings.Contains(body, "SELECT * FROM _promote_hot_batch") {
		t.Fatalf("migration must not use positional SELECT * promote (column drift between request_logs_hot and request_logs reroutes columns)")
	}

	// DELETE and INSERT must live in the same data-modifying CTE statement so
	// a failure rolls back the batch atomically.
	if !strings.Contains(body, "moved_rows AS (") {
		t.Fatalf("migration must use the atomic moved_rows CTE (batch → DELETE RETURNING → INSERT)")
	}
}

func TestMigration602ColumnListsAligned(t *testing.T) {
	migration, err := os.ReadFile("602_request_logs_promote_atomic.sql")
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
	if len(returning) < 100 {
		t.Fatalf("expected the full shared column list (135 cols), got %d", len(returning))
	}

	for _, must := range []string{
		// Columns the incident repro proved were positionally misrouted.
		"customer_id", "client_model", "search_text",
		// Columns actively written by the gateway INSERT path.
		"agent_name", "canonical_model", "request_type", "token_band", "discard_events",
		// Key columns.
		"id", "request_id", "ts", "tenant_id",
	} {
		found := false
		for _, c := range returning {
			if c == must {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("promote column list missing %q", must)
		}
	}

	// Hot-only columns must never appear (they don't exist on the parent and
	// would abort the INSERT). The five type-divergent legacy columns are
	// intentionally excluded — see the migration comment.
	for _, banned := range []string{
		"caller_id", "session_correlation_id", "status_code",
		"protocol_conversion", "ir_extensions", "sanitizer_mutations",
		"content_safety_score", "dlp_violations",
	} {
		for _, c := range returning {
			if c == banned {
				t.Fatalf("promote column list must not contain %q", banned)
			}
		}
	}
}

func TestMigration602DownRestoresPreviousBody(t *testing.T) {
	down, err := os.ReadFile("602_request_logs_promote_atomic.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	body := string(down)
	if !strings.Contains(body, "CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition") {
		t.Fatalf("down migration missing function restore")
	}
}
