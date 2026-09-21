package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration617CandidateFailureHotContract(t *testing.T) {
	up, err := os.ReadFile("617_candidate_failure_logs_hot_contract.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(up))
	for _, required := range []string{
		"begin;", "alter table if exists public.candidate_failure_logs", "add column if not exists session_id text",
		"add column if not exists per_attempt_latency_ms integer", "alter table if exists public.candidate_failure_logs_hot",
		"commit;",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("migration 617 missing %q", required)
		}
	}
	if strings.Contains(body, "drop column") {
		t.Fatal("forward migration must not drop candidate failure columns")
	}

	down, err := os.ReadFile("617_candidate_failure_logs_hot_contract.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBody := strings.ToLower(string(down))
	for _, required := range []string{"begin;", "drop column if exists session_id", "drop column if exists per_attempt_latency_ms", "commit;"} {
		if !strings.Contains(downBody, required) {
			t.Fatalf("migration 617 down missing %q", required)
		}
	}
}
