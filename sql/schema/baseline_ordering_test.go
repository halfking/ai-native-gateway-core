package schema

// Guards for the fresh-install baseline schema (01-schema.sql).
//
// Background: a fresh install applies 00-prereqs.sql then 01-schema.sql
// statement-by-statement. Objects created with `LANGUAGE sql` have their
// bodies validated at CREATE time, so a definition that appears *after* its
// first eager reference aborts the install. A COMMENT ON FUNCTION emitted
// before the function's CREATE fails the same way.
//
// Round 42 measured this on a real database: the canonical baseline failed
// with 6 such errors. Each was traced to one of four roots, and the fix was to
// relocate the definitions. These tests pin the resulting ordering so the
// failure cannot silently return.
//
// The three copies are generated from the same dump but drift independently,
// so every ordering is asserted for all three.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var copies = []struct {
	name string
	path string
}{
	{"canonical", "01-schema.sql"},
	{"installer-embeddata", "../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql"},
	{"deploy-baseline", "../../deploy/sql/schemas/baseline/01-schema.sql"},
}

var prereqCopies = []struct {
	name string
	path string
}{
	{"canonical", "00-prereqs.sql"},
	{"installer-embeddata", "../../installer/cmd/llm-gw-installer/embeddata/00-prereqs.sql"},
	{"deploy-baseline", "../../deploy/sql/schemas/baseline/00-prereqs.sql"},
}

var headerRe = regexp.MustCompile(`^-- Name: (.+?); Type: (.+?); Schema:`)

// headerLine returns the 1-based line of the pg_dump banner for object name.
func headerLine(t *testing.T, path, name string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for i, l := range strings.Split(string(b), "\n") {
		if m := headerRe.FindStringSubmatch(l); m != nil && m[1] == name {
			return i + 1
		}
	}
	t.Fatalf("%s: object banner %q not found", path, name)
	return 0
}

// TestBaselineDefinitionPrecedesEagerReference pins each definition before the
// object that needs it. The left side of each pair is a dependency that must
// already exist; the right side is the eagerly-validated consumer.
//
// Ordering within a pair is what matters, not absolute position: adding a new
// object to the dump must not require editing these assertions.
func TestBaselineDefinitionPrecedesEagerReference(t *testing.T) {
	orderings := []struct {
		dep, consumer string
		why           string
	}{
		{
			"request_logs_id_seq", "credential_most_used_model(bigint, integer)",
			"the function body does nextval('public.request_logs_id_seq') via request_logs_hot",
		},
		{
			"request_logs_hot", "credential_most_used_model(bigint, integer)",
			"LANGUAGE sql body selects FROM request_logs_hot; validated at CREATE",
		},
		{
			"provider_models", "credential_most_used_model(bigint, integer)",
			"same body joins provider_models; validated at CREATE",
		},
		{
			"model_probe_state", "get_model_state_summary(text)",
			"LANGUAGE sql body reads model_probe_state; validated at CREATE",
		},
		{
			"credentials", "get_model_state_summary(text)",
			"same body reads credentials; validated at CREATE",
		},
		{
			"model_probe_state", "model_probe_credential_concurrency(bigint)",
			"LANGUAGE sql body counts FROM model_probe_state",
		},
		{
			"model_probe_credential_concurrency(bigint)", "v_suspicious_probe_targets",
			"the view body calls the function at CREATE time",
		},
		{
			"system_health_status(integer)",
			"FUNCTION system_health_status(p_window_seconds integer)",
			"COMMENT ON FUNCTION is emitted before the function's own CREATE",
		},
		// The derived copies emit this trio in reverse (drift_report ->
		// healthcheck -> insert_only_parents); canonical is already correct.
		{
			"columnar_insert_only_parents()", "columnar_healthcheck()",
			"healthcheck's body calls columnar_insert_only_parents()",
		},
		{
			"columnar_healthcheck()", "columnar_drift_report()",
			"drift_report's body selects FROM columnar_healthcheck()",
		},
	}

	for _, c := range copies {
		t.Run(c.name, func(t *testing.T) {
			for _, o := range orderings {
				dep := headerLine(t, c.path, o.dep)
				con := headerLine(t, c.path, o.consumer)
				if dep >= con {
					t.Errorf("forward reference: %q defined at L%d but consumed by %q at L%d (%s)",
						o.dep, dep, o.consumer, con, o.why)
				}
			}
		})
	}
}

// TestPrereqsProvideColumnarAccessMethod guards the installer regression found
// in round 42: installer/…/embeddata/00-prereqs.sql was a stale 2026-06-24
// snapshot that never created citus_columnar. Without it the columnar access
// method is not registered in a fresh database, so every
// `SET default_table_access_method = columnar` in 01-schema.sql fails and the
// install aborts.
//
// The three prereqs files are generated from the same dump and must not drift.
func TestPrereqsProvideColumnarAccessMethod(t *testing.T) {
	required := []string{
		"citus", "citus_columnar", // registers the columnar access method
		"vector", "pg_stat_statements", "pgstattuple",
		"btree_gist", "pg_trgm", "pgcrypto", "plpgsql",
	}
	for _, c := range prereqCopies {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(c.path)
			if err != nil {
				t.Fatalf("read %s: %v", c.path, err)
			}
			for _, ext := range required {
				if !strings.Contains(string(b), "CREATE EXTENSION IF NOT EXISTS "+ext+" ") {
					t.Errorf("%s: missing `CREATE EXTENSION IF NOT EXISTS %s`; "+
						"a fresh install would abort on this extension", c.path, ext)
				}
			}
		})
	}
}

// TestPrereqsCopiesAreIdentical keeps the three prereqs files from drifting
// again. They are all emitted from the same production dump.
func TestPrereqsCopiesAreIdentical(t *testing.T) {
	canonical, err := os.ReadFile(prereqCopies[0].path)
	if err != nil {
		t.Fatalf("read canonical prereqs: %v", err)
	}
	for _, c := range prereqCopies[1:] {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		if string(b) != string(canonical) {
			t.Errorf("%s has drifted from %s; regenerate both from the same dump",
				c.path, prereqCopies[0].path)
		}
	}
}
