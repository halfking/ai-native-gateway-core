package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStatsStartupMigrationsMatchCanonicalSources(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	expected := map[string][]byte{
		"511_state_transitions_table.sql":                 requestJourneyMigration511,
		"515_state_transitions_seq_unique.sql":            requestJourneyMigration515,
		"521_repair_state_transitions_tenant.sql":         requestJourneyMigration521,
		"530_request_journey_contract.sql":                requestJourneyMigration530,
		"531_request_journey_tenant_uniqueness.sql":       requestJourneyMigration531,
		"536_stats_analytics_foundation.sql":              statsMigration536,
		"537_usage_facts.sql":                             statsMigration537,
		"539_stats_reconciliation_tenant.sql":             statsMigration539,
		"540_stats_event_inbox_consumer.sql":              statsMigration540,
		"544_stats_adjustments_alignment.sql":             statsMigration544,
		"545_stats_reconciliation_phantom_resolution.sql": statsMigration545,
		"546_stats_reconciliation_diffs_unique.sql":       statsMigration546,
		"547_session_project_attribution.sql":             statsMigration547,
		"548_stats_reconciliation_diffs_identity.sql":     statsMigration548,
		"551_approval_resume_claim.sql":                   approvalResumeMigration551,
		"552_request_journey_durable_outbox.sql":          requestJourneyMigration552,
	}

	for name, embedded := range expected {
		canonical, err := os.ReadFile(filepath.Join(canonicalDir, name))
		if err != nil {
			t.Fatalf("read canonical migration %s: %v", name, err)
		}
		if !bytes.Equal(embedded, canonical) {
			t.Fatalf("embedded migration %s differs from canonical source", name)
		}
	}
}

func TestStatsStartupMigrationsAreWrittenToInstallerDirectories(t *testing.T) {
	expected := []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"521_repair_state_transitions_tenant.sql",
		"530_request_journey_contract.sql",
		"531_request_journey_tenant_uniqueness.sql",
		"536_stats_analytics_foundation.sql",
		"537_usage_facts.sql",
		"539_stats_reconciliation_tenant.sql",
		"540_stats_event_inbox_consumer.sql",
		"544_stats_adjustments_alignment.sql",
		"545_stats_reconciliation_phantom_resolution.sql",
		"546_stats_reconciliation_diffs_unique.sql",
		"547_session_project_attribution.sql",
		"548_stats_reconciliation_diffs_identity.sql",
		"551_approval_resume_claim.sql",
		"552_request_journey_durable_outbox.sql",
	}

	tmp := t.TempDir()
	if err := copySQLBackup(tmp); err != nil {
		t.Fatalf("copy SQL backup: %v", err)
	}

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up SQL dir: %v", err)
	}
	defer cleanup()

	for _, name := range expected {
		backupPath := filepath.Join(tmp, "db", "init", "startup", name)
		setupPath := filepath.Join(sqlDir, "startup", name)
		backup, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("read backup migration %s: %v", name, err)
		}
		setup, err := os.ReadFile(setupPath)
		if err != nil {
			t.Fatalf("read setup migration %s: %v", name, err)
		}
		if !bytes.Equal(backup, setup) {
			t.Fatalf("installer migration %s differs between backup and setup directories", name)
		}
	}
}
