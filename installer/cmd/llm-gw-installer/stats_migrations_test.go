package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
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
		"600_outbound_body_to_bodies_hot.sql":                              outboundBodyToBodiesHotMigration600,
		"601_request_logs_bodies_drop_metadata.sql":                        requestLogsBodiesDropMetadataMigration601,
		"602_request_logs_promote_atomic.sql":                              requestLogsPromoteAtomicMigration602,
		"618_request_journey_snapshot_receipts.sql":                        journalSnapshotReceiptsMigration618,
		"614_session_bodies_hot.sql":                                       sessionBodiesHotMigration614,
		"615_session_bodies_hot_promote_function.sql":                      sessionBodiesHotPromoteMigration615,
		"620_provider_error_details_tenant_scope.sql":                      providerErrorDetailsTenantScopeMigration620,
		"621_provider_error_details_cleanup_index.sql":                     providerErrorDetailsCleanupIndexMigration621,
		"622_provider_error_aggregator_state.sql":                          providerErrorAggregatorStateMigration622,
		"623_journal_snapshot_receipts_projection_base.sql":                journalSnapshotProjectionBaseMigration623,
		"624_candidate_failure_logs_promote_atomic_v2.sql":                 candidateFailureLogsPromoteAtomicV2Migration624,
		"625_session_bodies_unified_explicit.sql":                          sessionBodiesUnifiedExplicitMigration625,
		"626_session_bodies_hot_promote_reconcile.sql":                     sessionBodiesHotPromoteReconcileMigration626,
		"627_candidate_failure_logs_aggregation_id_unified.sql":            candidateFailureLogsAggregationIdUnifiedMigration627,
		"628_candidate_failure_logs_promote_atomic_v3.sql":                 candidateFailureLogsPromoteAtomicV3Migration628,
		"629_audit_attachments_cleanup.sql":                                auditAttachmentsCleanupMigration629,
		"630_session_aggregate_outbox.sql":                                 sessionAggregateOutboxMigration630,
		"631_provider_credential_soft_delete.sql":                          providerCredentialSoftDeleteMigration631,
		"655_session_summaries_schema_reconcile.sql":                       sessionSummariesSchemaReconcileMigration655,
		"635_drop_session_turns_unified.sql":                               dropSessionTurnsUnifiedMigration635,
		"647_goal_client_signal.sql":                                       goalClientSignalMigration647,
		"649_routing_analytics_probe_filter.sql":                           routingAnalyticsProbeFilterMigration649,
		"650_auto_route_selection_treatment_attribution.sql":               autoRouteSelectionTreatmentAttributionMigration650,
	}

	for name, embedded := range expected {
		canonicalPath := filepath.Join(canonicalDir, name)
		if name == "600_outbound_body_to_bodies_hot.sql" {
			canonicalPath = filepath.Join(canonicalDir, "up", name)
		}
		canonical, err := os.ReadFile(canonicalPath)
		if err != nil {
			t.Fatalf("read canonical migration %s: %v", name, err)
		}
		if !bytes.Equal(embedded, canonical) {
			t.Fatalf("embedded migration %s differs from canonical source", name)
		}
	}
}

func TestSessionTurnsHotBootstrapIsFinalStateAsset(t *testing.T) {
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS public.session_turns_hot",
		"digest JSONB",
		"CREATE VIEW public.session_turns_with_current_month",
		"security_invoker = true",
		"CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition",
		"installer session-turns bootstrap requires nullable JSONB digest",
	} {
		if !bytes.Contains(sessionTurnsHotBootstrap, []byte(marker)) {
			t.Fatalf("session turns hot bootstrap is missing final-state marker %q", marker)
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
		"600_outbound_body_to_bodies_hot.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"602_request_logs_promote_atomic.sql",
		"618_request_journey_snapshot_receipts.sql",
		"649_routing_analytics_probe_filter.sql",
		"650_auto_route_selection_treatment_attribution.sql",
		"session_turns_hot_bootstrap.sql",
	}

	seen := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		if _, ok := seen[name]; ok {
			t.Fatalf("duplicate installer migration %s", name)
		}
		seen[name] = struct{}{}
	}
	order := []string{
		"552_request_journey_durable_outbox.sql",
		"553_approval_resume_claim.sql",
		"600_outbound_body_to_bodies_hot.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"602_request_logs_promote_atomic.sql",
		"618_request_journey_snapshot_receipts.sql",
	}
	positions := make(map[string]int, len(expected))
	for i, name := range expected {
		positions[name] = i
	}
	for i := 1; i < len(order); i++ {
		if positions[order[i-1]] >= positions[order[i]] {
			t.Fatalf("installer migration order is invalid: %s before %s", order[i-1], order[i])
		}
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

// TestStartupFilesAreAllEmbedded guards against the 2026-08-31 audit gap:
// dbinit.Runner.StartupFiles referenced migrations (614/615/619 and later
// 620-626) that were never added to the embed maps, so a fresh install would
// fail in applySQL with "file not found". This test fails whenever a
// StartupFiles entry has no counterpart in the setupSQLDir output.
func TestStartupFilesAreAllEmbedded(t *testing.T) {
	t.Helper()

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up SQL dir: %v", err)
	}
	defer cleanup()

	runner := dbinit.NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("dbinit.Runner.StartupFiles is empty")
	}
	for _, name := range runner.StartupFiles {
		path := filepath.Join(sqlDir, "startup", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("StartupFiles entry %q is not provided by setupSQLDir — add the file to installer/cmd/llm-gw-installer/embeddata/startup/, the go:embed vars, and both embed maps in main.go: %v", name, err)
		}
	}
}
