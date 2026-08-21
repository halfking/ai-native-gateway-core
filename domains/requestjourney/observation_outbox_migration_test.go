package requestjourney

import (
	"os"
	"strings"
	"testing"
)

func TestDurableObservationOutboxMigrationContract(t *testing.T) {
	body := readMigration(t, "../../sql/migrations/startup/552_request_journey_durable_outbox.sql")
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ",
		"request_state_transitions_retry_at_event_chk",
		"retry_at IS NULL OR event_type = 'retry_scheduled'",
		"CREATE TABLE IF NOT EXISTS request_journey_observation_outbox",
		"payload_hash   TEXT NOT NULL",
		"next_retry_at  TIMESTAMPTZ NOT NULL",
		"claim_owner",
		"claim_until",
		"claim_fencing_token",
		"request_journey_observation_outbox_claim_fence_chk",
		"request_journey_observation_outbox_processing_lease_chk",
		"request_journey_observation_outbox_identity_uq",
		"idx_request_journey_observation_outbox_due",
		"idx_request_journey_observation_outbox_lease",
		"ENABLE ROW LEVEL SECURITY",
		"request_journey_observation_outbox_tenant_isolation",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("durable observation migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(body), "request_body") || strings.Contains(strings.ToLower(body), "authorization") {
		t.Fatal("observation outbox must remain content-free")
	}
}

func TestDurableObservationOutboxDownMigrationContract(t *testing.T) {
	body, err := os.ReadFile("../../sql/migrations/startup/552_request_journey_durable_outbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"BEGIN;",
		"LOCK TABLE public.request_journey_observation_outbox IN ACCESS EXCLUSIVE MODE",
		"SELECT count(*) INTO v_outbox_rows FROM public.request_journey_observation_outbox",
		"Migration 552 down refused",
		"DROP TABLE IF EXISTS request_journey_observation_outbox",
		"DROP INDEX IF EXISTS idx_state_transitions_journey_retry_at",
		"DROP COLUMN IF EXISTS retry_at",
		"COMMIT;",
	} {
		if !strings.Contains(string(body), required) {
			t.Errorf("durable observation down migration missing %q", required)
		}
	}
}
