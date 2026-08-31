package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionV2MigrationContract captures the source-level requirements that must
// be satisfied before a Session V2 migration can be applied or read-path cutover
// can be considered.
type sessionV2MigrationContract struct {
	bodyMigration    string
	promoteMigration string
	turnsMigration   string
	turnsDown        string
	schemaMigration  string
	runnerSource     string
	embedSource      string
}

func TestSessionV2MigrationContractDetector(t *testing.T) {
	// Default CI tests the detector against a synthetic compliant contract. The
	// real-source gate is intentionally opt-in because the repository is known
	// to be No-Go until an additive migration and bootstrap registration land.
	compliant := sessionV2MigrationContract{
		bodyMigration:    "ENABLE ROW LEVEL SECURITY; security_invoker=true; session_bodies_with_current_month; UNIQUE (tenant_id, request_id, partition_date);",
		promoteMigration: "pg_advisory_xact_lock; FOR UPDATE SKIP LOCKED; DELETE ... RETURNING; INSERT INTO;",
		turnsMigration:   "DROP VIEW IF EXISTS public.session_turns_unified",
		turnsDown:        "CREATE VIEW public.session_turns_unified WITH (security_invoker=true)",
		runnerSource:     "614_session_bodies_hot.sql 615_session_bodies_hot_promote_function.sql 635_drop_session_turns_unified.sql",
		embedSource:      "614_session_bodies_hot.sql 615_session_bodies_hot_promote_function.sql 635_drop_session_turns_unified.sql",
	}
	if violations := detectSessionV2MigrationViolations(compliant); len(violations) != 0 {
		t.Fatalf("compliant migration contract unexpectedly rejected: %v", violations)
	}
}

func TestSessionV2MigrationContractHasKnownNoGoEvidence(t *testing.T) {
	contract := loadSessionV2MigrationContract(t)
	violations := detectSessionV2MigrationViolations(contract)
	for _, violation := range violations {
		t.Logf("NO-GO: %s", violation)
	}
	if len(violations) != 0 {
		t.Fatalf("expected current source to have no Session V2 migration contract violations; got %d", len(violations))
	}
}

func detectSessionV2MigrationViolations(c sessionV2MigrationContract) []string {
	body := strings.ToLower(c.bodyMigration)
	promote := strings.ToLower(c.promoteMigration)
	turns := strings.ToLower(c.turnsMigration)
	var violations []string

	for _, want := range []string{
		"enable row level security",
		"security_invoker",
		"session_bodies_with_current_month",
		"unique (tenant_id, request_id, partition_date)",
	} {
		if !strings.Contains(body, want) {
			violations = append(violations, "614 body migration missing "+want)
		}
	}
	if strings.Contains(body, "select *") {
		violations = append(violations, "614 body view uses SELECT *")
	}
	for _, want := range []string{
		"pg_advisory_xact_lock",
		"for update skip locked",
		"returning",
	} {
		if !strings.Contains(promote, want) {
			violations = append(violations, "615 promote migration missing "+want)
		}
	}
	// 635 is the cleanup migration that drops the orphan session_turns_unified
	// view (previously created by 619). The contract now requires the up
	// migration to DROP VIEW IF EXISTS session_turns_unified and the down to
	// recreate it WITH (security_invoker=true).
	if !strings.Contains(turns, "DROP VIEW IF EXISTS public.session_turns_unified") &&
		!strings.Contains(turns, "drop view if exists public.session_turns_unified") {
		violations = append(violations, "635 turns cleanup must DROP VIEW IF EXISTS public.session_turns_unified")
	}
	if !strings.Contains(strings.ToLower(c.turnsDown), "session_turns_unified") {
		violations = append(violations, "635 turns down must recreate session_turns_unified view")
	}
	if strings.Contains(strings.ToLower(c.turnsDown), "session_turns_unified") &&
		!strings.Contains(strings.ToLower(c.turnsDown), "security_invoker") {
		violations = append(violations, "635 turns down must recreate view WITH (security_invoker=true)")
	}
	if strings.Contains(strings.ToLower(c.schemaMigration), "schemaname = 'gateway'") {
		violations = append(violations, "430 schema migration checks gateway instead of public")
	}
	for _, name := range []string{
		"614_session_bodies_hot.sql",
		"615_session_bodies_hot_promote_function.sql",
		"635_drop_session_turns_unified.sql",
	} {
		if !strings.Contains(c.runnerSource, name) {
			violations = append(violations, "startup runner does not register "+name)
		}
		if !strings.Contains(c.embedSource, name) {
			violations = append(violations, "installer embeddata does not contain "+name)
		}
	}
	return violations
}

func loadSessionV2MigrationContract(t *testing.T) sessionV2MigrationContract {
	t.Helper()
	return sessionV2MigrationContract{
		bodyMigration:    readMigrationContract(t, "614_session_bodies_hot.sql"),
		promoteMigration: readMigrationContract(t, "615_session_bodies_hot_promote_function.sql"),
		turnsMigration:   readMigrationContract(t, "635_drop_session_turns_unified.sql"),
		turnsDown:        readMigrationContract(t, "635_drop_session_turns_unified.down.sql"),
		schemaMigration:  readMigrationContract(t, "430_sessions_v2_schema.sql"),
		runnerSource:     readSourceContract(t, "../../../installer/internal/dbinit/runner.go"),
		embedSource:      readEmbeddedStartupNames(t),
	}
}

func readMigrationContract(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func readSourceContract(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func readEmbeddedStartupNames(t *testing.T) string {
	t.Helper()
	root := "../../../installer/cmd/llm-gw-installer/embeddata/startup"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read embedded startup directory: %v", err)
	}
	var names strings.Builder
	for _, entry := range entries {
		names.WriteString(entry.Name())
		names.WriteByte('\n')
	}
	return names.String()
}
