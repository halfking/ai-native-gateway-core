package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration699EmbeddedMatchesCanonical guards the installer mirror of the
// supplier_errors ensure timezone pin. Both up and down mirrors must stay
// byte-identical to canonical so the rollback asset shipped to db/init cannot
// drift (694 precedent).
func TestMigration699EmbeddedMatchesCanonical(t *testing.T) {
	for _, name := range []string{
		"699_supplier_errors_ensure_timezone_pin.sql",
		"699_supplier_errors_ensure_timezone_pin.down.sql",
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

// TestMigration699Contract pins the shape of the supplier_errors ensure
// timezone fix: the SET LOCAL pin must be the first body statement (the
// pre-699 DECLARE initializers evaluated before any in-body SET LOCAL could
// take effect — the 694 trap), while the V359 columnar semantics survive.
func TestMigration699Contract(t *testing.T) {
	up, err := os.ReadFile("699_supplier_errors_ensure_timezone_pin.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	sql := string(up)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"INSERT INTO public.schema_migrations (version, description)",
		"VALUES ('699', 'Pin ensure_supplier_errors_partition to Asia/Shanghai session timezone')",
		"ON CONFLICT (version)",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("699 up migration missing %q", marker)
		}
	}
	if got := strings.Count(sql, "CREATE OR REPLACE FUNCTION public.ensure_"); got != 1 {
		t.Errorf("699 up migration defines %d ensure functions, want 1", got)
	}

	bodyStart := strings.Index(sql, "CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition")
	bodyEnd := strings.Index(sql, "$$;")
	if bodyStart < 0 || bodyEnd < 0 || bodyEnd < bodyStart {
		t.Fatalf("cannot span the 699 function definition")
	}
	body := sql[bodyStart:bodyEnd]

	if strings.Contains(body, "date_trunc('month', target_ts)::date;") &&
		!strings.Contains(body, "month_start    date;") {
		// DECLARE initializers must be gone: they evaluate before BEGIN.
		t.Error("699 body still carries DECLARE-time initializers")
	}
	pinIdx := strings.Index(body, "SET LOCAL TIME ZONE 'Asia/Shanghai';")
	if pinIdx < 0 {
		t.Fatal("699 body missing SET LOCAL TIME ZONE pin")
	}
	beginIdx := strings.Index(body, "BEGIN\n")
	if beginIdx < 0 || pinIdx < beginIdx {
		t.Error("699: SET LOCAL must be the first statement inside the function body")
	}
	if calIdx := strings.Index(body, "date_trunc("); calIdx >= 0 && calIdx < pinIdx {
		t.Error("699: date_trunc evaluated before the timezone pin")
	}

	// V359 columnar semantics must survive the pin: supplier_errors is a
	// read-only tiered store (562's heap decision does not apply).
	for _, marker := range []string{
		"FOR VALUES FROM (%L) TO (%L) USING columnar",
		"PERFORM enforce_columnar_partition(partition_name, 'supplier_errors')",
		"RETURNS text",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("699 body missing V359 columnar contract marker %q", marker)
		}
	}
}

// TestMigration699DownExecutableAfterUp pins the 42710 rollback fix: the
// restored pre-699 definition must use CREATE OR REPLACE so down runs against
// a database where up already installed that exact signature, and the ledger
// row must be removed.
func TestMigration699DownExecutableAfterUp(t *testing.T) {
	down, err := os.ReadFile("699_supplier_errors_ensure_timezone_pin.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	dsql := string(down)
	for _, marker := range []string{
		"BEGIN;",
		"COMMIT;",
		"CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition(target_ts timestamp with time zone)",
		"DELETE FROM public.schema_migrations WHERE version = '699';",
	} {
		if !strings.Contains(dsql, marker) {
			t.Errorf("699 down migration missing %q", marker)
		}
	}
	if strings.Contains(dsql, "SET LOCAL TIME ZONE") {
		t.Error("699 down migration must restore the pre-699 body without a timezone pin")
	}
	if strings.Contains(dsql, "\nCREATE FUNCTION public.ensure_") {
		t.Error("699 down migration must use CREATE OR REPLACE (bare CREATE aborts rollback with 'function already exists')")
	}
}
