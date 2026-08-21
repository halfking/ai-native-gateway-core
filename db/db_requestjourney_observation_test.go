package db

import (
	"os"
	"strings"
	"testing"
)

func TestApplyMigrationsIncludesRequestJourneyObservationEnsure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureRequestJourneyObservationSchema(migCtx)",
		"func (d *DB) ensureRequestJourneyObservationSchema",
		"request_journey_observation_outbox",
		"claim_fencing_token",
		"uq_state_transitions_legacy_request_seq",
		"DROP INDEX IF EXISTS uq_state_transitions_request_seq",
		"FORCE ROW LEVEL SECURITY",
		"request_journey_observation_outbox_processing_lease_chk",
		"request_journey_observation_outbox_super_admin_bypass",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing request journey observation contract %q", want)
		}
	}
}
