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
		"511_state_transitions_table.sql":                                  requestJourneyMigration511,
		"515_state_transitions_seq_unique.sql":                             requestJourneyMigration515,
		"521_repair_state_transitions_tenant.sql":                          requestJourneyMigration521,
		"530_request_journey_contract.sql":                                 requestJourneyMigration530,
		"531_request_journey_tenant_uniqueness.sql":                        requestJourneyMigration531,
		"536_stats_analytics_foundation.sql":                               statsMigration536,
		"537_usage_facts.sql":                                              statsMigration537,
		"539_stats_reconciliation_tenant.sql":                              statsMigration539,
		"540_stats_event_inbox_consumer.sql":                               statsMigration540,
		"544_stats_adjustments_alignment.sql":                              statsMigration544,
		"545_stats_reconciliation_phantom_resolution.sql":                  statsMigration545,
		"546_stats_reconciliation_diffs_unique.sql":                        statsMigration546,
		"547_session_project_attribution.sql":                              statsMigration547,
		"548_stats_reconciliation_diffs_identity.sql":                      statsMigration548,
		"552_request_journey_durable_outbox.sql":                           requestJourneyMigration552,
		"553_approval_resume_claim.sql":                                    approvalResumeMigration553,
		"554_goal_runs.sql":                                                goalRunsMigration554,
		"555_goal_run_actions_lease_fencing.sql":                           goalRunActionsLeaseFencingMigration555,
		"560_session_summaries_tenant_uniqueness.sql":                      sessionSummariesTenantUniquenessMigration560,
		"561_request_logs_view_origin_actor.sql":                           requestLogsViewOriginActorMigration561,
		"562_fix_request_logs_bodies_partitions_heap.sql":                  fixRequestLogsBodiesPartitionsHeapMigration562,
		"563_session_summary_trigger_on_hot.sql":                           sessionSummaryTriggerOnHotMigration563,
		"564_session_summary_backfill_safe.sql":                            sessionSummaryBackfillSafeMigration564,
		"565_cost_usd_pricing_backfill.sql":                                costUsdPricingBackfillMigration565,
		"566_credentials_governor_revision.sql":                            credentialsGovernorRevisionMigration566,
		"567_session_analysis_metadata.sql":                                sessionAnalysisMetadataMigration567,
		"568_credential_priority_flag.sql":                                 credentialPriorityFlagMigration568,
		"569_candidate_binding_scope_revision_canonical.sql":               candidateBindingScopeRevisionCanonicalMigration569,
		"570_model_offers_insert_priority_passthrough.sql":                 modelOffersInsertPriorityPassthroughMigration570,
		"571_candidate_binding_scope_revision_canonical_priority_hash.sql": candidateBindingScopeRevisionCanonicalPriorityHashMigration571,
		"601_request_logs_bodies_drop_metadata.sql":                        requestLogsBodiesDropMetadataMigration601,
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
		"552_request_journey_durable_outbox.sql",
		"553_approval_resume_claim.sql",
		"554_goal_runs.sql",
		"555_goal_run_actions_lease_fencing.sql",
		"560_session_summaries_tenant_uniqueness.sql",
		"561_request_logs_view_origin_actor.sql",
		"562_fix_request_logs_bodies_partitions_heap.sql",
		"563_session_summary_trigger_on_hot.sql",
		"564_session_summary_backfill_safe.sql",
		"565_cost_usd_pricing_backfill.sql",
		"566_credentials_governor_revision.sql",
		"567_session_analysis_metadata.sql",
		"568_credential_priority_flag.sql",
		"569_candidate_binding_scope_revision_canonical.sql",
		"570_model_offers_insert_priority_passthrough.sql",
		"571_candidate_binding_scope_revision_canonical_priority_hash.sql",
		"601_request_logs_bodies_drop_metadata.sql",
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
