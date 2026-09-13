package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration703EmbeddedMatchesCanonical guards the installer mirror of the
// supplier_errors promote timezone pin. Both up and down mirrors must stay
// byte-identical to canonical so the rollback asset shipped to db/init cannot
// drift (694/699 precedent).
func TestMigration703EmbeddedMatchesCanonical(t *testing.T) {
	for _, name := range []string{
		"703_supplier_errors_promote_timezone_pin.sql",
		"703_supplier_errors_promote_timezone_pin.down.sql",
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

// TestMigration703Contract pins the shape of the supplier_errors promote
// timezone fix: the SET LOCAL pin must be the first body statement and every
// date_trunc evaluation must come after it (the 698 family contract, applied
// to the V371 deploy-track body), while the D-2#2 atomic CTE semantics and
// the explicit 20-column lists survive untouched.
func TestMigration703Contract(t *testing.T) {
	up, err := os.ReadFile("703_supplier_errors_promote_timezone_pin.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	sql := string(up)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"INSERT INTO public.schema_migrations (version, description)",
		"VALUES ('703', 'Pin promote_supplier_errors month grouping to Asia/Shanghai (V371 track, F13)')",
		"ON CONFLICT (version)",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("703 up migration missing %q", marker)
		}
	}
	if got := strings.Count(sql, "CREATE OR REPLACE FUNCTION public.promote_"); got != 1 {
		t.Errorf("703 up migration defines %d promote functions, want 1", got)
	}

	body := extract703Body(t, sql)

	pinIdx := strings.Index(body, "SET LOCAL TIME ZONE 'Asia/Shanghai';")
	if pinIdx < 0 {
		t.Fatal("703 body missing SET LOCAL TIME ZONE pin")
	}
	beginIdx := strings.Index(body, "BEGIN\n")
	if beginIdx < 0 || pinIdx < beginIdx {
		t.Error("703: SET LOCAL must be the first statement inside the function body")
	}
	if calIdx := strings.Index(body, "date_trunc("); calIdx >= 0 && calIdx < pinIdx {
		t.Error("703: date_trunc evaluated before the timezone pin")
	}
	if calIdx := strings.Index(body, "statement_timestamp()"); calIdx >= 0 && calIdx < pinIdx {
		t.Error("703: statement_timestamp evaluated before the timezone pin")
	}

	// D-2#2 atomic-CTE contract must survive the pin: single-statement
	// SKIP LOCKED -> DELETE RETURNING -> INSERT, explicit 20-column lists
	// (no SELECT * / RETURNING * bitwise-mismatch surface).
	for _, marker := range []string{
		"FOR UPDATE SKIP LOCKED",
		"DELETE FROM supplier_errors_hot h USING batch b",
		"RETURNING h.id, h.occurred_at, h.request_id, h.trace_id, h.tenant_id",
		"INSERT INTO supplier_errors (",
		"h.latency_ms, h.affected_users, h.request_metadata",
		"SELECT count(*) INTO moved FROM inserted",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("703 body missing D-2#2 contract marker %q", marker)
		}
	}
	// 602 precedent: bare "EXCEPTION" would also hit the RAISE guards and
	// the migration's own comments; the swallowed-failure pattern is the
	// EXCEPTION WHEN OTHERS sub-block.
	if strings.Contains(body, "EXCEPTION WHEN OTHERS") {
		t.Error("703 body must not reintroduce the EXCEPTION-swallowing pattern D-2#2 removed")
	}
	// The promote must delegate to the 699-pinned ensure (bounds convention).
	if !strings.Contains(body, "PERFORM ensure_supplier_errors_partition(m)") {
		t.Error("703 body lost the ensure_supplier_errors_partition delegation")
	}
}

// TestMigration703DownRestoresV371Body pins the rollback: CREATE OR REPLACE
// (bare CREATE would 42710 against the up-installed signature), the restored
// body is the V371 original WITHOUT the pin, and the ledger row is removed.
func TestMigration703DownRestoresV371Body(t *testing.T) {
	down, err := os.ReadFile("703_supplier_errors_promote_timezone_pin.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	dsql := string(down)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"CREATE OR REPLACE FUNCTION public.promote_supplier_errors_hot_to_partition(",
		"DELETE FROM public.schema_migrations WHERE version = '703';",
	} {
		if !strings.Contains(dsql, marker) {
			t.Errorf("703 down migration missing %q", marker)
		}
	}
	body := extract703Body(t, dsql)
	if strings.Contains(body, "SET LOCAL TIME ZONE") {
		t.Error("703 down migration must restore the pre-703 V371 body without a timezone pin")
	}
	if strings.Contains(dsql, "\nCREATE FUNCTION public.promote_") {
		t.Error("703 down migration must use CREATE OR REPLACE (bare CREATE aborts rollback with 'function already exists')")
	}
}

func extract703Body(t *testing.T, sql string) string {
	t.Helper()
	bodyStart := strings.Index(sql, "CREATE OR REPLACE FUNCTION public.promote_supplier_errors_hot_to_partition(")
	bodyEnd := strings.Index(sql[bodyStart:], "$$;")
	if bodyStart < 0 || bodyEnd < 0 {
		t.Fatalf("cannot span the 703 function definition")
	}
	return sql[bodyStart : bodyStart+bodyEnd]
}
