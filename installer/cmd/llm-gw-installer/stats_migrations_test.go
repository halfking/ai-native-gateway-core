package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

func TestStatsStartupMigrationsMatchCanonicalSources(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	expected := map[string][]byte{
		"511_state_transitions_table.sql":      requestJourneyMigration511,
		"515_state_transitions_seq_unique.sql": requestJourneyMigration515,
		// R42 (2026-09-18): durable family base tables — 657/722 ride on
		// them; a fresh install without 516/520 aborted with 42P01 at 657.
		"516_durable_llm_tasks.sql":                                              durableLlmTasksMigration516,
		"520_durable_task_settlement_intents.sql":                                durableTaskSettlementIntentsMigration520,
		"521_repair_state_transitions_tenant.sql":                                requestJourneyMigration521,
		"530_request_journey_contract.sql":                                       requestJourneyMigration530,
		"531_request_journey_tenant_uniqueness.sql":                              requestJourneyMigration531,
		"536_stats_analytics_foundation.sql":                                     statsMigration536,
		"537_usage_facts.sql":                                                    statsMigration537,
		"539_stats_reconciliation_tenant.sql":                                    statsMigration539,
		"540_stats_event_inbox_consumer.sql":                                     statsMigration540,
		"544_stats_adjustments_alignment.sql":                                    statsMigration544,
		"545_stats_reconciliation_phantom_resolution.sql":                        statsMigration545,
		"546_stats_reconciliation_diffs_unique.sql":                              statsMigration546,
		"547_session_project_attribution.sql":                                    statsMigration547,
		"548_stats_reconciliation_diffs_identity.sql":                            statsMigration548,
		"552_request_journey_durable_outbox.sql":                                 requestJourneyMigration552,
		"553_approval_resume_claim.sql":                                          approvalResumeMigration553,
		"554_goal_runs.sql":                                                      goalRunsMigration554,
		"555_goal_run_actions_lease_fencing.sql":                                 goalRunActionsLeaseFencingMigration555,
		"560_session_summaries_tenant_uniqueness.sql":                            sessionSummariesTenantUniquenessMigration560,
		"561_request_logs_view_origin_actor.sql":                                 requestLogsViewOriginActorMigration561,
		"562_fix_request_logs_bodies_partitions_heap.sql":                        fixRequestLogsBodiesPartitionsHeapMigration562,
		"563_session_summary_trigger_on_hot.sql":                                 sessionSummaryTriggerOnHotMigration563,
		"564_session_summary_backfill_safe.sql":                                  sessionSummaryBackfillSafeMigration564,
		"565_cost_usd_pricing_backfill.sql":                                      costUsdPricingBackfillMigration565,
		"566_credentials_governor_revision.sql":                                  credentialsGovernorRevisionMigration566,
		"567_session_analysis_metadata.sql":                                      sessionAnalysisMetadataMigration567,
		"568_credential_priority_flag.sql":                                       credentialPriorityFlagMigration568,
		"569_candidate_binding_scope_revision_canonical.sql":                     candidateBindingScopeRevisionCanonicalMigration569,
		"570_model_offers_insert_priority_passthrough.sql":                       modelOffersInsertPriorityPassthroughMigration570,
		"571_candidate_binding_scope_revision_canonical_priority_hash.sql":       candidateBindingScopeRevisionCanonicalPriorityHashMigration571,
		"600_outbound_body_to_bodies_hot.sql":                                    outboundBodyToBodiesHotMigration600,
		"601_request_logs_bodies_drop_metadata.sql":                              requestLogsBodiesDropMetadataMigration601,
		"602_request_logs_promote_atomic.sql":                                    requestLogsPromoteAtomicMigration602,
		"618_request_journey_snapshot_receipts.sql":                              journalSnapshotReceiptsMigration618,
		"614_session_bodies_hot.sql":                                             sessionBodiesHotMigration614,
		"615_session_bodies_hot_promote_function.sql":                            sessionBodiesHotPromoteMigration615,
		"620_provider_error_details_tenant_scope.sql":                            providerErrorDetailsTenantScopeMigration620,
		"621_provider_error_details_cleanup_index.sql":                           providerErrorDetailsCleanupIndexMigration621,
		"622_provider_error_aggregator_state.sql":                                providerErrorAggregatorStateMigration622,
		"623_journal_snapshot_receipts_projection_base.sql":                      journalSnapshotProjectionBaseMigration623,
		"624_candidate_failure_logs_promote_atomic_v2.sql":                       candidateFailureLogsPromoteAtomicV2Migration624,
		"625_session_bodies_unified_explicit.sql":                                sessionBodiesUnifiedExplicitMigration625,
		"626_session_bodies_hot_promote_reconcile.sql":                           sessionBodiesHotPromoteReconcileMigration626,
		"627_candidate_failure_logs_aggregation_id_unified.sql":                  candidateFailureLogsAggregationIdUnifiedMigration627,
		"628_candidate_failure_logs_promote_atomic_v3.sql":                       candidateFailureLogsPromoteAtomicV3Migration628,
		"629_audit_attachments_cleanup.sql":                                      auditAttachmentsCleanupMigration629,
		"630_session_aggregate_outbox.sql":                                       sessionAggregateOutboxMigration630,
		"631_provider_credential_soft_delete.sql":                                providerCredentialSoftDeleteMigration631,
		"632_audit_attachments_filesystem_cleanup.sql":                           auditAttachmentsFilesystemCleanupMigration632,
		"655_session_summaries_schema_reconcile.sql":                             sessionSummariesSchemaReconcileMigration655,
		"635_drop_session_turns_unified.sql":                                     dropSessionTurnsUnifiedMigration635,
		"647_goal_client_signal.sql":                                             goalClientSignalMigration647,
		"649_routing_analytics_probe_filter.sql":                                 routingAnalyticsProbeFilterMigration649,
		"650_auto_route_selection_treatment_attribution.sql":                     autoRouteSelectionTreatmentAttributionMigration650,
		"656_auto_route_selections_hot.sql":                                      autoRouteSelectionsHotMigration656,
		"657_durable_llm_tasks_decision_history.sql":                             durableTasksDecisionHistoryMigration657,
		"658_auto_route_structured_features.sql":                                 autoRouteStructuredFeaturesMigration658,
		"659_legacy_promote_atomic_cte.sql":                                      legacyPromoteAtomicCTEMigration659,
		"660_credential_model_weekly_peak_unique.sql":                            credentialModelWeeklyPeakUniqueMigration660,
		"662_feature_distribution_stats.sql":                                     featureDistributionStatsMigration662,
		"663_training_export.sql":                                                trainingExportMigration663,
		"664_provider_error_details_agg_key_dedup.sql":                           providerErrorDetailsAggKeyDedupMigration664,
		"666_orchestration_and_stats_tables.sql":                                 orchestrationAndStatsTablesMigration666,
		"667_llm_hourly_stats_timestamp_fix.sql":                                 llmHourlyStatsTimestampFixMigration667,
		"668_llm_hourly_stats_final_fix.sql":                                     llmHourlyStatsFinalFixMigration668,
		"669_training_human_annotations.sql":                                     trainingHumanAnnotationsMigration669,
		"670_routing_optimization.sql":                                           routingOptimizationMigration670,
		"671_local_provider_catalog.sql":                                         localProviderCatalogMigration671,
		"672_local_first_title_summary_routing.sql":                              localFirstTitleSummaryRoutingMigration672,
		"673_annotation_stats_empty_table_fix.sql":                               annotationStatsEmptyTableFixMigration673,
		"674_annotation_request_id_unique.sql":                                   annotationRequestIdUniqueMigration674,
		"675_qwen38_family_vendor.sql":                                           qwen38FamilyVendorMigration675,
		"676_routing_opt_active_fix.sql":                                         routingOptActiveFixMigration676,
		"677_session_summaries_canonical_bootstrap.sql":                          sessionSummariesCanonicalBootstrapMigration677,
		"678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql": requestLogsBodiesHotUniqueRepairMigration678,
		"679_local_credential_unique.sql":                                        localCredentialUniqueMigration679,
		"680_request_logs_current_month_view_bootstrap.sql":                      requestLogsCurrentMonthViewBootstrapMigration680,
		"681_provider_error_details_fingerprint_restore_8part.sql":               providerErrorDetailsFingerprintRestore8partMigration681,
		"682_model_offers_context_window_columns.sql":                            modelOffersContextWindowColumnsMigration682,
		"683_session_dim_ownership_columns.sql":                                  sessionDimOwnershipColumnsMigration683,
		"684_drop_stale_provider_error_tenant_fingerprint.sql":                   dropStaleProviderErrorTenantFingerprintMigration684,
		"685_task_default_routing_tenant_text.sql":                               taskDefaultRoutingTenantTextMigration685,
		"686_fix_session_module_executions_2026_10_bounds.sql":                   fixSessionModuleExecutions2026_10BoundsMigration686,
		"687_fix_473_partition_0800_bounds.sql":                                  fix473Partition0800BoundsMigration687,
		"688_promote_default_retention_align_go_scheduler.sql":                   promoteDefaultRetentionAlignGoSchedulerMigration688,
		"689_candidate_failure_logs_partitions_heap.sql":                         candidateFailureLogsPartitionsHeapMigration689,
		"690_session_summaries_archived_ttl_index.sql":                           sessionSummariesArchivedTTLIndexMigration690,
		// 691-695: extend byte-equality coverage; the map previously stopped
		// at 690, leaving later embed copies unverified against canonical
		// (2026-09-12 audit finding).
		"691_proxy_region_policy.sql":                          proxyRegionPolicyMigration691,
		"692_session_summaries_user_intent_widen.sql":          sessionSummariesUserIntentWidenMigration692,
		"693_provider_models_canonical_cleared_at.sql":         providerModelsCanonicalClearedAtMigration693,
		"694_partition_ensure_timezone.sql":                    partitionEnsureTimezoneMigration694,
		"695_request_logs_promote_final_success_self_heal.sql": requestLogsPromoteFinalSuccessSelfHealMigration695,
		"696_request_logs_view_system_fingerprint.sql":         requestLogsViewSystemFingerprintMigration696,
		"697_request_logs_promote_system_fingerprint.sql":      requestLogsPromoteSystemFingerprintMigration697,
		"698_promote_hot_partition_timezone_pin.sql":           promoteHotPartitionTimezonePinMigration698,
		"699_supplier_errors_ensure_timezone_pin.sql":          supplierErrorsEnsureTimezonePinMigration699,
		"700_request_logs_view_raw_model_name.sql":             requestLogsViewRawModelNameMigration700,
		"701_credential_balance_floor.sql":                     credentialBalanceFloorMigration701,
		"703_supplier_errors_promote_timezone_pin.sql":         supplierErrorsPromoteTimezonePinMigration703,
		// 706-713: extend byte-equality coverage to the storage-v2 session
		// family and hosted-task/outbox channel (R29 audit 2026-09-15: the
		// map previously stopped at 703 while StartupFiles grew past it).
		"706_session_family_s1a.sql":           sessionFamilyS1aMigration706,
		"707_session_turns_s1a.sql":            sessionTurnsS1aMigration707,
		"708_session_bodies_s1a.sql":           sessionBodiesS1aMigration708,
		"711_hosted_tasks.sql":                 hostedTasksMigration711,
		"712_session_mirror_outbox.sql":        sessionMirrorOutboxMigration712,
		"713_session_turns_cost_precision.sql": sessionTurnsCostPrecisionMigration713,
		// R34 (2026-09-17 audit): byte-equality coverage for the five-point
		// sync backfill (704/705/709/710/714/715) — see the go:embed block in
		// main.go.
		"704_plan_quota_probe_backoff.sql":                   planQuotaProbeBackoffMigration704,
		"705_request_logs_reattach_detached_partitions.sql":  requestLogsReattachDetachedPartitionsMigration705,
		"709_work_type_route_coverage.sql":                   workTypeRouteCoverageMigration709,
		"710_request_logs_view_session_family_v2.sql":        requestLogsViewSessionFamilyV2Migration710,
		"714_partition_timezone_pin_remaining.sql":           partitionTimezonePinRemainingMigration714,
		"715_route_incidents_pending_state.sql":              routeIncidentsPendingStateMigration715,
		"716_unify_probe_health_views.sql":                   unifyProbeHealthViewsMigration716,
		"717_request_logs_hot_column_alignment.sql":          requestLogsHotColumnAlignmentMigration717,
		"718_drop_redundant_indexes_and_add_ttl_indexes.sql": dropRedundantIndexesMigration718,
		// R38 (2026-09-17 audit): ensure-shadowed index root fix + request_logs
		// parent-index ownership unification.
		"719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql": unifyEnsureShadowedIndexesMigration719,
		// R40 (2026-09-18): RLS policy vocabulary unification (design §五
		// Phase 1 item 1) — landed in embeddata only (f5328e13c), five-point
		// sync completed in this round.
		"720_rls_policy_vocabulary_unification.sql": rlsPolicyVocabularyUnificationMigration720,
		// 721 (507d78cff, parallel session) landed with file copies only;
		// parity coverage added by R40 five-point completion.
		"721_credential_balance_source_and_error.sql": credentialBalanceSourceAndErrorMigration721,
		// R40 (2026-09-18): durable family schema convergence — repairs
		// pre-final-516 databases missing events/pending tables and
		// checkpoint_payload (evidence: local llm_gateway DB).
		"722_durable_family_schema_convergence.sql": durableFamilySchemaConvergenceMigration722,
		// R40 (2026-09-18): RLS enable for attachments/cfl_columnar_old
		// (design §五 Phase 1 item 3; renumbered 721→723 after the balance
		// metadata migration took 721 mid-round).
		"723_rls_enable_attachments_and_cfl_old.sql": rlsEnableAttachmentsAndCflOldMigration723,
		// 724 (7106e1c5b, parallel session) landed with four of five sync
		// points; parity coverage added by R40-followup (2026-09-18).
		"724_task_type_corrections.sql": taskTypeCorrectionsMigration724,
		// 725 (parallel R41 session) landed with canonical copies only —
		// five-point gap caught by the pre-commit canonical-delivery gate
		// on the R42 merge commit; registered here (R42, 2026-09-18).
		"725_r41_request_logs_and_tmp_super_admin_bypass.sql": r41RequestLogsSuperAdminBypassMigration725,
		// 726 (58384b0d8) landed with the embeddata copy only; runner/main/
		// sequence registration completed by R43 (2026-09-18) — same
		// five-point gap as 720/721/725.
		"726_restore_credential_model_index_hot_unique.sql": restoreCredentialModelIndexHotUniqueMigration726,
		// 730 (R48, 2026-09-20): session role hierarchy — five-point sync
		// completed in the same round as the canonical copy landed.
		"730_session_role_hierarchy.sql": sessionRoleHierarchyMigration730,
		// 731 (R50, 2026-09-21): auto_route_selections role attribution —
		// five-point sync completed in the same round as the canonical copy.
		"731_auto_route_selection_role_attribution.sql": autoRouteSelectionRoleAttributionMigration731,
		// 733/734 (R50, 2026-09-21 catch-up): session storage decoupling v3.
		// Delivered by the v3 session line (as 731/732, renumbered on
		// collision) without the installer five points — the shared gate
		// stayed red until this round synced them. Transaction-safe.
		"733_session_turn_details.sql":           sessionTurnDetailsMigration733,
		"734_request_logs_view_details_join.sql": requestLogsViewDetailsJoinMigration734,
		// 735 (R51, 2026-09-21): models_canonical active folded-name unique
		// index — five-point sync completed in the same round as the
		// canonical copy landed. Transaction-safe (guard + IF NOT EXISTS).
		"735_models_canonical_active_folded_unique.sql": canonicalFoldedUniqueMigration735,
		// 736 (Wave 3 B1, 2026-09-22): peak/off-peak rate multiplier
		// columns + shared SQL resolver — five-point sync in the same
		// round as the migration landed. Idempotent.
		"736_maas_rate_multiplier.sql":           maasRateMultiplierMigration736,
		// 737 (Wave 3 B8, 2026-09-22): internal reconciliation findings
		// table — five-point sync in the same round as the migration
		// landed. Idempotent (CREATE TABLE IF NOT EXISTS).
		"737_maas_reconciliation_findings.sql": maasReconciliationFindingsMigration737,
		// 738 (Wave 3 B1 follow-up, 2026-09-22): view chain gains
		// credits_rate_multiplier — five-point sync landed with R56
		// audit (runner.go had it; embed+map+reconciliation were
		// missing at 764d2514b). Idempotent (CREATE OR REPLACE VIEW
		// with per-view guards).
		"738_view_chain_credits_rate_multiplier.sql": viewChainCreditsRateMultiplierMigration738,
		// 739 (R56, 2026-09-23): promote functions carry
		// rate_multiplier/credits_rate_multiplier — five-point sync in the
		// same round as the migration landed. Idempotent (CREATE OR
		// REPLACE FUNCTION).
		"739_promote_functions_rate_multiplier.sql": promoteFunctionsRateMultiplierMigration739,
		// 740 (R57, 2026-09-23): view chain projects the real client_ip —
		// five-point sync in the same round as the migration landed.
		// Idempotent (regexp append + full top-view rebuild with guards).
		"740_view_chain_client_ip.sql": viewChainClientIPMigration740,
		// 745 (R63, 2026-09-24): report_snapshots 快照表（设计预埋，worker
		// 未实现）—— 曾死放 migrations/ 顶层无投递通道，本轮补五点同步并
		// 入 parity 守卫。
		"745_report_snapshots.sql": reportSnapshotsMigration745,
		// 746 (2026-09-25, 对账报表落地轮): report_snapshots 内部对帐维度
		// 补齐（tenant_id→text + credits/latency 列 + scope 扩员注记）——
		// 与消费方（bg worker / admin 读面）同轮落地，五点同步 + parity
		// 守卫。可重入（ALTER TYPE USING text::text + IF NOT EXISTS）。
		"746_report_snapshots_internal_dims.sql": reportSnapshotsInternalDimsMigration746,
		// 800 (2026-09-24, r0924 supplier-protocol-optimization §3.2):
		// provider_endpoint_protocols 每 provider 多端点表 + 回填 —— 原
		// deploy/sql/migrations/V800 文件从未进任何存量库通道，本轮移入
		// startup 目录（并行代理执行移动）并补五点同步。幂等（IF NOT
		// EXISTS + ON CONFLICT DO NOTHING）。注意：800 无 Go boot ensure
		// （文件头注记 P4 wiring 待接线），升级库靠本 sequence 通道。
		"800_provider_endpoint_protocols.sql": providerEndpointProtocolsMigration800,
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

// TestStartupFilesAreAllEmbedded guards against two drift directions:
//
//  1. The 2026-08-31 audit gap: dbinit.Runner.StartupFiles referenced
//     migrations (614/615/619 and later 620-626) that were never added to the
//     embed maps, so a fresh install would fail in applySQL with "file not
//     found". Checked as StartupFiles ⊆ setupSQLDir output.
//  2. The 2026-09-07 audit gap: 632_audit_attachments_filesystem_cleanup.sql
//     sat in embeddata/startup without a go:embed var or StartupFiles entry,
//     so fresh installs silently lacked the table its runtime writer expects.
//     Checked as embeddata/startup ReadDir ⊆ StartupFiles (*.down.sql exempt —
//     the installer never applies rollbacks).
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
	registered := make(map[string]struct{}, len(runner.StartupFiles))
	for _, name := range runner.StartupFiles {
		registered[name] = struct{}{}
		path := filepath.Join(sqlDir, "startup", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("StartupFiles entry %q is not provided by setupSQLDir — add the file to installer/cmd/llm-gw-installer/embeddata/startup/, the go:embed vars, and the embeddedSQLFiles map in main.go: %v", name, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join("embeddata", "startup"))
	if err != nil {
		t.Fatalf("read embeddata/startup: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		if _, ok := registered[name]; !ok {
			t.Errorf("embeddata/startup file %q is not registered in dbinit.Runner.StartupFiles — wire it into runner.go StartupFiles plus the go:embed var and embeddedSQLFiles entry in main.go, or delete the stray copy", name)
		}
	}
}

// psqlConcurrencyRequired lists canonical startup migrations that may NOT be
// synced into the installer five points: they use CREATE INDEX CONCURRENTLY
// (727/728 via \gexec), which PostgreSQL refuses inside a transaction block —
// and the installer's dbinit runner applies every file through
// `psql --single-transaction` (installer/internal/dbinit/runner.go). Copying
// them in would abort every FRESH INSTALL at that migration (R49 audit,
// 2026-09-20: the naive five-point sync would have been a P0). These files
// ship exclusively through the revision-sequence channel
// (scripts/apply-db-revision-sequence.sh, psql -f without a transaction
// wrapper) against EXISTING databases; fresh installs skip the indexes, which
// are performance-only — a non-concurrent variant may be added later if a
// fresh install ever needs them on day one.
//
// 728 (2026-09-21, R50 follow-up): same \gexec + CONCURRENTLY shape as 727;
// sql/migrations/startup/728_sql_audit_request_logs_credential_model_index.sql
// header comment self-certifies "实现约束与 727 相同".
//
// 729 (2026-09-21, 252 部署验证轮): same three-phase \gexec + CONCURRENTLY
// shape as 727/728 (header comment "实现约束与 727/728 相同"); ships
// exclusively through the revision-sequence channel like its predecessors.
//
// 744 (2026-09-24, 252 SQL 日志审计第六轮): same shape (outbox done-trim
// partial index + session_turns digest-NULL three-phase partial indexes);
// unlike 727/728/729 it also carries a Go ensure mirror
// (db.ensureSqlAuditPartialIndexes) so existing databases converge at boot,
// but the canonical file still must not enter the single-transaction
// installer channel.
var psqlConcurrencyRequired = map[string]string{
	"727_sql_audit_slow_query_indexes.sql":                  "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"728_sql_audit_request_logs_credential_model_index.sql": "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"729_sql_audit_session_turns_credential_ts_index.sql":   "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"744_sql_audit_partial_indexes.sql":                     "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
}

// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered (R34, 2026-09-17
// audit) closes the drift direction no test covered: a canonical migration
// that never reached the installer (704/705/709/710 drifted out — R30
// leftover #8; 714 landed with no installer copy and no Go ensure mirror).
// From 704 onward every canonical up-migration must be registered in
// StartupFiles; anything below 704 is legacy history (pre-703 shapes are
// superseded or Go-ensure-backed) and stays exempt.
func TestCanonicalStartupMigrationsAtOrAbove704AreRegistered(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	entries, err := os.ReadDir(canonicalDir)
	if err != nil {
		t.Fatalf("read canonical startup dir: %v", err)
	}

	runner := dbinit.NewRunner("", "", "", "")
	registered := make(map[string]struct{}, len(runner.StartupFiles))
	for _, name := range runner.StartupFiles {
		registered[name] = struct{}{}
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix := name
		if i := strings.Index(name, "_"); i > 0 {
			prefix = name[:i]
		}
		num, err := strconv.Atoi(prefix)
		if err != nil {
			continue // non-numeric asset (e.g. dated repair scripts)
		}
		if num < 704 {
			continue
		}
		if reason, exempt := psqlConcurrencyRequired[name]; exempt {
			// Deliberate channel split, not drift — but keep it visible so the
			// exemption is re-evaluated whenever the file set changes.
			t.Logf("canonical startup migration %q intentionally not in installer: %s", name, reason)
			continue
		}
		if _, ok := registered[name]; !ok {
			t.Errorf("canonical startup migration %q (>=704) is not registered in dbinit.Runner.StartupFiles — run the five-point sync (embeddata copy, go:embed var + embeddedSQLFiles map in main.go, StartupFiles entry, parity map here), see llm-gateway-installer-migration-3way-sync", name)
		}
	}
}

// TestDurableFamilyPrerequisitesRegistered (R42, 2026-09-18 audit) pins the
// fresh-install ordering invariant the ≥704 floor above cannot see: 657 and
// 722 unconditionally ALTER/reference durable_llm_tasks, whose only creators
// are 516 (tasks/events) and 520 (settlement intents, FK → tasks). Before
// R42 the installer chain shipped 657/722 without 516/520, so every fresh
// install aborted with 42P01 at 657. Any future migration that touches the
// durable family must keep its base-table creators ahead of it in
// StartupFiles.
func TestDurableFamilyPrerequisitesRegistered(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	position := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		position[name] = i
	}

	for _, base := range []string{
		"516_durable_llm_tasks.sql",
		"520_durable_task_settlement_intents.sql",
	} {
		if _, ok := position[base]; !ok {
			t.Errorf("%s is not registered in dbinit.Runner.StartupFiles — 657/722 ALTER/reference durable_llm_tasks and a fresh install cannot succeed without it", base)
		}
	}
	for _, dependent := range []string{
		"657_durable_llm_tasks_decision_history.sql",
		"722_durable_family_schema_convergence.sql",
	} {
		pos, ok := position[dependent]
		if !ok {
			continue // covered by the ≥704 registration test
		}
		// R43 (2026-09-18): pin ordering against 520 too — its FK references
		// durable_llm_tasks, so a reorder that slides dependents between 516
		// and 520 (or past 520) must fail here, not at install time.
		if base516 := position["516_durable_llm_tasks.sql"]; ok && pos < base516 {
			t.Errorf("%s (pos %d) must come after 516_durable_llm_tasks.sql (pos %d) in StartupFiles", dependent, pos, base516)
		}
		if base520 := position["520_durable_task_settlement_intents.sql"]; ok && pos < base520 {
			t.Errorf("%s (pos %d) must come after 520_durable_task_settlement_intents.sql (pos %d) in StartupFiles", dependent, pos, base520)
		}
	}
}
