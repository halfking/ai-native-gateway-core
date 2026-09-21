package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migration694EnsureFunctions lists the 13 partition ensure functions that
// migration 694 replaces with Asia/Shanghai-pinned bodies. The order mirrors
// the up migration so positional slices stay meaningful.
var migration694EnsureFunctions = []string{
	"ensure_candidate_failure_logs_partition",
	"ensure_credential_model_index_partition",
	"ensure_request_logs_bodies_partition",
	"ensure_request_logs_partition",
	"ensure_request_wal_partition",
	"ensure_routing_decision_log_partition",
	"ensure_usage_ledger_partition",
	"ensure_next_month_archive_partition",
	"ensure_next_month_cmi_archive_partition",
	"ensure_next_month_request_wal_partition",
	"ensure_next_month_routing_archive_partition",
	"ensure_tool_usage_stats_partition",
	"ensure_credit_ledger_partition",
}

// TestMigration694EmbeddedMatchesCanonical guards the installer mirror of the
// partition ensure timezone migration. The installer executes only the up
// file, but both up/down mirrors must stay byte-identical to canonical so the
// rollback asset shipped to db/init cannot drift.
func TestMigration694EmbeddedMatchesCanonical(t *testing.T) {
	for _, name := range []string{
		"694_partition_ensure_timezone.sql",
		"694_partition_ensure_timezone.down.sql",
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

// splitFunctionBodies slices sql into per-function chunks keyed by the CREATE
// header anchor. Each chunk spans from its anchor to the next anchor (or EOF),
// which is enough for containment assertions without a PL/pgSQL parser.
func splitFunctionBodies(t *testing.T, sql string) map[string]string {
	t.Helper()
	bodies := make(map[string]string, len(migration694EnsureFunctions))
	for i, fn := range migration694EnsureFunctions {
		anchor := "CREATE OR REPLACE FUNCTION public." + fn
		start := strings.Index(sql, anchor)
		if start < 0 {
			t.Errorf("missing function definition for %s", fn)
			continue
		}
		end := len(sql)
		if i+1 < len(migration694EnsureFunctions) {
			if next := strings.Index(sql, "CREATE OR REPLACE FUNCTION public."+migration694EnsureFunctions[i+1]); next > start {
				end = next
			}
		}
		bodies[fn] = sql[start:end]
	}
	return bodies
}

func TestMigration694Contract(t *testing.T) {
	up, err := os.ReadFile("694_partition_ensure_timezone.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	sql := string(up)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		// Idempotent ledger bookkeeping (694 records itself).
		"INSERT INTO public.schema_migrations (version, description)",
		"VALUES ('694', 'Fix partition ensure functions to use Asia/Shanghai session timezone')",
		"ON CONFLICT (version)",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("694 up migration missing %q", marker)
		}
	}
	if got := strings.Count(sql, "CREATE OR REPLACE FUNCTION public.ensure_"); got != 13 {
		t.Errorf("694 up migration has %d CREATE OR REPLACE FUNCTION ensure_ definitions, want 13", got)
	}
	if got := strings.Count(sql, "SET LOCAL TIME ZONE 'Asia/Shanghai';"); got != 13 {
		t.Errorf("694 up migration has %d SET LOCAL TIME ZONE statements, want 13 (one per function)", got)
	}

	// Per-function: the timezone pin must precede the first calendar
	// derivation (date_trunc / now()), and no function may keep its
	// pre-694 DECLARE-time initializers — those evaluate before any
	// in-body SET LOCAL could take effect.
	bodies := splitFunctionBodies(t, sql)
	for _, fn := range migration694EnsureFunctions {
		body, ok := bodies[fn]
		if !ok {
			continue
		}
		setIdx := strings.Index(body, "SET LOCAL TIME ZONE 'Asia/Shanghai';")
		if setIdx < 0 {
			t.Errorf("%s: missing SET LOCAL TIME ZONE pin", fn)
			continue
		}
		bodyStart := strings.Index(body, "BEGIN\n")
		if bodyStart < 0 || setIdx < bodyStart {
			t.Errorf("%s: SET LOCAL must be the first statement inside the function body", fn)
		}
		if calIdx := strings.Index(body, "date_trunc("); calIdx >= 0 && calIdx < setIdx {
			t.Errorf("%s: date_trunc evaluated before the timezone pin", fn)
		}
		for _, stale := range []string{
			"month_start    date := date_trunc(",
			"month_start date := date_trunc(",
			"next_month_start date := date_trunc(",
			"next_month_start   date := date_trunc(",
		} {
			if strings.Contains(body, stale) {
				t.Errorf("%s: declaration initializer still computes the month before the timezone pin: %q", fn, stale)
			}
		}
	}

	// 689 contract must survive: candidate partitions stay heap. Anchor on
	// executable shapes — the historical 689 comments inside the body
	// legitimately mention "columnar" and enforce_columnar_partition, so a
	// bare substring match would false-positive on prose.
	if body := bodies["ensure_candidate_failure_logs_partition"]; body != "" {
		if strings.Contains(body, "TO (%L) USING columnar") {
			t.Error("candidate_failure_logs ensure must keep 689 heap semantics (EXECUTE creates USING columnar)")
		}
		if strings.Contains(body, "PERFORM enforce_columnar_partition") {
			t.Error("candidate_failure_logs ensure must keep 689 heap semantics (ELSE branch enforces columnar)")
		}
		if !strings.Contains(body, "RETURNS text") {
			t.Error("candidate_failure_logs ensure must keep RETURNS text (689 signature)")
		}
	}
}

// TestMigration694DownExecutableAfterUp pins the 42710 rollback fix: every
// restored definition must use CREATE OR REPLACE so down runs against a
// database where up already installed those exact signatures. A bare CREATE
// FUNCTION aborts the rollback transaction on the first definition, leaving
// the version-694 ledger row behind.
func TestMigration694DownExecutableAfterUp(t *testing.T) {
	down, err := os.ReadFile("694_partition_ensure_timezone.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	dsql := string(down)

	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"DELETE FROM public.schema_migrations WHERE version = '694';",
	} {
		if !strings.Contains(dsql, marker) {
			t.Errorf("694 down migration missing %q", marker)
		}
	}
	if got := strings.Count(dsql, "CREATE OR REPLACE FUNCTION public.ensure_"); got != 13 {
		t.Errorf("694 down migration has %d CREATE OR REPLACE definitions, want 13", got)
	}
	// Bare CREATE FUNCTION against an existing signature fails the whole
	// rollback — forbid it outright.
	if idx := strings.Index(dsql, "\nCREATE FUNCTION public.ensure_"); idx >= 0 {
		t.Errorf("694 down migration contains a bare CREATE FUNCTION at offset %d; rollback would abort with 'function already exists'", idx)
	}

	// Rollback intentionally restores the pre-694 session-timezone-dependent
	// semantics: no SET LOCAL pin anywhere in the down file.
	if strings.Contains(dsql, "SET LOCAL TIME ZONE") {
		t.Error("694 down migration must restore pre-694 bodies without a timezone pin")
	}
}
