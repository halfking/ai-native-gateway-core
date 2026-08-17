package startup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func migrationFile(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test source path")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(file), name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return body
}

func TestMigration532HandoffHotColumnarContract(t *testing.T) {
	text := string(migrationFile(t, "534_handoff_logs_hot_columnar.sql"))
	for _, required := range []string{
		"pg_advisory_xact_lock",
		"CREATE TABLE IF NOT EXISTS public.handoff_logs_hot",
		"PARTITION BY RANGE (created_at)",
		"USING columnar",
		"ensure_handoff_logs_partition",
		"promote_handoff_logs_hot_to_partition",
		"FOR UPDATE SKIP LOCKED",
		"WITH moved_rows AS",
		"INSERT INTO public.handoff_logs",
		"handoff_logs_with_current_month",
		"handoff_logs_legacy_532",
		"handoff_pending_confirmations",
		"lifecycle.handoff_logs_hot_retention_hours",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("migration 535 missing %q", required)
		}
	}
	if strings.Contains(text, "ON CONFLICT DO NOTHING") {
		t.Error("migration 535 promote must not use ON CONFLICT with a columnar target")
	}
}

func TestMigration534CandidatePromoteIsAtomic(t *testing.T) {
	text := string(migrationFile(t, "535_candidate_failure_logs_atomic_promote.sql"))
	for _, required := range []string{
		"promote_candidate_failure_logs_hot_to_partition",
		"FOR UPDATE SKIP LOCKED",
		"WITH moved_rows AS",
		"INSERT INTO public.candidate_failure_logs",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("migration 535 missing %q", required)
		}
	}
	if strings.Contains(text, "EXCEPTION WHEN OTHERS") || strings.Contains(text, "ON CONFLICT DO") {
		t.Error("migration 535 must atomically fail rather than swallow columnar insert errors")
	}
}
