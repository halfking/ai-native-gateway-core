package startup

import (
	"strings"
	"testing"
)

func TestMigration624CandidatePromoteKeepsDeleteAndInsertAtomic(t *testing.T) {
	text := string(migrationFile(t, "624_candidate_failure_logs_promote_atomic_v2.sql"))
	for _, required := range []string{
		"promote_candidate_failure_logs_hot_to_partition",
		"source_ctid",
		"WITH moved_rows AS",
		"INSERT INTO public.candidate_failure_logs",
		"per_attempt_latency_ms",
		"session_id",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("migration 624 missing %q", required)
		}
	}
	if strings.Contains(text, "SELECT * FROM candidate_failure_logs_hot") || strings.Contains(text, "EXCEPTION WHEN OTHERS") {
		t.Fatal("migration 624 must use explicit columns and let insert errors roll back the hot delete")
	}
}

func TestMigration624DownDoesNotRestoreUnsafePromotion(t *testing.T) {
	text := string(migrationFile(t, "624_candidate_failure_logs_promote_atomic_v2.down.sql"))
	if strings.Contains(text, "SELECT *") || strings.Contains(text, "DELETE FROM public.candidate_failure_logs_hot") {
		t.Fatal("migration 624 down must not restore delete-before-insert promotion")
	}
	if !strings.Contains(text, "safe promote function is missing") {
		t.Fatal("migration 624 down must fail closed when the safe function is missing")
	}
}
