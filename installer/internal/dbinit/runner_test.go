package dbinit

import "testing"

func TestStartupFilesIncludeRequestJourneyOutboxPrerequisites(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	want := []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"521_repair_state_transitions_tenant.sql",
		"530_request_journey_contract.sql",
		"531_request_journey_tenant_uniqueness.sql",
		"552_request_journey_durable_outbox.sql",
	}
	positions := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		positions[name] = i
	}
	for _, name := range want {
		if _, ok := positions[name]; !ok {
			t.Errorf("startup migration %s is missing", name)
		}
	}
	for i := 1; i < len(want); i++ {
		if positions[want[i-1]] >= positions[want[i]] {
			t.Errorf("startup migrations are out of order: %s before %s", want[i-1], want[i])
		}
	}
}
