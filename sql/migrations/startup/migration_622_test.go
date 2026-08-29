package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration622ProviderErrorAggregatorState(t *testing.T) {
	migration, err := os.ReadFile("622_provider_error_aggregator_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(migration))
	for _, fragment := range []string{
		"candidate_failure_logs_hot_aggregation_id_seq",
		"aggregation_id bigint",
		"provider_error_aggregator_state",
		"last_source_id",
		"alter column aggregation_id set default",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing %q", fragment)
		}
	}

	down, err := os.ReadFile("622_provider_error_aggregator_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, fragment := range []string{
		"drop table if exists public.provider_error_aggregator_state",
		"drop sequence if exists public.candidate_failure_logs_hot_aggregation_id_seq",
		"drop column if exists aggregation_id",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Errorf("rollback migration missing %q", fragment)
		}
	}
}
