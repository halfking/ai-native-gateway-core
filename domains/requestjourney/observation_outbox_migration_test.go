package requestjourney

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestDurableObservationOutboxMigrationContract(t *testing.T) {
	body := readMigration(t, "../../sql/migrations/startup/549_request_journey_durable_outbox.sql")
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ",
		"request_state_transitions_retry_at_event_chk",
		"retry_at IS NULL OR event_type = 'retry_scheduled'",
		"CREATE TABLE IF NOT EXISTS request_journey_observation_outbox",
		"payload_hash   TEXT NOT NULL",
		"next_retry_at  TIMESTAMPTZ NOT NULL",
		"claim_owner    TEXT",
		"claim_until    TIMESTAMPTZ",
		"claim_token    BIGINT NOT NULL DEFAULT 0",
		"request_journey_observation_outbox_lease_chk",
		"FOR ALL",
		"WITH CHECK",
		"app.current_tenant",
		"app.current_role",
		"app.bypass_rls",
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

func TestDurableObservationOutboxMigrationMatchesInstallerEmbed(t *testing.T) {
	canonical := readMigration(t, "../../sql/migrations/startup/549_request_journey_durable_outbox.sql")
	embedded, err := os.ReadFile("../../installer/cmd/llm-gw-installer/embeddata/startup/549_request_journey_durable_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(canonical), embedded) {
		t.Fatal("canonical 549 migration differs from installer embedded migration")
	}
}

func TestDurableObservationOutboxDownMigrationContract(t *testing.T) {
	body, err := os.ReadFile("../../sql/migrations/startup/549_request_journey_durable_outbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"cannot roll back migration 549 while request journey observations remain in outbox",
		"DROP TABLE IF EXISTS request_journey_observation_outbox",
		"DROP INDEX IF EXISTS idx_state_transitions_journey_retry_at",
		"DROP COLUMN IF EXISTS retry_at",
	} {
		if !strings.Contains(string(body), required) {
			t.Errorf("durable observation down migration missing %q", required)
		}
	}
}
