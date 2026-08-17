package requestjourney

import (
	"os"
	"strings"
	"testing"
)

const (
	upMigrationPath   = "../../sql/migrations/startup/530_request_journey_contract.sql"
	downMigrationPath = "../../sql/migrations/startup/530_request_journey_contract.down.sql"
)

func TestRequestJourneyMigrationContract(t *testing.T) {
	body := readMigration(t, upMigrationPath)

	for _, required := range []string{
		"ALTER TABLE request_state_transitions",
		"ALTER COLUMN transition_type DROP NOT NULL",
		"request_state_transitions_event_kind_chk",
		"transition_type IS NOT NULL OR event_type IS NOT NULL",
		"gateway_instance_id TEXT",
		"event_type TEXT",
		"stage TEXT",
		"requested_model TEXT",
		"resolved_model TEXT",
		"from_model TEXT",
		"to_model TEXT",
		"from_credential_id BIGINT",
		"to_credential_id BIGINT",
		"attempt_id TEXT",
		"attempt_no INTEGER",
		"model TEXT",
		"provider_id BIGINT",
		"provider TEXT",
		"credential_id BIGINT",
		"outcome TEXT",
		"error_kind TEXT",
		"http_status INTEGER",
		"retry_reason TEXT",
		"switch_reason TEXT",
		"observation_status TEXT",
		"node_health_status TEXT",
		"occurred_at TIMESTAMPTZ",
		"request_state_transitions_journey_required_chk",
		"request_state_transitions_journey_no_metadata_chk",
		"request_state_transitions_event_type_chk",
		"request_state_transitions_journey_stage_chk",
		"request_state_transitions_event_fields_chk",
		"request_state_transitions_switch_fields_chk",
		"request_state_transitions_canceled_outcome_chk",
		"request_state_transitions_degraded_event_chk",
		"request_state_transitions_observation_status_chk",
		"request_state_transitions_node_health_status_chk",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_tenant_request_seq",
		"(tenant_id, request_id, seq)",
		"idx_state_transitions_journey_recent",
		"idx_state_transitions_journey_model_recent",
		"idx_state_transitions_journey_node_recent",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("migration missing %q", required)
		}
	}

	if strings.Contains(body, "'cancelled'") {
		t.Error("migration must use project outcome spelling 'canceled', not 'cancelled'")
	}
	for _, forbidden := range []string{"request_body", "response_body", "authorization", "api_key", "access_token", "secret_key"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("migration must not add sensitive/body field %q", forbidden)
		}
	}
}

func TestRequestJourneyMigrationEnumParity(t *testing.T) {
	body := readMigration(t, upMigrationPath)
	for _, eventType := range AllEventTypes() {
		if !strings.Contains(body, "'"+string(eventType)+"'") {
			t.Errorf("migration event type CHECK missing %q", eventType)
		}
	}
	for _, stage := range AllJourneyStages() {
		if !strings.Contains(body, "'"+string(stage)+"'") {
			t.Errorf("migration journey stage CHECK missing %q", stage)
		}
	}
	for _, status := range AllNodeHealthStatuses() {
		if !strings.Contains(body, "'"+string(status)+"'") {
			t.Errorf("migration node health CHECK missing %q", status)
		}
	}
	for _, status := range []ObservationStatus{ObservationComplete, ObservationDegraded} {
		if !strings.Contains(body, "'"+string(status)+"'") {
			t.Errorf("migration observation CHECK missing %q", status)
		}
	}
}

func TestRequestJourneyDownMigrationIsSymmetric(t *testing.T) {
	body := readMigration(t, downMigrationPath)
	for _, required := range []string{
		"DROP INDEX IF EXISTS idx_state_transitions_journey_recent",
		"DROP INDEX IF EXISTS idx_state_transitions_journey_model_recent",
		"DROP INDEX IF EXISTS idx_state_transitions_journey_node_recent",
		"DROP INDEX IF EXISTS uq_state_transitions_tenant_request_seq",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_journey_required_chk",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_journey_stage_chk",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_event_fields_chk",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_switch_fields_chk",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_canceled_outcome_chk",
		"DROP CONSTRAINT IF EXISTS request_state_transitions_event_kind_chk",
		"ALTER COLUMN transition_type SET NOT NULL",
		"DROP COLUMN IF EXISTS gateway_instance_id",
		"DROP COLUMN IF EXISTS stage",
		"DROP COLUMN IF EXISTS requested_model",
		"DROP COLUMN IF EXISTS resolved_model",
		"DROP COLUMN IF EXISTS from_model",
		"DROP COLUMN IF EXISTS to_model",
		"DROP COLUMN IF EXISTS from_credential_id",
		"DROP COLUMN IF EXISTS to_credential_id",
		"DROP COLUMN IF EXISTS occurred_at",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("down migration missing %q", required)
		}
	}
}

func readMigration(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	return string(body)
}
