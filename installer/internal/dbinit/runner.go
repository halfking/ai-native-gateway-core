// Package dbinit 提供数据库初始化能力（应用 schema + seed）
package dbinit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner DB 初始化执行器
type Runner struct {
	CitusContainer string // "kx-citus"
	DBUser         string // "kxuser"
	DBName         string // "llm_gateway"
	SQLDir         string // 包含 00-prereqs.sql / 01-schema.sql / 02-seed.sql
	StartupFiles   []string
}

// NewRunner 创建 Runner
func NewRunner(citusContainer, dbUser, dbName, sqlDir string) *Runner {
	return &Runner{
		CitusContainer: citusContainer,
		DBUser:         dbUser,
		DBName:         dbName,
		SQLDir:         sqlDir,
		StartupFiles: []string{
			// The 01-schema baseline was dumped around 477 but is not a faithful
			// snapshot of any single lineage: the pre-478 files below are the
			// canonical creators of objects the baseline either lacks or carries
			// in an older shape. Without them fresh installs abort somewhere in
			// 510..802 (42P01/42703) — the 2026-09-30 fresh-install-chain review
			// red-flagged the first one (622) and the 2026-10-01 e2e round walked
			// the whole sequence to green.
			//
			// hot/parent alignment: 388/484/491/510/532/542/543 + 392/535/617
			// (candidate_failure_logs) + 471 (session_summaries archival) are
			// self-guarded historical migrations, registrable as-is; 523/350 are
			// NOT (their view/function surgery conflicts with the newer baseline)
			// and became the 804/805 reconciles instead.
			"388_billing_cancellation_audit.sql",
			// 392/535/617 predate this list's 478 floor: the 01-schema baseline was
			// dumped around 477 and carries neither the candidate_failure_logs
			// hot/monthly-partition split (392), the atomic promote replacement
			// (535), nor the session_id/per_attempt_latency_ms writer columns (617).
			// 622 ALTERs and UPDATEs candidate_failure_logs_hot unconditionally, so
			// fresh installs aborted at 622 with 42P01 (2026-09-30
			// fresh-install-chain review; wired 2026-10-01).
			"392_candidate_failure_logs_monthly_partition.sql",
			// 471 is baseline-gap class like 392/535/617 (wired 2026-10-01): the
			// archival columns/session_summaries index it adds are consumed by 690.
			// Self-guarded information_schema checks make it directly registrable.
			"471_session_summaries_archival.sql",
			// 426/470/472 wired 2026-10-04 (§9.116). These are the LAST three
			// relations that are genuinely this repo's responsibility and were
			// still missing on a fresh install; §9.116 re-derived that set from
			// 116 candidates down to 3.
			//
			//   426 → task_type_centroids      (read by autoroute/embedding_classifier.go)
			//   470 → cache_metrics            (read by domains/cachemetrics/recorder.go,
			//                                    admin/cache_metrics_handler.go,
			//                                    internal/partguard/parents.go)
			//   472 → cache_metrics_default    (the DEFAULT partition 470 needs for
			//                                    rows that fall outside any month)
			//
			// All three applied cleanly to a real fresh-install database, and
			// none of them has an information_schema post-condition block, so
			// unlike 456 (§9.114.3) they are not landmines. 472 only references
			// cache_metrics (which 470 creates) plus pg_inherits/pg_tables, so
			// 470 must precede 472 — that is why they are adjacent here.
			"426_task_type_centroids.sql",
			"470_cache_metrics.sql",
			"472_cache_metrics_partitions.sql",
			// 456 wired 2026-10-04 (§9.114), immediately before 467 because the
			// two are the same family: display/summary columns the session list
			// query projects. Without 456 the fresh install was missing
			// sessions.summary / summary_model / summary_generated_at, so
			// GET /api/admin/turns/sessions failed 42703 "column s.summary does
			// not exist" — a second 500 on the same endpoint, right behind the
			// session_title_states 42P01 that 550/551 fix.
			//
			// 456 was never registered because it could not pass: its
			// post-condition block queried information_schema for
			// table_schema='gateway' while its own ALTERs create the columns in
			// `public`, and no `gateway` schema exists anywhere. The assertion
			// therefore raised unconditionally. That is fixed in the migration
			// itself (gateway -> public); 430, which 456's header names as its
			// up-stream, is still unregistered, but the baseline already carries
			// the three tables 456 alters, so 456 applies cleanly at this
			// position — verified by rebuilding the gate database, not assumed.
			"456_session_v2_display_columns.sql",
			// 467 is baseline-gap class (wired 2026-10-04): the baseline
			// CREATE TABLE public.sessions carries neither `title` nor
			// `user_tags`, and no registered migration adds them — 467, which
			// does, was simply never wired. Consequence was that on a fresh
			// install SessionAggregator.GetSessionMetadata and
			// UpdateSessionMetadata always failed 42703 "column \"title\" does
			// not exist" (both statements select/write sessions.title +
			// sessions.user_tags). Production 252 was unaffected because it
			// has the columns, which is exactly why the defect stayed
			// invisible: the class of bug the 388/392/471 entries above
			// already describe, with one more file missed.
			//
			// Self-guarded (IF NOT EXISTS on information_schema.columns), so
			// this is a no-op on any database that already has the columns.
			"467_sessions_title_user_tags.sql",
			// 351 wired 2026-10-04 (§9.115). This supersedes a conclusion I
			// recorded in §9.105 and in session_aggregator.go's comment, which
			// said registering 351 was deliberately left out "because nothing
			// calls it". That was true of ONE half and false of the other:
			//
			//   - the READ path (SessionAggregator.GetSessionMetadata) really
			//     has no production caller — only tests, and they drive a mock,
			//     so its SQL is never parsed. That half of the claim stands.
			//   - the WRITE path is live. cmd/gateway/approval_integration.go
			//     constructs analysis.NewSessionStateProjector and injects it
			//     via CacheUpdateHook.SetStateProjector, and
			//     domains/hooks/sessionaudit/cache_update_hook.go calls
			//     Project() on every audit update. Project() INSERTs into
			//     session_tags. On a fresh install that statement is 42P01,
			//     and the hook treats the failure as best-effort — so the
			//     projection is silently lost rather than reported.
			//
			// 351 creates SIX tables, not one: session_tags,
			// session_request_summaries, session_embeddings, session_clusters,
			// session_cluster_members, session_optimization_suggestions. The
			// other five also have production readers (domains/analysis/
			// clusterer.go, request_summary.go, optimizer.go,
			// admin/session_clusters_handler.go, admin/session_panorama_
			// handler.go), so one unregistered file accounted for six of the
			// missing relations measured in §9.114.4. I first wrote "five"
			// here and in the test; the count is measured, not assumed.
			//
			// Unlike 456 (§9.114.3) this file had no information_schema
			// post-condition block and referenced nothing it did not create,
			// so it applies cleanly to a real fresh install — all six tables,
			// no error. But it was NOT re-runnable: PostgreSQL has no
			// `CREATE POLICY IF NOT EXISTS`, and all 12 of this file's
			// policies were unguarded, so a second run died on
			// `policy "…" for table … already exists` and, being inside one
			// transaction, took the rest of the script with it. Fixed by
			// guarding each policy with DROP POLICY IF EXISTS, matching the
			// IF NOT EXISTS the same file already used on its indexes;
			// first-run behaviour is unchanged. Verified by applying the file
			// twice in a row with no error.
			"351_session_analytics_tables.sql",
			// session_turns_hot_bootstrap (installer-only final-state asset,
			// 526+636 projection) moved here 2026-10-01: it must precede 706/707
			// — 707 §2 expands session_turns_hot in lock-step with the parent
			// and its parity check requires the baseline-era 42-column parent
			// shape this bootstrap is projected onto. It previously sat after
			// 731, where 707's hot-side statements crashed 42P01 on fresh
			// installs (the sequence never reached 707 before the baseline-gap
			// fixes landed). Still ahead of 733/734 as the R51 ruling requires.
			"session_turns_hot_bootstrap.sql",
			"478_auto_route_affinity.sql",
			// 484/485/487/491/510 are baseline-gap class (wired 2026-10-01): hot-side
			// status_code, request_logs parent's raw_model_name (hot twin via 603)
			// and system_fingerprint (hot twin via 603), the t0..t9 queue
			// timestamps, and request_type — all consumed by 573's rebuilt view
			// and 696/700.
			"484_request_logs_hot_add_status_code.sql",
			// 486 (wired 2026-10-12): probe_rollback/node_probe revert scans
			// read cmb.probe_revert_at; 486 fixed the production 42703 on
			// 2026-08-13 but was never registered, so fresh installs re-hit
			// the identical failure every ~30s (1088 repeats on the host-first
			// install before this was wired).
			"486_credential_model_bindings_add_probe_revert_at.sql",
			"485_request_logs_add_raw_model_name.sql",
			"487_request_logs_add_system_fingerprint.sql",
			"491_request_logs_queue_timestamps.sql",
			"510_request_type.sql",
			// 532/542/543 are baseline-gap class (wired 2026-10-01): hot+parent
			// is_final_success / token_band / discard_events; 573's view and the
			// 695/747 final-success path reference them.
			"532_request_logs_final_success.sql",
			"542_request_logs_token_band.sql",
			"543_request_logs_discard_events.sql",
			"511_state_transitions_table.sql",
			"515_state_transitions_seq_unique.sql",
			// R42 (2026-09-18): 516/520 are the only creators of the durable
			// family base tables. 657 and 722 both ALTER/reference
			// durable_llm_tasks unconditionally, so a fresh install without
			// them aborted with 42P01 at 657 (the gap predates this round;
			// 722/723 registration in R40 rode the same broken chain).
			"516_durable_llm_tasks.sql",
			"520_durable_task_settlement_intents.sql",
			"521_repair_state_transitions_tenant.sql",
			"530_request_journey_contract.sql",
			"531_request_journey_tenant_uniqueness.sql",
			// 534 is wired 2026-10-02 and is a PRODUCTION BLOCKER fix, not a
			// coverage nicety. db/handoff_schema.go runs a contract on every
			// db.Open that raises unless handoff_logs is a RANGE partitioned
			// parent with a handoff_logs_hot heap twin, the
			// handoff_logs_with_current_month view, and both partition
			// functions. 534 is the only migration that produces that shape
			// and it was never registered, so every fresh install built from
			// the baseline plus the registered chain produced a database the
			// gateway refused to open.
			//
			// Reproduced, not inferred: with the 198-migration chain applied
			// cleanly (applied=198 failed=0 missing=0), the 715 fresh-chain
			// test fails with
			//   handoff hot+columnar schema contract: ERROR: handoff_logs
			//   schema contract requires a RANGE partitioned parent; run
			//   startup migration 534 (SQLSTATE P0001)
			// and the failure is in db.Open, not in the chain.
			//
			// Placement: 534 only needs the baseline's heap handoff_logs, and
			// it is written to replay onto exactly that legacy heap shape, so
			// it sits with the 532/535 baseline-gap class. The registered
			// chain carries no migration below 388 — the baseline snapshot
			// owns the pre-388 era — so 534 is the one case where a
			// sub-388 number genuinely belongs in the chain, because no
			// snapshot bootstrap can carry it.
			//
			// Citus columnar is not a new dependency here: 392/532/535/562/627
			// in this same chain already use it.
			//
			// 517 is registered here — AHEAD of 534 — on the Owner's decision
			// (2026-10-02), after the "keep it unregistered" note below was
			// written. Placement is load-bearing; the rest of this comment is
			// the evidence for it.
			//
			// Why ahead: 517 declares handoff_log_id REFERENCES handoff_logs(id),
			// valid on the baseline heap table. 534 rebuilds handoff_logs as a
			// RANGE-partitioned parent with no unique constraint (PostgreSQL
			// refuses PK(id) on a partitioned table — the constraint must
			// contain the partition key). So 534-after-517 fails outright:
			//   ERROR: there is no unique constraint matching given keys for
			//   referenced table "handoff_logs"
			// 534's own DO block (534:289-307) then DROPs that FK. Registering
			// 517 first and letting 534 remove it is the same end state
			// production is already in — 534 has done this on existing
			// databases — and it closes the 42P01 gap for fresh installs.
			//
			// Why the Owner chose "no unique constraint, no FK" rather than
			// keeping a partition-compatible UNIQUE (id, created_at): measured
			// on a real PG reproducing confirmation_pg.go's write path, the FK
			// is unsatisfiable regardless. Go:129 INSERTs into handoff_logs_hot
			// and returns its id; Go:162 immediately uses that id for
			// handoff_log_id, but the row is in the HOT table and promotion
			// into handoff_logs is async (retention
			// lifecycle.handoff_logs_hot_retention_hours = 8h):
			//   -> UPDATE fails, SQLSTATE 23503 foreign key violation
			// Control case (promote the row first, then UPDATE) -> succeeds.
			// So 517's FK is a leftover from the pre-534 world where writes
			// landed directly in handoff_logs. A unique constraint would be
			// paid for on every production install (building it spans every
			// partition) and 534 would drop the FK anyway.
			//
			// Verified end state on a clean database
			// (00-prereqs + baseline + 517 + 534): rc=0, zero errors,
			// handoff_pending_confirmations present, handoff_logs a partitioned
			// parent, handoff_logs_hot + handoff_logs_with_current_month
			// present, and 0 FKs left pointing at handoff_logs.
			"517_handoff_pending_confirmations.sql",
			// 527 is NOT optional with 517 and must stay immediately after it.
			// 517 creates the base table; 527 is what completes it. Registering
			// 517 alone is not merely incomplete — it is actively wrong:
			//   * 517's CHECK is status IN ('pending','confirmed','expired'),
			//     while confirmation_pg.go writes 'accounting_confirmed'. 527
			//     widens status to VARCHAR(32) and adds that value (plus
			//     restored / manual_required) to the constraint.
			//   * 527 adds goal_state / goal_state_version / restore_status /
			//     restore_error / restore_attempted_at / restored_at and the
			//     idx_handoff_pending_restore index.
			// Caught by the full-package sweep, not by the migration gate:
			//   domains/hooks/handoff TestPGStoreSavePendingSemantics
			//   ERROR: column "goal_state" of relation
			//   "handoff_pending_confirmations" does not exist (SQLSTATE 42703)
			// That test used to pass only because nothing created the table at
			// all, so it made its own full one. Registering 517 alone handed it
			// the half-built table instead.
			"527_handoff_durable_goal_state.sql",
			"534_handoff_logs_hot_columnar.sql",
			"535_candidate_failure_logs_atomic_promote.sql",
			"536_stats_analytics_foundation.sql",
			"537_usage_facts.sql",
			"539_stats_reconciliation_tenant.sql",
			"540_stats_event_inbox_consumer.sql",
			// 541 must precede 568: it creates candidate_binding_scope_revision,
			// which 568_credential_priority_flag.sql indexes into.
			"541_candidate_binding_scope_revision.sql",
			"544_stats_adjustments_alignment.sql",
			"545_stats_reconciliation_phantom_resolution.sql",
			"546_stats_reconciliation_diffs_unique.sql",
			"547_session_project_attribution.sql",
			"548_stats_reconciliation_diffs_identity.sql",
			// session_title_states wired 2026-10-04 (§9.114). Same class as
			// 467 one line above: a self-contained, additive, idempotent
			// migration that was simply never registered, so the table did
			// not exist on a fresh install. It is not cosmetic — five
			// production files read or write it (admin/turns_sessions.go,
			// admin/session_meta_view.go, admin/session_turns_v2.go,
			// internal/titlestore/store.go, db/db.go), which made
			// GET /api/admin/turns/sessions answer 500
			// (42P01 relation "public.session_title_states" does not exist)
			// on every fresh install, and made titlestore's conditional
			// title commit fail. Production 252 was unaffected because it
			// has the table, which is exactly why the defect stayed
			// invisible.
			//
			// Both files are self-contained: 550 creates the table with
			// CREATE TABLE IF NOT EXISTS and only alters that same table;
			// 551 adds constraints and indexes on it. Neither references any
			// object created by a later migration, so placing them here
			// (after 548, before 552) is safe.
			"550_session_title_states_expand.sql",
			"551_session_title_states_indexes.sql",
			"552_request_journey_durable_outbox.sql",
			"553_approval_resume_claim.sql",
			"554_goal_runs.sql",
			"555_goal_run_actions_lease_fencing.sql",
			"655_session_summaries_schema_reconcile.sql",
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
			"572_session_summary_large_token_ratio.sql",
			// 573 (wired 2026-10-01) must precede 577: it drops the baseline's
			// body-column-referencing request_logs views and rebuilds them
			// body-free, unblocking 603's outbound_body DROP; 577 then renames
			// the rebuilt wrapper. End state matches production (bodies_progress
			// stays dropped, per 573's own ruling).
			"573_drop_request_logs_body_columns.sql",
			// 577/610 rebuild the request_logs view chain by renaming the
			// current wrapper and re-wrapping (577: customer_id; 610:
			// request_class/due_at). Both are fully guarded and the baseline
			// request_logs tables carry customer_id, so they apply cleanly on
			// fresh installs; 696/700/738/740 later expect the wrappers.
			"577_request_logs_view_customer_id.sql",
			"600_outbound_body_to_bodies_hot.sql",
			"601_request_logs_bodies_drop_metadata.sql",
			"602_request_logs_promote_atomic.sql",
			// 603 (wired 2026-10-01) is the canonical creator for the ten
			// request_logs_hot columns (system_fingerprint/raw_model_name/…)
			// that only deploy-lineage databases had; 603's defensive view
			// check needs 577's without_customer_id wrapper, so it sits after it.
			"603_repair_request_logs_schema_consistency.sql",
			"606_session_summaries_agent_expert_tags.sql",
			"610_request_class_due_at.sql",
			"614_session_bodies_hot.sql",
			"615_session_bodies_hot_promote_function.sql",
			"617_candidate_failure_logs_hot_contract.sql",
			// 803 closes the second canonical-chain gap the fresh-install e2e
			// found (after 392/535/617): 627's candidate_failure_logs_unified
			// view references hot columns that only deploy-chain (V359)
			// databases ever had. Numbered in the 8xx range per the tail
			// convention but applied here, ahead of its 627 consumer.
			"803_candidate_failure_logs_hot_column_reconcile.sql",
			"618_request_journey_snapshot_receipts.sql",
			"620_provider_error_details_tenant_scope.sql",
			"621_provider_error_details_cleanup_index.sql",
			"622_provider_error_aggregator_state.sql",
			// 612 is wired 2026-10-02 with 517; see the 534 note above for
			// why. Placed with the other provider/credential migrations because
			// it adds a binding-scoped capability row keyed on
			// credential_model_bindings, whose primary key 612 installs itself
			// (the baseline ships that table with no PK — verified).
			"612_native_responses_capability.sql",
			"623_journal_snapshot_receipts_projection_base.sql",
			"624_candidate_failure_logs_promote_atomic_v2.sql",
			"625_session_bodies_unified_explicit.sql",
			"626_session_bodies_hot_promote_reconcile.sql",
			"627_candidate_failure_logs_aggregation_id_unified.sql",
			"628_candidate_failure_logs_promote_atomic_v3.sql",
			"629_audit_attachments_cleanup.sql",
			"630_session_aggregate_outbox.sql",
			"631_provider_credential_soft_delete.sql",
			"632_audit_attachments_filesystem_cleanup.sql",
			"635_drop_session_turns_unified.sql",
			"637_session_bodies_unified_today_visible.sql",
			"638_session_bodies_promote_guard.sql",
			"639_provider_error_details_credential.sql",
			// 640 is baseline-gap class (wired 2026-10-01): protocol triplet on
			// session_turns parent+hot + view projection; 713's turns-view
			// rebuild references client_protocol/upstream_protocol/ir_metadata.
			"640_session_turns_protocol_fields.sql",
			"644_tuning_views_selfcheck_and_candidate_failure_cache.sql",
			"645_session_bodies_hot_request_unique_repair.sql",
			"646_proxy_management_canonical.sql",
			"647_goal_client_signal.sql",
			"649_routing_analytics_probe_filter.sql",
			"650_auto_route_selection_treatment_attribution.sql",
			"651_provider_quality_hot_rollup.sql",
			"652_system_monitor_fallback_queue.sql",
			"653_archive_credential_model_index_canonical_return.sql",
			"654_archive_credential_model_index_detach_drop.sql",
			"656_auto_route_selections_hot.sql",
			"657_durable_llm_tasks_decision_history.sql",
			"658_auto_route_structured_features.sql",
			"659_legacy_promote_atomic_cte.sql",
			"660_credential_model_weekly_peak_unique.sql",
			"662_feature_distribution_stats.sql",
			"663_training_export.sql",
			"664_provider_error_details_agg_key_dedup.sql",
			"666_orchestration_and_stats_tables.sql",
			"667_llm_hourly_stats_timestamp_fix.sql",
			"668_llm_hourly_stats_final_fix.sql",
			"669_training_human_annotations.sql",
			"670_routing_optimization.sql",
			"671_local_provider_catalog.sql",
			"672_local_first_title_summary_routing.sql",
			"673_annotation_stats_empty_table_fix.sql",
			"674_annotation_request_id_unique.sql",
			"675_qwen38_family_vendor.sql",
			"676_routing_opt_active_fix.sql",
			"677_session_summaries_canonical_bootstrap.sql",
			"678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql",
			"679_local_credential_unique.sql",
			"680_request_logs_current_month_view_bootstrap.sql",
			"681_provider_error_details_fingerprint_restore_8part.sql",
			// 804 (wired 2026-10-01) closes the third canonical-chain gap: 523's
			// context_window triplet is only half-covered by the baseline, and
			// 682's model_offers rebuild references cmb.context_window_source/
			// _updated_at. 523 itself is un-appliable on fresh installs (its
			// 2026-07 view definition would shrink the baseline's model_offers,
			// which CREATE OR REPLACE VIEW forbids), so only the columns are
			// reconciled here.
			"804_credential_model_context_window_columns.sql",
			// 852 (wired 2026-10-12) closes the second half of 804's gap plus a
			// sibling found in the same file-level audit: 469 (context_window
			// triplet) and 478 (reasoning_caps column + partial indexes) on
			// models_canonical were never registered, and the 01-schema
			// baseline predates both — fresh installs silently lack them, and
			// any query joining those columns (admin provider/credential model
			// lists' three-level override chain, the capability column block)
			// dies with 42703. Observed live as GET /api/providers/{id}/models
			// and .../credentials/{cid}/models → 500 on the host-first fresh
			// install (2026-10-12). Deploy-lineage databases ran 469/478 long
			// ago; the IF NOT EXISTS reconcile is a no-op there. Column types,
			// defaults and indexes match 469/478 exactly. 045/422/451 are also
			// unregistered but fully covered by the baseline (verified live).
			"852_models_canonical_column_set_reconcile.sql",
			// 489/490 (wired 2026-10-20): 853 自己在文件头声明依赖
			// 「489/490_credential_probe_queue_*（队列表本体）」，而那两条**从未**
			// 登记进本链 ⇒ 新装环境在 853 处报
			//   ERROR: relation "public.credential_probe_queue" does not exist
			// 并**中止整个安装**（真库临时库实测；pristine HEAD 同样失败 ⇒ 既有缺陷，
			// 与 2026-10-20 的 request_logs→session_turns 退役轮无关，但挡住了
			// 「新装路径可跑通」这个交付物）。
			// 853 的注释本就写着「once 489/490 create the queue」——事实是它们没被登记。
			// 489 自称「startup mirror of domain migrations 343/347/349」，490 补齐运行时列；
			// 两条都幂等（CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS /
			// CREATE INDEX IF NOT EXISTS）且自包含（不引用其它关系）⇒ 可直接登记。
			// 排位：必须在 853 **之前**（853 是 ALTER TABLE 那张表）。
			"489_credential_probe_queue_startup_backfill.sql",
			"490_credential_probe_queue_runtime_columns.sql",
			// 853 (wired 2026-10-12): deploy-chain V360's automatic column on
			// credential_probe_queue was never mirrored into the startup chain
			// — no startup/domain migration adds it, yet bg/probe_queue.go's
			// enqueue/claim reference it by name (since e2d3490e38). Fresh
			// installs hit 42703 'column q.automatic does not exist' on every
			// claim scan once 489/490 create the queue. Column type, default
			// and partial index match V360 exactly; deploy-lineage databases
			// ran V360 long ago, so the reconcile is a no-op there.
			"853_credential_probe_queue_automatic.sql",
			// 854 (wired 2026-10-12): 526's session_turns_advisory_lock_key is
			// required by the sessionv2mirror shadow write, but 526 cannot be
			// registered wholesale — its session_turns_with_current_month view
			// definition predates the applied shape and CREATE OR REPLACE VIEW
			// cannot drop columns (the same reason 523 was reconciled as 804
			// instead of registered). Function body matches 526 verbatim;
			// 526-lineage databases already have it, so this is a no-op there.
			"854_session_turns_advisory_lock_key.sql",
			"682_model_offers_context_window_columns.sql",
			// 805 (wired 2026-10-01): session_dim's canonical creators (350/358)
			// are un-registrable wholesale — 350 rewrites update_session_summary()
			// with a 2026-07 body (the 572/563/661 clobber guard) — so the table
			// itself is reconciled here at its production shape, ahead of 683's
			// ownership-column ALTER.
			"805_session_dim_reconcile.sql",
			"683_session_dim_ownership_columns.sql",
			"684_drop_stale_provider_error_tenant_fingerprint.sql",
			"685_task_default_routing_tenant_text.sql",
			"686_fix_session_module_executions_2026_10_bounds.sql",
			"687_fix_473_partition_0800_bounds.sql",
			"688_promote_default_retention_align_go_scheduler.sql",
			"689_candidate_failure_logs_partitions_heap.sql",
			"690_session_summaries_archived_ttl_index.sql",
			"691_proxy_region_policy.sql",
			"692_session_summaries_user_intent_widen.sql",
			"693_provider_models_canonical_cleared_at.sql",
			"694_partition_ensure_timezone.sql",
			"695_request_logs_promote_final_success_self_heal.sql",
			"696_request_logs_view_system_fingerprint.sql",
			"697_request_logs_promote_system_fingerprint.sql",
			"698_promote_hot_partition_timezone_pin.sql",
			"699_supplier_errors_ensure_timezone_pin.sql",
			"700_request_logs_view_raw_model_name.sql",
			"701_credential_balance_floor.sql",
			"703_supplier_errors_promote_timezone_pin.sql",
			"706_session_family_s1a.sql",
			"707_session_turns_s1a.sql",
			// 806 (wired 2026-10-01, mirrors 562): the baseline pre-creates
			// session_bodies_2026_07/08 as columnar; 708's per-partition UPDATE
			// and its recursive unique indexes cannot run against columnar.
			// Empty non-heap partitions are detached and rebuilt as heap with
			// their original bounds.
			"806_session_bodies_partitions_heap.sql",
			"708_session_bodies_s1a.sql",
			"711_hosted_tasks.sql",
			"712_session_mirror_outbox.sql",
			"713_session_turns_cost_precision.sql",
			// R34 (2026-09-17 audit): five-point sync backfill — 704/705/709/
			// 710 drifted out of the installer (R30 leftover #8) and 714/715
			// landed after it; 714 has no Go-side ensure mirror, so the
			// installer was the only fresh-install delivery channel for it.
			"704_plan_quota_probe_backoff.sql",
			"705_request_logs_reattach_detached_partitions.sql",
			"709_work_type_route_coverage.sql",
			"710_request_logs_view_session_family_v2.sql",
			"714_partition_timezone_pin_remaining.sql",
			"715_route_incidents_pending_state.sql",
			"716_unify_probe_health_views.sql",
			"717_request_logs_hot_column_alignment.sql",
			"718_drop_redundant_indexes_and_add_ttl_indexes.sql",
			"719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql",
			// R40 (2026-09-18): five-point sync completion — 720 landed in
			// embeddata only (f5328e13c), leaving TestStartupFilesAreAllEmbedded
			// red on main; registered here with the canonical copy restored.
			"720_rls_policy_vocabulary_unification.sql",
			// 721 (507d78cff) landed with file copies only — same incomplete
			// five-point sync shape as 720; registered here (R40, 2026-09-18).
			"721_credential_balance_source_and_error.sql",
			"722_durable_family_schema_convergence.sql",
			"723_rls_enable_attachments_and_cfl_old.sql",
			// 724 (2026-09-18): taskprofile per-request human task-type
			// corrections (renumbered from a colliding 721 after R40 landed).
			"724_task_type_corrections.sql",
			// 725 (2026-09-18, parallel R41): super_admin_bypass policies for
			// request_logs + tenant_model_policies (Phase 2 prerequisite).
			"725_r41_request_logs_and_tmp_super_admin_bypass.sql",
			// 726 (58384b0d8, 2026-09-18): restore credential_model_index_hot
			// unique index dropped by 718 — without it fresh installs fail
			// auto route rollup with SQLSTATE 42P10 (154 production incident).
			"726_restore_credential_model_index_hot_unique.sql",
			// 730 (R48, 2026-09-20): session role hierarchy — sessions
			// agent_role/parent_session_id/parent_task_id columns +
			// role_task_llm_mapping (role × task_kind → LLM preference) +
			// light-pool tier corrections on provider_models.
			"730_session_role_hierarchy.sql",
			// 731 (R50, 2026-09-21): auto_route_selections role attribution —
			// agent_role/task_kind/routing_source on parent + hot tables so
			// affinity learning can exclude forced role-route selections.
			"731_auto_route_selection_role_attribution.sql",
			// R51 (2026-09-21)：733/734 曾在本列表出现两次——首轮把这一对
			// 注册在 session_turns_hot_bootstrap 之前（错位，依赖 hot 表
			// 存在），后续轮又在 bootstrap 之后补了一对；InitSchema 按序
			// 逐文件 applySQL 不去重，同一迁移会被重复应用。删除 bootstrap
			// 前的错位对，保留下方位置合法的一对；TestStartupFilesHaveNoDuplicates
			// 守门（既有 contains 型测试用 map 记录位置，抓不到列表内重复）。
			// 2026-10-01：bootstrap 本体上移至 478 之前的 head 簇（707 依赖
			// hot 表，见彼处注释），733/734 仍在 bootstrap 之后，约束不变。
			"733_session_turn_details.sql",
			"734_request_logs_view_details_join.sql",
			// 735 (R51, 2026-09-21): models_canonical active 折叠名表达式唯一
			// 索引（R50 F19 对账收口）。fail-closed 前置守卫：active 折叠重复
			// 对未对账时报错指路 sql/fixes/2026-09-20-canonical-dedup-cleanup.sql
			// （CASCADE 引用族裁决不进启动迁移静默选 winner）。幂等。
			"735_models_canonical_active_folded_unique.sql",
			// 736 (Wave 3 B1, 2026-09-22): 峰谷倍率 — 计费倍率列
			// (usage_ledger[_hot].rate_multiplier /
			// request_logs[_hot].credits_rate_multiplier) +
			// maas_resolve_rate_multiplier() 共享取档函数。幂等。
			"736_maas_rate_multiplier.sql",
			// 737 (Wave 3 B8, 2026-09-22): reconciliation findings 表
			// （CREATE TABLE IF NOT EXISTS，幂等）。
			"737_maas_reconciliation_findings.sql",
			// 738 (Wave 3 B1 follow-up, 2026-09-22): view 链补
			// credits_rate_multiplier 列。736 在 request_logs / _hot 加列但
			// 680/717/734 重建的冻结体不会自动补列 → bg/stats_minute_rollup
			// 每分钟 INSERT 抛 "column … does not exist"。补列 + 列数守卫。
			"738_view_chain_credits_rate_multiplier.sql",
			// 739 (R56, 2026-09-23): promote 函数补倍率列。698 的两个
			// promote 显式列清单止于 system_fingerprint / error_kind，
			// 736 加列后热窗转移把倍率证据落 NULL/DEFAULT 1.0。幂等
			//（CREATE OR REPLACE FUNCTION）。
			"739_promote_functions_rate_multiplier.sql",
			// 740 (R57, 2026-09-23): view 链补投影真实 client_ip（R57 B7
			// 数据源级修复）。regexp 补列 + 顶层全量重建 + 列数守卫
			// fail-closed。幂等。
			"740_view_chain_client_ip.sql",
			// 742 (R65, 2026-09-23): hosted_task_events 类型白名单扩
			// 'recalled'（召回轻量快照路径 §3.3/§4.3）。741 已被 B11
			// 申领。幂等（DROP+ADD CONSTRAINT；自注册带 schema_migrations
			// 存在性守卫）。
			"742_hosted_task_recalled_event.sql",
			// 743 (R60, 2026-09-23): providers.protocol 无 CHECK 约束，
			// 存量行残留 "openai-response"（vapeur 事故确切脏值）、"openai"、
			// "anthropic" 等别名拼写。按 provider/catalog
			// NormalizeProviderProtocol 的别名表归一到 catalog 五值枚举
			//（providers + provider_catalog 防御性对账），与 R59 写边界、
			// R60 读面归一配套。幂等（canonical 不是别名 key，重复执行
			// no-op；无法识别的值保持原样）。
			"743_normalize_provider_protocol.sql",
			// 745 (R63, 2026-09-24): report_snapshots 日报快照表（设计预埋，
			// 消费方 worker 尚未实现）。scope×model×day 粒度，UNIQUE 四键
			// 幂等（CREATE TABLE IF NOT EXISTS）。曾死放 migrations/ 顶层
			// 无投递通道，本轮修正结构并补五点同步。
			"745_report_snapshots.sql",
			// 746 (2026-09-25, 对账报表落地轮): report_snapshots 内部对
			// 帐维度补齐 —— tenant_id bigint→text（对齐 usage_facts 文本
			// 租户键，745 建表按 bigint 设计但无写入方，ALTER 安全）+
			// credits_charged/latency_p50_ms/latency_p95_ms 三列 + scope
			// 枚举扩员注记（internal_person/internal_model）。可重入
			//（ALTER TYPE USING text::text 与 ADD COLUMN IF NOT EXISTS）。
			"746_report_snapshots_internal_dims.sql",
			// 747 (2026-09-25, 252 SQL 审计第八轮 D12; 原号 746 与对账报表轮
			// 撞号重编——纪律㉒): session_mirror_outbox source CHECK 扩展
			// 'claim'——final-success claim 同事务补偿登记（telemetry
			// registerFinalSuccessClaimOutbox）的枚举值。
			"747_session_mirror_outbox_source_claim.sql",
			// 748 (2026-09-25, probe-cost-optimization P0-1): 自检系统密钥
			// key_tier 'default'(12 RPM)→'system'(300 RPM) 存量修复——
			// 历史 EnsureSystemAPIKey INSERT 未设 tier，48h 实测 86% 自检
			// 请求被自家网关 RPM 弹回（gw_rpm_exceeded）形成自增强风暴。
			// 幂等（WHERE 全限定，二次执行 0 行）；网关启动侧等价自愈
			// bg.HealSelfCheckSystemKeyTier 双通道兜底。
			"748_selfcheck_system_key_tier.sql",
			// 750 (R68, 2026-09-26): usage_facts 按日分区函数（ensure_
			// usage_facts_daily_partition）+ 当日/次日预建。DEFAULT 分区
			// 保留作历史 catch-all；新一日数据走日分区，partition pruning
			// 对 WHERE 范围查询仅扫命中分区。partition_manager 24h tick
			// 后续按 ensureSpecs 接管当日/次日预建（同源 Asia/Shanghai 日历
			// 钉扎，与 687/694 月分区同款）。CREATE TABLE PARTITION OF 不
			// 需事务（IF NOT EXISTS 幂等），可走 installer；同时登记
			// scripts/apply-db-revision-sequence.sh files=() 让升级通道双
			// 投递（与 745/746/747/748 同族）。TTL 由 owner 拍板后续迁移
			// 处理，本迁移仅建日分区函数不删除任何历史 partition。
			"750_usage_facts_daily_partition.sql",
			// 751 (R69, 2026-09-26): ensure_usage_facts_daily_partition
			// 时区钉扎——ALTER FUNCTION SET timezone 在函数入口生效，
			// 覆盖 750 DECLARE 初始化器里 p_date::timestamptz 的会话时区
			// 依赖（UTC 会话会产出与 Shanghai 日边界错位 8h 的分区窗口；
			// 694 先例的对偶：body 内 SET LOCAL 不覆盖初始器，函数级 SET
			// 覆盖）。幂等 ALTER，不动函数体与分区；boot 链
			// db.ensureUsageFactsDailyPartition 同语句双通道收敛。
			"751_usage_facts_partition_tz_pin.sql",
			// 752 (2026-09-27, mock probe 生产入口收口轮): mock_probe_
			// history 历史表 + 按日分区函数——DDL 原死放 migrations/ 顶层
			// (036) 无投递通道（745 同款病），仅 252 被手工跑过；收编
			// startup 正典通道。相对 036 加固：函数级 SET timezone 钉扎
			// (751 对偶) + move-then-attach (750 同款，DEFAULT 当日行
			// 搬移)。幂等（IF NOT EXISTS / OR REPLACE / pg_inherits
			// 短路），252 存量库重放安全且顺带升级旧版函数。
			"752_mock_probe_history.sql",
			// 753 (2026-09-27, R67 session-storage 审计子任务 2; 语义按
			// 批判式审计修正、R72 审计轮分批): session_turn_logs TTL 清理
			// 函数 cleanup_session_turn_logs_by_ttl(p_ttl_hours,
			// p_batch_size)——保留期由写入方按 settings 烘焙进 expires_at，
			// 函数只做「到期即删」，每调用删一有界批（LIMIT 主键选批）；
			// p_ttl_hours 越界直接 RAISE（fail-closed 联锁，无 GREATEST/
			// NULL 兜底）。幂等（OR REPLACE），仅函数无 DDL 表变更，
			// installer 通道安全。清扫由 bg/partition_manager
			// cleanupSessionTurnLogsByTTL 循环分批调用（settings
			// lifecycle.session_turn_logs_ttl_hours 热加载）。
			"753_session_turn_logs_ttl.sql",
			// 754 (2026-09-27, R67 session-storage 审计子任务 7): request_logs
			// 主表归档函数 archive_request_logs_default(p_retention_days)
			// ——超窗月分区摘要字段落进 request_logs_archive_YYYY_MM，
			// [7,365] 越界 RAISE 联锁；无 DELETE、无 DROP（R68 纪律）。
			// 幂等（OR REPLACE）。R72 审计轮补五点同步（子任务只登记了
			// sequence 通道，canonical 登记守卫对本号必红）。
			"754_archive_request_logs_default.sql",
			// 755 is operator-only: its explicit BEGIN/COMMIT would break the
			// installer's --single-transaction wrapper. Fresh installs do not
			// need to drop a legacy function that was never created there.
			// 756 (2026-09-29, D07 S-01 真库 EXPLAIN): request_logs(id) 索引。
			// 754 的批游标 `WHERE id > :last ORDER BY id LIMIT 1000` 在该表上
			// 没有任何可用索引（无主键，唯一索引仅 (request_id, ts)），每批次
			// 退化为全分区并行顺序扫描 —— 实测 2.1M 行月分区 >24min 未跑完。
			"756_request_logs_id_index.sql",
			// 757/758 repair live read/write contracts. Register both in the
			// fresh-install path as well as the revision sequence for upgrades.
			"757_session_turns_origin_actor_projection.sql",
			"758_routeincident_missing_columns.sql",
			// 759 (2026-09-29, 对帐多维筛选轮): report_snapshots 增加
			// credential_id / api_key_id / person 三列与两个最细粒度 scope
			// 所需的 4 个 partial 索引。没有这三列，凭据 / apikey / 用户
			// 三类筛选在 schema 层就无处可取（不是读面没实现）。
			"759_report_snapshots_grain_dims.sql",
			// 760 (2026-09-30, R27-HC-1/HC-2): analysis_events /
			// stats_event_inbox 终态 TTL 清扫支撑索引（部分索引，仅终态行）。
			"760_analysis_events_inbox_ttl_indexes.sql",
			// 761 (2026-09-30, R28-HC-10): 同步投影行 processing_status
			// 回填——writer 漏翻状态留下的假 pending 历史行一次性修复。
			"761_stats_inbox_sync_status_backfill.sql",
			// 762 (2026-09-30, R32-P-3 / 三十三轮 Track C): session_summaries
			// 项目维度回填链——两级口径解析函数 + session_dim 触发器写链 +
			// 回填扫描索引。只建链不搬数据；存量 33 万行由
			// bg/project_backfill_worker.go 分批回填（共用 sync 函数）。
			"762_session_project_backfill_chain.sql",
			// 763 (2026-09-30, R14 批判式复审 D16): provider_events 契约对齐
			// （序列 + id 默认值 + fail-closed PK + (credential_id, ts) 索引）。
			// installer 的 01-schema.sql 至今把该表建成裸表（id 可空、无默认、
			// 无 PK、无序列），全新安装每次都复现同一漂移——不登记则 fresh install
			// 拿不到契约态。文件已按 --single-transaction 纪律去掉显式事务。
			"763_provider_events_contract.sql",
			// 764 (2026-09-30, R36-B3 / 三十六轮): request_logs 分区家族补
			// (tenant_id, ts DESC) 索引——341 只索引了 hot 侧，分区父表从未
			// 有 tenant 前导索引，tenant 维度 days>7 聚合对每分区全表扫
			//（252-dev 实测 3 行租户 6.5s）。父表 CREATE INDEX 级联全部分区。
			"764_request_logs_tenant_ts_index.sql",
			// 765 (2026-09-30, R16 存储轮): request_logs_bodies 月分区列存化
			//（ensure 函数 columnar 分支 + 存量空分区转换，56.6× 压缩实测）+
			// bodies 两族 lz4 TOAST + request_stage_events 三死索引清理。
			"765_bodies_columnar_storage.sql",
			// 800 (2026-09-24, supplier-protocol-optimization §3.2): 每
			// provider 多端点表 + 从 providers 旧行回填（ON CONFLICT DO
			// NOTHING 幂等）。原 deploy V800 文件从未进任何存量库通道，
			// 本轮移入 startup 并补登记。
			"800_provider_endpoint_protocols.sql",
			// 801 (原 759, 2026-09-29): session_turn_details hot 排水的有界无损
			// 重定义——733 的 parent anti-join 在 backfill 已插入同一 request key 时
			// 会滞留旧 hot 行，其不可达 ON CONFLICT DO UPDATE 还会把 parent-only
			// 补字段擦成 hot 的 NULL。
			//
			// 编号 801：759 已被 upstream 占用（759_report_snapshots_grain_dims，
			// docs/12小时内修订审计-20260929-1635.md §R22-A 订的「先提交方占号」规则）。
			// 位置在 800 之后：本迁移 CREATE OR REPLACE 的目标函数依赖 733 建的
			// session_turn_details 表与其 hot 表，必须晚于 733 生效；旧位置（758 与
			// 760 之间）会让 applySQL 顺序执行时在依赖对象存在前就替换函数。
			"801_session_turn_details_duplicate_drain.sql",
			// 802 (2026-09-30, 会话存储解耦 v3 S4 前置): session_turn_details
			// 族补 (tenant_id, gw_task_id) 部分索引。跨租户访问门
			// assertTaskInTenant 的 session 族母表腿依赖它——缺索引时 EXISTS
			// 判定退化为 167 万行顺序扫描。位置约束：必须晚于 733（建表与
			// 分区）与 801（同族 promote 改版），故排在序列末尾。
			"802_session_turn_details_gw_task_id_index.sql",
			// 803 (2026-10-01, 会话/请求数据存储审计): 删除
			// request_logs_bodies_hot 上被同列 UNIQUE 索引
			// idx_request_logs_bodies_hot_request_id 全量影蔽的普通索引
			// request_logs_bodies_hot_request_id_idx（仅 baseline dump 封存
			// 产物，迁移链与 Go ensure 链均不创建）。唯一侧是 Go
			// ON CONFLICT (request_id) 的承重索引，不可动。
			"807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql",
			// 808 (2026-10-01, Round 44 收口): 补建 request_logs 的 DEFAULT
			// 分区。全新安装上母表只有 2026_07/2026_08 两个月分区、没有任何
			// DEFAULT，于是 ts 落在 2026-08 之外的写入一律 23514
			// "no partition of relation request_logs found for row"——母表从
			// 2026-09 起就写不进任何一行。baseline 是 2026-08-04 的 dump，
			// dump 里只有函数体引用 request_logs_default 而没有对应的
			// CREATE TABLE ... PARTITION OF ... DEFAULT；267 个启动迁移里也
			// 没有一条创建它。它同时是 705 的 repair 骨架与
			// ensure_request_logs_partition / promote_request_logs_default_batch /
			// archive_request_logs_default 四个函数共同依赖的承重对象。
			"808_request_logs_default_partition.sql",
			// 809 (2026-10-01, Round 44 收口): 解除
			// instance_release_status.release_id 的 NOT NULL。376 建表时把它
			// 设成 NOT NULL + REFERENCES releases(id)，而 RecordUpdateReport
			// 在查不到 release 时兜底成 0 —— releases_id_seq 恒从 1 开始，
			// id=0 永远不存在，于是该兜底每次必撞 23503、handler 回 500、
			// 上报丢失。379 当年想把它改成可空（并建了只在可空时才需要的
			// idx_irs_release 偏索引），但 379 的 CREATE TABLE IF NOT EXISTS
			// 对已存在的表是空操作，且它从未注册进 StartupFiles（下限 388），
			// 因此那个意图三处印证都未落地。回滚上报的 ToVersion 是「回滚到
			// 的旧版本」，天然可能没有对应 releases 行 —— 这是真实业务流，
			// 不是夹具。外键保留（外键允许 NULL，非空时引用完整性照旧）。
			"809_instance_release_status_nullable_release_id.sql",
			// 813/814 are wired 2026-10-02 so the chain stops short of the
			// registered set. TestStartupFilesAreAllEmbedded (>=704 floor) was
			// red on main for both: neither is in the psql-concurrency /
			// operator-gated / sequence-channel exemption maps, so the repo's
			// own contract is that they belong in the installer.
			//
			// 813_supplier_errors_partitions_heap — supplier_errors family AM
			// normalisation: the columnar partitions go back to heap and the
			// ensure function is de-columnarised. Its own header records the
			// reason as a dated landmine: "R20 登记的 supplier_errors 90d TTL
			// 是 Row-level DELETE、11-06 必炸". Converting only the partitions
			// without replacing the function leaves the self-heal loop
			// re-applying columnar, so the drift recurs; 813 does both.
			// Depends on 689 (position 126) and 699 (position 136), both
			// already registered, and supersedes 699 at runtime.
			//
			// 814_adaptive_probe_targets_hot_subquery — rebuilds
			// v_adaptive_probe_targets so recent_passive_failures reads the
			// hot twin instead of the partitioned parent. It is a
			// CREATE OR REPLACE VIEW with an unchanged column list, and its
			// dependency candidate_failure_logs_hot is created by 392.
			//
			// Placed at the end of the chain: the 8xx block is ordered by
			// dependency, not by number. Both are idempotent.
			"813_supplier_errors_partitions_heap.sql",
			"814_adaptive_probe_targets_hot_subquery.sql",

			// 815 is wired 2026-10-02. Third instance of the same omission:
			// the migration file was committed in both trees but never
			// registered, so TestStartupFilesAreAllEmbedded was red on main and
			// the fix it carries never ran on a fresh install.
			//
			// What it fixes is a live defect, not hygiene: it adds origin_stage
			// / token_band / client_forwarded_for to the canonical
			// request_logs_with_current_month view (118-column contract).
			// admin/compression_stats.go:212 reads token_band from that view and
			// the column was never in the contract, so every call raised 42703
			// and the error was swallowed by slog.Warn — that dashboard cell
			// has been silently empty. 815 also carries a view-chain guard
			// that RAISE NOTICEs and RETURNs when the 680-incident wrapper
			// shape is absent — but the trailing column-count DO block then
			// runs unconditionally and RAISE EXCEPTIONs (count <> 118), so
			// every "skip" path actually terminates the migration. The
			// no-op-plus-self-heal wording (here and in the skip notices) is
			// therefore false advertising; fail-closed is the real behavior.
			// R32 registers the contradiction (12h 审计 P2-F); aligning the
			// two blocks means editing an applied migration's content, which
			// is a channel-replay decision left to the owner. The data
			// precondition was measured against a real 1,515,960-row
			// request_id pairing, not inferred.
			//
			// It depends on the view chain only, so it sits after 814 at the
			// end of the 8xx block.
			"815_request_logs_view_stage_band_cff.sql",
			// 816 is wired 2026-10-02 (R33 12h audit round; fourth instance
			// of the same omission shape — the migration landed in the
			// canonical tree with no installer five-point sync, leaving
			// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered red
			// on main).
			//
			// It rewrites exactly one line of the 815 view: the session arm's
			// client_ip goes from NULL-padding to a session-side sourced
			// projection guarded by a CASE on the raw text form. Without it a
			// fresh install stays at the 815 shape forever — ensureRequestLogs-
			// CurrentMonthView's early-exit is already satisfied by 815, so
			// nothing converges to 816 — and the client_ip dimension silently
			// collapses to __unknown__ on fresh installs while upgraded
			// databases have the projection.
			"816_request_logs_view_client_ip_projection.sql",
			// 817 (2026-10-02, 审计 §9.64): 把 816 的字符类 client_ip 守卫换成
			// pg_input_is_valid(v,'inet')。816 的守卫只挡非字符集垃圾，
			// 192.168.1 / deadbeef / ::: 全部通过它后死在 ::inet 上，
			// 打挂整条 canonical 视图的每一个读方（本机真库复现）。
			// ensureRequestLogsCurrentMonthView 的早退在 816 形态上就成立，
			// 所以**没有任何自愈通道会把它收敛到 817** —— 不装就永久停在
			// 已知会崩的形态上（与 816 同一形状的第五次遗漏）。
			"817_request_logs_view_client_ip_semantic_guard.sql",
			// 818 (2026-10-02, 存储优化 v2): ursm_node_snapshot_min 的 payload
			// 字段拆分 —— 24 个 hash 键提升为 typed 列，payload 退化为未知字段的
			// 前向兼容仓并剔除 7 个与已有列重复的键。实测该表 payload 占 heap
			// 66%（872 MB/天）；该表 22 GB 占库 56%，且运行时零读方。
			// 刻意不回填：ADD COLUMN nullable 无 DEFAULT 不触发重写，收益随 7 天
			// 保留期自然滚出。
			"818_ursm_snapshot_typed_columns.sql",
			// 821：给 session_turns 补 is_abandoned 标记列（审计 §9.92，
			// 取代被推翻的 819 独立表方案）。母表 + hot 两张都要有——只加一张
			// 会让另一张上的同名列不存在，UPDATE 直接报 42703。
			// 写方（internal/sessionv2mirror 的 markAbandonedTurnIfT0Missing）
			// 紧接在 mirror 写完终态 turn 之后执行，fail-open，故本迁移未应用时
			// 只是标记缺失，不会打挂镜像写链（审计 §9.93）。
			"821_session_turns_abandoned_marker.sql",
			// 822 (2026-10-04, 252 SQL 日志审计 R23): session_summaries 健康分
			// 待评分捞取查询的部分索引 (last_request_at DESC) WHERE health_score
			// IS NULL。bg/session_health_worker（10-02 ece68f148 接入生产）每
			// 60min/实例 tick，无索引支撑时对 58.6 万行表 Parallel Seq Scan +
			// Sort，252 真库实测 25.8-29.4s/次（>1s 慢日志 24h 28 条）。
			// 带 dbinit:no-transaction 标记，走非事务通道。
			"822_session_summaries_health_pending_index.sql",
			// 823 (2026-10-04, 会话族退役审计 §9.155): session_turns.request_status
			// —— 落网关已算好的四态标签 (success|failure|rate_limited|in_progress)。
			// 该标签此前只存在于 telemetry entry，镜像链把它丢了（hook.go 的
			// entryToProcessedRequest 没复制），而 ResolveRequestStatus 永远不产
			// 出 rate_limited ⇒ 会话族无法区分「限流拒绝」与「真实上游失败」。
			// 退役 request_logs 前必须补：394,614 行（连停写窗口 ≈446,819）扫描
			// 噪声已在会话族内，v1 一删即永久失去标签，且 session_* 现有列筛不出
			// 来（最优代理误报 41.2%）。纯加法、无回填。
			// 母表 + hot 两侧必须对称加列 —— promote（707 起）入口强制
			// (列名:类型:非空) 全等契约，不对称会直接 RAISE EXCEPTION。
			"823_session_turns_request_status.sql",
			// 824 (2026-10-04, 会话族退役审计 §9.160): 修 canonical 视图会话腿
			// 的 request_status 分类**从未生效**。那条表达式靠
			// `status_code = 429` 认限流，而 session_turns 全表 1,688,629 行里
			// 429 是 **0 行**（真限流在会话侧记 500）⇒ 437,402 条真限流被报成
			// 普通 failure，视图把 failure 高估 32%、rate_limited 报 0。
			// 改走 `error_kind IN ('rate_limit_exceeded','key_throttled')`
			// （R41 F1：两字面量都来自写侧 EmitRateLimited，单值臂会把 823 后
			// key_throttled 镜像行永久报成 failure）：与 v1 权威标签在
			// 394,614 组孪生行上双向零反例。纯读侧表达式改写，118 列不变，
			// 不碰任何行。注：823 那条 request_status 列**没有**被本迁移引用
			// ——引用它会给「823 未跑的库」引入 undefined column 失败。
			"824_request_status_rate_limited_projection.sql",
			"825_modality_graded_verification.sql",
			"826_model_baseline_price.sql",
			"827_modality_verification_progress_view.sql",
			// ⚠ 834 的编号在 828 之后，但**位置必须在它之前**，而这一点的
			//   依据是本列表的**执行顺序**（applyStartup 按列表顺序跑，不排序）。
			//   823..833 那批是按编号递增排的，所以 835 也在 828 后面 —— 但
			//   835 不依赖这两张表，而 828 的 CREATE VIEW **依赖**它们，
			//   而 CREATE VIEW 是真校验（不像 plpgsql 函数体被
			//   check_function_bodies=off 放过），所以顺序错了就是硬失败。
			//
			//   实测（2026-10-06 集成门）：缺本条时 828 报
			//   `relation "public.supplier_errors_hot" does not exist`，
			//   applied=228 failed=1。
			//
			//   为什么补迁移而不登记 startup_known_gaps.tsv：那份文件的头
			//   记录了上一轮 19 条缺口的修法正是「把缺失的迁移加进
			//   StartupFiles」，登记豁免只是记欠条，不是修。
			"834_supplier_errors_base_tables.sql",
			"828_supplier_errors_unified_tracked.sql",
			"829_bodies_columnar_rollback.sql",
			"831_work_type_route_source.sql",
			"832_model_baseline_observation_health.sql",
			"833_supplier_price_nonneg_check.sql",
			// 835：把多模态定时核实的每一次尝试写进自检台账
			// system_probe_runs（task_type='modality_verify'）。在它之前，核实
			// 循环是唯一一条「在跑但运维查不到」的链路：语义探针经
			// internal/upstreamurl 直连上游、不产生 request_logs，进程内的
			// 尝试台账又随重启清零。落点与理由见该迁移的文件头。
			"835_modality_verify_probe_ledger.sql",
			// 836：重新断言 supplier_errors 族的最终形态。813 的台账 sha 与文件一致
			// ⇒ 幂等通道每次都跳过它，活库的列存漂移因此永远没人修（详见迁移头）。
			// 本条无台账行 ⇒ 每次部署都跑；自身幂等。
			"836_supplier_errors_heap_reassert.sql",
			// 837: 摘掉两个 routing analytics 物化视图里的 NOW() AS refreshed_at，
			// 改由 routing_mv_refresh_state 单行记录刷新时刻。带 NOW() 的列让
			// REFRESH ... CONCURRENTLY 每轮重写 100% 的行（252 实测 100.7%，
			// 真实差异仅 0.339%），是本网关库第 3 大 WAL 生产者（8.98%）。
			"837_routing_mv_refresh_state.sql",
			// 838: analyze_llm_gateway_table_stats 每月每小时重分析「上月」那批
			// 冻结分区（跨月后不再写入、统计量不会失效）。252 实测这批占整趟
			// 逐列统计工作量的 25.7%（§10.92），是网关库当前数据库时间的
			// 第一名（81.0%，约 56 分钟/天）。改为：往月分区仅在
			// pg_statistic 无行（从未被分析过）时才补，当月仍无条件分析。
			"838_analyze_skip_frozen_month.sql",
			// 839: 当月**堆**分区交回 autovacuum，并把它们
			// autovacuum_analyze_scale_factor 降到 0.005。
			// 838 之后手工 pass 仍每小时分析当月分区，占整趟 40.2%（§10.93.5）；
			// 而手工 ANALYZE 会清零 n_mod_since_analyze ⇒ autovacuum 永不就手
			// （生产实测 22/22 个关系该计数全为 0，§10.93.6）。
			// 交回后触发线 = 50 + scale_factor×行数，实测写入 834 行/小时，
			// 最坏间隔从 6h/11h 压到 3h/4h（§10.99）。
			// ★ 列存分区与「从未被分析过」的分区仍必须留在手工 pass：
			//   生产实测列存 autoanalyze_count = 0，autovacuum 不采集。
			"839_autovac_current_month_heap_handoff.sql",
			// 840：analyze 跨实例共享节流槽。§10.53 那把 advisory 锁实测一次都没命中
			// （2026-10-07 今日 32 趟 / 0 次跳过，见 runbook §10.107.6）⇒
			// 「两台各跑一遍」原封不动；这张共享表用原子占槽把 2 次/小时压到 1 次。
			// ⚠️ 「偏移 6 分 01 秒」是旧结论的**错误数字**，已撤回：稳态逐小时实测
			//    偏移约 40 秒。成因仍未解释（runbook §10.107.6）。
			"840_analyze_stats_throttle_slot.sql",
			// 841：月度分区族的分区级 DROP 保留（runbook §10.106.15）。
			// ★ 配置表**建表即空** ⇒ 应用后不删任何东西；
			//   启用需按族 INSERT 保留月数，那是业务决定，不在本迁移内。
			"841_monthly_partition_retention.sql",
			// 842：给 latest_bucket 聚合补 (credential_id, raw_model, bucket) 索引。
			"842_credential_model_index_latest_bucket_idx.sql",
			// 843（并行线）：candidate_failure_logs 两臂补 (ts DESC)，
			// 让无过滤的 max(ts) 从全索引扫描变成读一条索引条目。
			"843_candidate_failure_logs_ts_desc_idx.sql",
			// 844（本会话）：给 v_supplier_price_vs_baseline 补两个缓存基准价的投影。
			// 缺了它们，缓存基准价只在 models_canonical 里躺着，永远进不了
			// supplier_price_drift ⇒ 被 2026-10-07 拍板指定为「合理性下限告警」
			// 依据的四个价里，有两个没有任何监控。
			// ★ 幂等：CREATE OR REPLACE VIEW 只在末尾追加列，
			//   既有列的位置/类型/顺序逐字不变，消费方不受影响。
			// ★ 编号注：本条先撞 842（19:25）重排为 843，再撞 843（20:05）重排为 844。
			"844_supplier_view_cache_baseline_columns.sql",
			// 845（R30，2026-10-09）：stats_event_dedup 补 (event_id, occurred_at)
			// 复合索引，消 stats 日聚合 JOIN 的逐行 heap 回表。
			// ★ 文件带 dbinit:no-transaction 标记（CREATE INDEX CONCURRENTLY
			//   不能进事务块），applySQL 据此走非事务通道，
			//   TestNoTransactionMarkerIsJustified 钉住「标记文件必须真含
			//   不可事务化语句」。
			"845_stats_event_dedup_join_index.sql",
			// 846（R30，2026-10-09）：session_turns 父表 partial 索引
			// (id) WHERE request_status IS NULL，回填 worker 候选捞取的承重索引。
			// ★ 分区父表不能 CONCURRENTLY（R46/843 先例），改普通 CREATE INDEX
			//   IF NOT EXISTS（幂等），ShareLock 随部署窗口；全新安装时表为空，
			//   无代价。
			"846_session_turns_req_status_backfill_idx.sql",
			// 847（multimedia，2026-10-08）：credentials 加 routing_scope
			// （all|text_only|media_only），多模态路由隔离的候选闸。
			"847_credential_routing_scope.sql",
			// 848（multimedia，2026-10-08）：billing_unit（token|image|audio_second）
			// 与 audio_second 计费列骨架。
			"848_billing_unit_and_audio_seconds.sql",
			// 849（R31 SQL 审计，2026-10-10）：session_turns 补 (request_id)
			// 覆盖式部分索引 WHERE request_status IS NULL，remaining-count
			// gauge 的 t 侧走 index-only；事务内 SET LOCAL statement_timeout
			// = '10min' 防部署期 57014（e3e2b4ef5）。
			"849_session_turns_req_status_backfill_cover_idx.sql",
			// 850（R31 SQL 审计，2026-10-10）：session_summaries 补 (created_at,
			// session_key) 部分索引 WHERE gw_project_id IS NULL，支撑
			// project_backfill 候选捞取的 48h 滚动窗，消除每日 ~6,700s 的
			// 「证明无事」空扫。
			"850_session_summaries_project_backfill_recent_idx.sql",
			// 851（2026-10-10）：停用 316 播种的 pii_patterns.ip_address 规则。
			//
			// 为什么必须在这里显式登记：316 是**幂等播种**，全新安装必然把这条
			// 规则种成 enabled=true，而它把每一个 IPv4（含环回、版本号）都当成
			// PII 在**输出侧**单向遮成 `*********`。遮蔽发生在 sanitize 还原
			// **之后**（见 cmd/gateway/goal_control.go 的链序：guard → 还原 →
			// 输出合规），因此模型回显的地址刚被还原成原文就被压成星号，且不可
			// 还原——客户端拿到的是坏掉的运维命令 / 坏掉的工具参数。
			//
			// 这条迁移此前**从未被登记**：文件躺在 sql/migrations/startup/ 里，
			// deploy 的目录扫描会捡到它，但安装器只跑 StartupFiles，于是每台
			// 全新安装的机器上这条规则都还开着。登记它是对「文件存在 ≠ 迁移会
			// 运行」这一形态的修复，不是新增策略。
			"851_disable_overbroad_pii_ip_address_rule.sql",
			// 855（2026-10-12，installer 五点同步 2026-10-13 补登记）：给
			// session-analytics 三兄弟（session_client_stats / session_task_stats /
			// session_client_task_matrix）加 30 天时间窗。deploy 的目录扫描会捡到
			// 这个文件，安装器只跑 StartupFiles，所以全新安装此前建出来的是**没有
			// 时间窗**的旧定义视图——三兄弟每轮重写约 169.3MB WAL、占集群 62~75%，
			// task 维度输出 104,299 组长尾；带窗后 154 实测降到 1,832 组（-98.2%）。
			// ⚠️ 口径变更：报表不再含 30 天以前的会话（owner 已知悉并拍板）。
			//
			// 放在 851 之后（链尾）而不是按编号插位：它读 session_summaries
			// （655/560/677 一族）与 session_dim（805 收编、683 补列），必须排在这
			// 两者定型之后；链尾是唯一无条件成立的位置。文件自带
			// `SET LOCAL statement_timeout = '10min'`（245 首部署实测 65.2s，
			// 服务端 30s 默认值会 57014），且是 DROP IF EXISTS + CREATE MATERIALIZED
			// VIEW，重复应用零行为变化。
			"855_session_analytics_time_window.sql",

			// 856（2026-10-13，origin/main 引入时的五点同步缺口，本合并轮补登记）：
			// 给 usage_ledger / usage_ledger_hot 建 (ts, tenant_id) INCLUDE(...) 覆盖索引，
			// 让「用量」页的 /api/usage/summary 与 /api/usage/by-model 走 index-only scan。
			// ⚠️ 补登记前必须先判「它该不该进 installer」——判据报错文案只说「补五点同步」，
			//   但同族有 6 张豁免表。先例 842 是决定性的：**它同样带显式 BEGIN/COMMIT、
			//   同样建在分区父表上、同样已登记在 StartupFiles** ⇒ 显式事务对这一族不是障碍，
			//   正解是登记而不是豁免。两条 CREATE INDEX IF NOT EXISTS 均幂等。
			"856_usage_ledger_ts_cover_agg_idx.sql",
			// 857（2026-10-13）：analyze_llm_gateway_table_stats 的列存分支加陈旧度闸门。
			// 见 runbook §10.110.14；函数体另有四份副本，migration_857_test.go 把这件事钉成门。
			// ⚠️ 原拟编号 856，与 origin/main 的 856_usage_ledger_ts_cover_agg_idx.sql 撞号，
			//    合并时改号为 857（两侧均未部署，改号无生产代价）。
			// 必须排在 838 → 839 之后且为链尾：它是该函数应有的最终体。
			"857_analyze_columnar_staleness_gate.sql",
			// 858（2026-10-13）：给「会话终态晋级」写路径补 is_final_success 存在性索引。
			// 见 runbook §10.110.16。缺口本体：hot 侧有同形状索引、5 个月度分区全没有，
			// 而这条语句必须逐个探测分区 ⇒ 每次调用摸约 5.1 万个索引条目并逐条回堆
			// （生产实测 mean 987.3 ms / 每次 114,159 页 / 4.06% 全库数据库时间）。
			// 与 842 同款取舍：建在分区父表上由 PG 级联，不加 CONCURRENTLY
			// （CONCURRENTLY 在分区父表上被拒）；窗口内写被阻塞、读不受影响。
			// 非唯一：唯一性仍由 hot 侧的 uq_request_logs_hot_final_success_session 承担。
			"858_request_logs_final_success_gsid_idx.sql",

			// 859（2026-10-13 Vapeur 全量测试轮；原编号 857 与上面的
			// analyze_columnar_staleness_gate 撞号改 859）：收窄 316 播种的
			// pii_patterns.phone_intl——第二段 \d{1,14} 只要求 1 位数字，
			// 算术式 `23*47+100` 的 `+100` 被当国际电话在输出侧遮成
			// [REDACTED:phone]（agent 的 tool 参数被改坏）。收窄为被叫
			// 号码 ≥7 位（E.164 现实区间）；UPDATE 存活 316 幂等重放。
			"859_tighten_pii_phone_intl_regex.sql",
			// 860（2026-10-14 S4 退役轮）：request_logs 族 `_` 前缀改名
			// （待删除标志）+ 26 个 plpgsql 函数体同步改写。视图按 OID
			// 自动跟随；分区/归档子表与索引名不动。回滚 = .down 迁移
			// （仅在族被真正 DROP 前有效）。
			"860_rename_request_logs_family_pending_delete.sql",
			// 861（2026-10-14）：重建 candidate_failure_logs_with_current_month，
			// 让 SELECT * 快照吸收 617 后加的 session_id（否则该列在视图上
			// 42703、/api/candidate-failures 恒 500）。861 = 纯视图重建。
			"861_candidate_failure_view_absorb_session_id.sql",
			// 862（2026-10-14）：session_turns / _request_logs 两分区父表
			// 无谓词 (ts) 前导索引 —— canonical 视图窗口读臂与
			// V1FreezeCutoffSQL 母表臂此前只能整条索引扫（252 实测
			// rollup mean 7.4s、峰值 29.5s 贴 30s 击杀线）。
			"862_partition_family_leading_ts_idx.sql",
			// 863（2026-10-14）：ensure_handoff_logs_partition 存储形态
			// 自适应——挂对父表即合法（columnar 只是优化）；无 pgcolumnar
			// 环境（restore 血缘）建 heap 分区。211 日志审计轮。
			"863_handoff_partition_am_aware.sql",
			// 864（2026-10-15）：model_aliases 裸列 (raw_name, canonical_id)
			// WHERE active 部分索引 —— S3 波 3 alias 链在全部生产读点用裸列
			// 等值，现有 lower(raw_name) 表达式索引与 uq 第二列均不服务，
			// CMI rollup 68/h 被击杀（252 审计轮实测）。
			"864_model_aliases_bare_raw_name_active_idx.sql",
			// Additive repair: stale 708 markers do not prove the body writer schema.
			"865_session_bodies_kind_reconcile.sql",
			// Versioned sanitization policies, review evidence, and instance acknowledgements.
			"866_sanitize_rule_management.sql",
			// 867（2026-10-16）：session_summaries (last_request_at DESC,
			// session_key DESC) 部分索引 —— 在线会话超管路径无租户前导，
			// tenant_time 只能整扫（252 冷缓存 >5s）。已建则整段跳过、不拿锁。
			"867_session_summaries_last_request_at_idx.sql",
			// 868: binding-scoped client transport policy; existing bindings
			// default to both, while Vapeur Claude is non-stream-only.
			"868_credential_model_stream_mode.sql",
			// 869（2026-10-16 retire 轮）：promote_session_turns_hot_to_partition
			// 的目录派生列清单重新确立为本通道终态体。硬编码 54 列清单相对 hot 的
			// 106 列会静默丢弃 52 列（trace_events 等），晋级不报错、计数照常递增。
			// 本通道顺序是 640(391) → 688(477) → 707(493)，707 原本已是末位；
			// 869 排在其后，把「派生清单 + 列形状守卫 + 后置条件」钉成终态，
			// 并在 fresh install 上与存量库（seq 通道，此前 640 为末位）收敛到同一体。
			"869_promote_session_turns_trace_events.sql",
			// 870（2026-10-17）：usage_ledger 月度对账覆盖索引，排在其后不被打断。
			"870_usage_ledger_recon_cover_idx.sql",
			// 871：route_incident_events(created_at) 首列索引。必须 CONCURRENTLY ——
			// 网关持续写该表，普通 CREATE INDEX 的 SHARE 锁会阻塞写入。
			// 该文件带 dbinit:no-transaction 标记，走非事务通道。
			"871_route_incident_events_created_at_idx.sql",
			// 872（2026-10-18）：credential_model_index 月分区 columnar→heap 存量转换。
			"872_credential_model_index_partitions_heap.sql",
			// 873（2026-10-18）：usage_ledger 窗口聚合覆盖索引补齐缓存 token 列。
			"873_usage_ledger_ts_cover_cache_agg_idx.sql",
			// 874（2026-10-18）：session_turns(client_request_id) 部分索引 ——
			// S4 停写后 getLog/unified_detail 的 client_request_id 兜底腿承重。
			"874_session_turns_client_request_id_idx.sql",
			// 875（2026-10-18）：empty_response_rate_5m 补齐为 typed 列 ——
			// 818 把它漏在 payload 里，而 store/pipeline.go:132 一直在读它。
			"875_ursm_snapshot_empty_response_rate_5m.sql",
			// 876（2026-10-18）：删掉三条声明在**分区父表**上、且被同表宽索引
			// 逐列覆盖的窄索引（usage_ledger 的 request_id / ts、sessions 的
			// session_id）。关键在父表上删：ensure_usage_ledger_partition 用的是
			// CREATE TABLE … PARTITION OF，会克隆父表声明的每个索引 ⇒ 只删分区上
			// 的实例，下个月建分区时原样长回来（实测每月约 243 MiB ≈ 2.9 GB/年）。
			// 守卫在迁移文件里**现算覆盖关系**，证不出来就整条回滚。
			"876_drop_prefix_covered_parent_indexes.sql",
			// 877（2026-10-19）：session_turns search_text GIN trigram 索引 ——
			// S4 停写后 admin/logs.go:540 的 `search_text ILIKE '%…%'` 前导
			// 通配符谓词在 session 家族上没有任何可用索引，对每个 6.4GB 级月分区
			// 顺序扫描。补齐 v1 族既有（ensure_request_logs_partition）的 trigram
			// 能力到 session 族。判据是 planner 层（EXPLAIN 出 Bitmap Index
			// Scan），不是「索引存在」——后者在空表上恒真。
			"877_session_turns_search_text_trgm.sql",
			// 878（2026-10-19）：supplier_errors 分区父表 5 条 btree 索引 ——
			// supplier_errors_unified = hot UNION ALL 历史，历史侧是**零索引**的
			// 54MB 月分区，而同一批查询在 hot 侧走 btree（211 acc-pg17 实测：
			// seq_tup_read=69,873,160 / idx_scan 为空，读:写 ≈ 927:1）。
			// 形状照抄 hot 侧那 5 条已验证索引；建在父表上让未来月分区自动继承
			// （ensure_supplier_errors_partition 用 CREATE TABLE … PARTITION OF）。
			"878_supplier_errors_partition_indexes.sql",
			// 879（2026-10-21）：删掉 session_summaries 上与**主键键集逐字重复**的
			// 裸唯一索引 session_summaries_session_key_uidx(UNIQUE(session_key))。
			// 生产两份各 ~67MiB，而该表持续写入 ⇒ 每次写多维护一份 B-tree；
			// 且点查计划器**优先选裸的那条**（它不是没人用的那一份）。
			// 守卫：只有当**还存在别的**可仲裁唯一索引时才删 —— 表没有主键时
			// 裸 uidx 是 ON CONFLICT (session_key) 唯一的仲裁器，删了写入直接失败。
			"879_drop_session_summaries_redundant_session_key_uidx.sql",
			// 880（2026-10-20）：把 session 写路径的**超时预算**在所有环境上对齐到 2000ms。
			// 缺陷：`settings.GetPlatformInt` 的解析顺序是 DB → env → spec.Default →
			// 代码 fallback，`spec.Default`（一直是 500）命中时代码里那个 2000 走不到
			// ⇒ 实际预算 500ms，撑不住一次 shadow write 的串行步骤 ⇒ 正文不落、
			// turn_no 错位、trace 丢失（全是静默失效）。本迁移只抬「已持久化过且
			// 偏小」的值，不覆盖偏大的运维调优值。
			"880_session_write_timeout_budget.sql",
			// 881（2026-10-20）：删掉 injection_attack_vectors 上那条**只写不读**的
			// ivfflat 索引 —— 全仓 `<=>` 的唯一读方打的是 task_type_centroids
			// （另一张表，索引是活的），这张表只有 INSERT 与一个不碰 embedding
			// 的 tenant 列表；而唯一的写入方**根本不写 embedding 列** ⇒ 该列恒为
			// NULL、索引里一个条目都没有，却要在每次写入时付维护成本。
			// 实测（EXPLAIN ANALYZE, BUFFERS，200 行 1536 维向量插入）：
			// 有 ivfflat 3229ms/read=21279，无 ivfflat 1149ms/read=12。
			// 守卫在迁移文件里**现算**：embedding 列有非 NULL 值就拒绝删
			// （= 将来真有向量检索了，逼人重新评估）。
			"881_drop_attack_vectors_embedding_index.sql",
			// 882（2026-10-20）：给 `_request_logs`（860 改名的 request_logs 分区
			// 父表）建 WHERE cost_usd < 0 的部分索引，根修
			// bg/routing_health_checks.go recorded_cost_is_negative 的重扫浪费
			// （R51 §3.5 方案 (a)。252 生产 24h 实测：158 次慢查 / 2,010s +
			// 34 次/天被 statement_timeout 击杀；而源已停写、全表负成本行 = 0）。
			// 建在父表上让未来月分区自动继承；fresh-install 的月分区
			// （01-schema ensure）不带 USING columnar，全 heap，无 AM 拒绝面。
			"882_request_logs_cost_negative_partial_idx.sql",
			// 883：把 auto-route 成本仪表盘的两个**活指标**子查询从 v1 冻结族
			// 切到会话族 —— customer_cost_view.active_concurrent（5 分钟窗，
			// 读冻结族 ⇒ 任何环境都恒 0）与 model_cost_per_task_view.avg_latency_ms
			// （7 天窗 ⇒ 读的是停写那一刻的快照）。两条腿的选型：5 分钟窗用
			// hot 单腿（在线写路径只写 hot，晋级延迟远大于 5 分钟 ⇒ 完整且
			// 无重复；本仓既有先例 SessionFamilyTurnsHotSourceSQL）；7 天窗用
			// hot ∪ parent UNION ALL（跨过 hot 保留期，形态同
			// SessionFamilyTurnsSourceSQL）。⚠ 会话族 api_key_id 是 text 而
			// api_key_model_cost.api_key_id 是 integer ⇒ 必须显式 ::text。
			"883_session_family_cost_view_metrics.sql",
			// 884：把 3 张 **v1 裸母表** 分析视图改读会话族 ——
			// v_continuation_effectiveness / v_node_switch_analysis /
			// v_timeout_effectiveness。它们读的是 `_request_logs`（v1 自
			// 2026-10-13 停写且历史数据已清空 ⇒ 真库恒 0 行、且不报任何错），
			// 同时是「后期删除 _request_logs」的依赖阻塞项。
			// 这 3 张是唯一**没有任何 API/Go 代码消费**的 v1 读面。
			// 6 个细节语义列（is_continuation / cached_response_id /
			// context_size_tokens / node_switch_count / timeout_mode /
			// effective_timeout_seconds）在 `session_turn_details` 上都有
			// （真库 1:1 填满）⇒ 能改读且**不丢能力**；canonical 视图
			// `request_logs_with_current_month` 不投影这 6 列 ⇒ 只能用会话族直连。
			// 两条腿 hot ∪ parent UNION ALL（前提：hot ∩ parent 实测无重叠，
			// 计数型指标不会被数两次）。
			"884_session_family_v_analytics_views.sql",
			// 890：退役 v1 request_logs 族的**最后一步**（真删表）。
			// ① canonical 视图 `request_logs_with_current_month` 去掉第三臂
			//    （冻结 v1 体 + 双反连接去重），只剩两臂 session_turns_hot ∪
			//    session_turns；② 删 4 张 v1-only 视图；③ 删 v1 族全部表
			//    （母表带分区）+ hot 表 + 序列；④ 删只服务 v1 族的分区管理
			//    函数与两个触发器函数。
			// ⚠ 本文件按**两种物理名**都写（`request_logs` 与 `_request_logs`），
			//    因为投递通道对"改名后"的处理**不一致**：部署通道
			//    scripts/apply-db-revision-sequence.sh 会做词法改写，
			//    安装器**不做**（逐字执行）⇒ 两种名字都试，三条通道都正确。
			"890_drop_request_logs_v1_family.sql",
			// 891：给**会话族**建 `WHERE cost_usd < 0` 的部分索引 ——
			// 根修 bg/routing_health_checks.go recorded_cost_is_negative
			// 检查的全表扫描。
			// ★ 起因：890 把 v1 `_request_logs` 族**真删掉** ⇒ **882 给它建
			//   的那条同款索引随表消亡**（索引活不过它的表）；而检查的源
			//   随 890 迁到了会话族 ⇒ 会话族上**一条 cost_usd 索引都没有**。
			// 实测（本地 PG17 真库）：hot 表 Seq Scan，父表每个分区一次
			//   Seq Scan（4 个分区），谓词挂在 Filter 上。
			// 形态：两条部分索引（父表 + hot），键序 (ts, credential_id,
			//   raw_model_name) 服务 30 天窗范围谓词与 GROUP BY，INCLUDE
			//   (cost_usd, cache_read_tokens, prompt_tokens) 让聚合 index-only。
			//   谓词 `cost_usd < 0`；条目数 = 负成本行数（实测 0）⇒ 索引本体几页。
			// 建在父表上 ⇒ 未来月分区自动继承（877 台账口径，882/874 同款）。
			// ★ 顺序前提：两表与 raw_model_name（707 补列）都由启动序列建 ⇒
			//   891 编号在其后，天然排在它们之后。
			"891_session_turns_cost_negative_partial_idx.sql",
			// 892：URSM 快照健康巡检的**每日 slot 汇总表**（P11，§143.35）——
			// 根修「巡检每小时重扫 7 天基线」的成本（§143.29 成本 #1：
			// 每次 2.9 GB 物理读 / 190 s，累计 74 GB）。
			// 形态：表 ursm_snapshot_slot_daily + 幂等填充函数
			//   refresh_ursm_snapshot_slot_daily(days_back, granularity)。
			// ★ 与 891 无关，但编号在它之后 ⇒ 天然排在它后面。
			// ⚠ 依赖：源表 ursm_node_snapshot_min 由 **baseline** sql/schema/01-schema.sql 建
			//   （★ 453_ursm_v2_node_snapshot_min.sql **未在任何通道登记** ⇒ 已被 baseline 吸收；
			//    892 只用 baseline 的 snapshot_ts 一列 ⇒ **不依赖** 818 的 typed 列）
			//   ⇒ baseline 先于任何迁移执行 ⇒ 892 天然在它之后。
			// ★ 不依赖 830（把源表变成分区表的那个迁移）—— 两者可独立授权。
			"892_ursm_snapshot_slot_daily.sql",
			// 885：给 session_turns **父表与 hot 表**各加 host（网关实例
			// 身份）+ tags（轮次级标签数组）两列（审计 A5 缺口：全族此前
			// 无任何主机/实例列，轮次级此前无标签位）。
			// ★ 两侧必须同形：迁移 707 给 promote_session_turns_hot_to_partition
			//   加了列集合契约校验（父表与 hot 的 (列名:类型:非空) 集合必须
			//   全等，否则 promote 当场 RAISE EXCEPTION）⇒ 只加一侧会让
			//   热表永远排不进月分区。
			// promote 函数本身不用改：707 的搬行体是「目录派生列清单 +
			//   RETURNING h.*」⇒ 对称加列自动流经。
			"885_session_turns_host_tags.sql",
			// 886：订正 public.session_bodies 的表注释（「使用 columnar 存储
			// 格式」是**假注释** —— 该族自建表起就是 heap + lz4 TOAST，且因
			// 唯一约束 / ON CONFLICT / UNION ALL 视图族三重互斥而**不可能**
			// 是列存；论证见 765:13-21 与 829）。只改 pg_description，
			// 零锁零 IO；**不改 430 本体**（已 applied + 已记账 + 校验和已登记）。
			"886_session_bodies_storage_comment.sql",
			// 887：给会话族的 search_text 建**全文检索**腿 —— 表达式 GIN 索引
			// `USING gin (to_tsvector('simple', search_text))`。
			// ★ **不得改用生成列**：promote_session_turns_hot_to_partition 会
			//   因为 "cannot insert into a generated column" 报错 ⇒ 热表永远
			//   排不进月分区（887 头部有完整论证）。
			// ★ 含 CREATE INDEX CONCURRENTLY ⇒ 本文件带 dbinit:no-transaction
			//   标记（首行），applySQL 据此不加 --single-transaction。
			// ★ 只给**会话族**建，不给 v1 族（_request_logs）建：顶层 OR 要求
			//   每条参与扫描的关系两条腿都有索引，而 v1 族 2026-10-20 实测 0 行
			//   ⇒ 代价为零（前提与复核条件写在 887 头部）。
			"887_session_turns_search_text_fts.sql",
			// 888：补会话族的 join 索引缺口 —— session_bodies 父表与 hot 表各
			// 补一条 (tenant_id, session_id, turn_no DESC, request_id)，
			// sessions 补一条 (tenant_id, session_id, partition_date DESC)。
			// ★ PG **不支持** ALTER INDEX … ADD COLUMN ⇒ 只能「建新 + 删旧」
			//   （先建后删，无空窗）。
			// ★ 含 CREATE/DROP INDEX CONCURRENTLY ⇒ 带 dbinit:no-transaction。
			"888_session_family_join_index_gaps.sql",
			// 889：裁决 session_turns.tools 死列 —— **填充**（不删列：710/816/817/824
			// 的视图投影 + db/request_logs_view_schema.go 的 sessionFamilyProjection
			// 都引用它，删列会撞依赖）。只订正 4 条列注释（tools × 2、turn_no × 2），
			// 零锁零 IO；写路径的填充在 domains/session/v2/turn_writer.go
			// （turnToolsJSONBExpr，从 session_tools_hot 按 session_id+request_id 聚合）。
			"889_session_turns_tools_population.sql",

			// ★★ 2026-10-21（R60 轮，属主拍板「登记 5 个」）：以下 5 条**此前从未被登记**
			// ⇒ 全新安装出来的库缺 5 个列 ⇒ 其中 provider_models.source 缺了 ⇒
			// **db.Open 的 ensure 链直接 42703** ⇒ 「全新安装 = 装完即炸」
			// （已实测复现 db::TestEnsureAsrCatalogSeed_RealDB 与
			//  sql-migrations-startup::TestMigration715FreshChainApplyMigrations 两条真库门；
			//  后者测的正是真实新装路径 —— 它的链来自 installed_startup_migrations.tsv）。
			//
			// 为什么既有那道空棘轮（sql/schema/startup_known_gaps.tsv，0 known gaps）
			// 看不见这一类：**它数「已登记迁移 failed」，不数「文件从未运行 missing」**
			// ⇒ 缺列时它照样是绿的 ⇒ 现已补
			// sql/schema/fresh_install_column_gaps.tsv（缺列登记册）
			// + sql/schema/fresh_install_column_gaps_test.go（真库门，检新缺口 + 查过期）。
			//
			// 全部是 ADD COLUMN IF NOT EXISTS ⇒ **幂等** ⇒ 与仓内 851/855/856/857
			// 的同形态补登记一致（那四条的注释也写着「文件躺在 sql/migrations/startup/ 里，
			// deploy 的目录扫描会捡到它，但安装器只跑 StartupFiles」）。
			//
			// 位置：**链尾**（不按编号插位）—— 依赖已逐条核过：
			//   361b  目标 provider_models 由 01-schema 基线建好（基线 16 列、无 source）；
			//         已登记链里**没有任何**迁移引用 provider_models.source ⇒ 无前向依赖。
			//   375/376 目标 offline_activation_requests 由基线建好（9 列、无 status/
			//         reject_reason/activation_code）；718 只是**注释**里提到该表名 ⇒ 无依赖。
			//   387  目标 session_embeddings 由 351（链内 index 8）建好 ⇒ 排在它之后即可。
			//   427  目标 task_type_centroids 由 426（链内 index 3）建好 ⇒ 排在它之后即可。
			// 链尾是唯一无条件成立的位置（同 855 的处置）。
			//
			// 361b：provider_models.source 前置补列（NOT NULL DEFAULT 'discovery'）。
			//   原始出处是 domain/361（domain 通道，**安装器不跑**）⇒ 361b 就是为
			//   「只走 startup 通道的库」写的同一句防御性 ALTER（文件头原文如此）。
			"361b_provider_models_source_prelude.sql",
			// 375：给 offline_activation_requests 补 status + reject_reason。
			//   文件头自述「修复 P0: store_pgx.go RejectOfflineRequest() 使用了这两个字段，
			//   但 374 迁移文件中漏掉了」⇒ 本条是 374 的补漏（374 本身也未登记，
			//   但目标表由基线建好 ⇒ 不需要 374 在链里）。
			"375_offline_requests_status.sql",
			// 376：给 offline_activation_requests 补 activation_code（离线激活码，
			//   供客户核对/审计）。
			"376_offline_activation_code.sql",
			// 387：session_embeddings 补 embedding_v2（会话分析推理用的向量列）。
			"387_session_analysis_inference.sql",
			// 427：task_type_centroids 补 embedding_model（任务类型质心用的模型名列）。
			"427_task_type_centroids_model.sql",
			"893_fresh_install_partition_prereq.sql",
			// 894：把 v_candidate_failure_logs_diagnosis **重新绑回活表**
			//   public.candidate_failure_logs（修 392 的改名副作用）。
			//
			//   机制：视图绑的是关系 OID，不是名字 ⇒ 392 把老堆表改名成
			//   candidate_failure_logs_columnar_archive 并新建同名空分区表时，
			//   **既有视图不会跟着换表** ⇒ 它仍绑在老表 OID 上 ⇒ 视图读不到新数据。
			//   实测：211 = 0 行（活表 91,935 行）、新装链终态同样脱靶；
			//   而 252 因 392 走「已分区则跳过」分支没被改名 ⇒ 187,907 行。
			//   ⇒ 同一个视图在两个生产实例返回不同数据集。
			//
			//   视图体与仓内两处真相源（基线 sql/schema/01-schema.sql、
			//   对象源文件 sql/objects/views/v_candidate_failure_logs_diagnosis.sql）
			//   逐字同形 ⇒ 本迁移只把链终态**对齐真相源**，不改语义。
			"894_rebind_candidate_failure_logs_diagnosis_view.sql",
			// 895：给 **11 张 domain 链的表**补 startup 通道镜像
			//   （domain/035/132/133/134/135/220/331 建的那批）。
			//
			//   缺陷本体（实测，非推断）：这 11 张表**只有 domain 通道建**，
			//   而 **domain 链从来没有在生产上跑过** —— 生产只跑 startup
			//   通道 + installer 的 StartupFiles，**domain 通道两者都不含**
			//   ⇒ 生产 252 的 `schema_migrations` 里 domain 链的号**一个都没有**
			//   ⇒ 那 11 张表在生产上**一个都不存在**
			//   ⇒ 而它们**都有 Go 生产读写点**
			//   （domains/credentialstate、domains/session、domains/dbdegradation、
			//     domains/credential、admin/… 等）⇒ 相关功能在生产上 42P01。
			//
			//   逐张核对过**基线**（sql/schema/01-schema.sql）与**全 sql/ 树**：
			//   ★ 这 11 张表**基线一个都没有建** ⇒ 全部要由 895 新建
			//     （基线里的 `client_profiles text[]` / `notification_channels jsonb`
			//      是**别的表上的列名**，不是表本身）。
			//   ★ 同批域链源文件里的**兄弟表不镜像**：
			//     tool_usage_stats / approval_routing_rules / credential_state_log
			//     **已由基线建**（01-schema.sql:17738 / :5550 / :6774）
			//     ⇒ 镜像它们会与基线抢同名对象；
			//     notification_send_log 基线未建但**全仓 Go 零读写点**
			//     ⇒ 不在本条口径内。
			//
			//   形态：**逐字照抄域链源文件**（列/类型/默认值/NOT NULL/CHECK/外键/
			//   唯一约束/主键/索引/注释/序列），并补齐域链没有的依赖：
			//   6 个触发器函数 + 7 个触发器（基线里**一个都没有**）。
			//   幂等：CREATE TABLE/INDEX IF NOT EXISTS + CREATE OR REPLACE FUNCTION
			//   + DROP TRIGGER IF EXISTS + CREATE TRIGGER。
			//
			//   顺序前提：**不依赖任何其它 startup 迁移** —— 外键目标
			//   `public.credentials` 由**基线**建（01-schema.sql:6820）⇒ 生产已存在
			//   ⇒ 链尾即可（与 893/894 同位置理由）。
			"895_domain_chain_startup_images.sql",
			// 896：把 375/376 给 offline_activation_requests 补的三列
			//   （status / reject_reason / activation_code）+ 两索引重新交付到
			//   只靠升级通道维护的存量库。生产通道 deploy-lib/db-changelog.sh 只把
			//   version >= 412 当 pending ⇒ 375/376 永远到不了 252（实测缺列 ⇒
			//   licensing 离线激活管理 SQL 42703）。本链上 375/376 已在前面应用
			//   ⇒ 896 在新装库是 no-op（IF NOT EXISTS）；表不存在时 NOTICE 跳过。
			"896_offline_activation_requests_columns_reassert.sql",
			// 897：给会话族的 hot 排水补上 **final_success 自愈**
			//   （★ 迁移 695 为 v1 request_logs 族做过同款；★ 那个族后来被 890
			//   ★ 退役 ⇒ ⇒ 自愈没有随族迁走 ⇒ ⇒ 真正有同款病灶的会话族一直没有它）。
			//
			//   ★ 病灶（★ 252 共享库 2026-10-23 生产定点实测）：父表与 hot 表各有
			//   同构的部分唯一索引 (tenant_id, session_id, partition_date)
			//   WHERE is_final_success；而 promote 的**去重**键是
			//   (tenant_id, request_id) ⇒ ⇒ 看不见 ⇒ ⇒ 批次里的冲突行撞
			//   uq_session_turns_final_success ⇒ **23505** ⇒ ⇒ 原子 CTE
			//   **整批回滚、零行搬走** ⇒ ⇒ 且批次按 ts 升序 ⇒ 那个坏行每批必含
			//   ⇒ ⇒ **每个 tick 必败** ⇒ ⇒ hot 表无限涨 + 父表停止接收冷行。
			//
			//   ★ 修法：与 695 逐字同款 —— 在**批次选取之前**把「父表已持有该键
			//   mark」的 hot 行降级为 is_final_success=FALSE（语义「输家 = superseded，
			//   历史不改写」⇒ ⇒ 与 695 一致 ⇒ ⇒ 零语义变更）。
			//
			//   ★ 基线是 **869**（★ 现行体）⇒ 函数体整段来自 869、只插入一处 demote
			//   ⇒ ⇒ 并保留 869 的「目录派生列清单 + 形状守卫」两道牙。
			//
			//   ★ 真库实测（一次性库，★ A/B 对照）：★ 无 demote 时同一场景报 23505
			//   （约束名与生产逐字一致）且整批回滚；★ 装上后 demote 1 行、moved=1、
			//   ★ 先到者的 mark 原样保留；★ 幂等（第二次 moved=0）。
			//   ⇒ ★ 本迁移**只落仓**；★ 生产应用需属主另行授权。
			"897_session_turns_promote_final_success_self_heal.sql",
			// 898：补上 public.session_bodies_hot 的 **ts 首列索引**。
			//
			//   ★ 病灶（★ 252 共享库 2026-10-23 生产只读实测）：该 hot 表的
			//   6 个索引中**没有一个以 ts 为首列** ⇒ 批次选取走
			//   **Seq Scan + Sort**（实测 cost 1602~1623）；★ 对照组
			//   session_turns_hot **有** idx_session_turns_hot_ts (ts, id,
			//   partition_date) ⇒ 走 **Index Only Scan**（cost 219~385）。
			//
			//   ★ ★ **交付缺口（带精确失败点）**：614 @ 6973a9fb8（= 生产
			//   schema_migrations 记账时刻 2026-08-29 16:48:05 那一版）把该
			//   索引定义成 partial index 带 `WHERE ts > now() - interval
			//   '8 hours'` 谓词 ⇒ **now() 非 IMMUTABLE** ⇒ PG 拒绝 ⇒
			//   ⇒ **该索引从未建成**，且它**后面**同文件的
			//   idx_session_bodies_hot_partition_date 也从未执行；⇒ 而它
			//   **前面**的 _lookup / _request 建成了 ⇒ ⇒ 与生产现状逐条
			//   吻合。⇒ 2026-09-03 的 368202126 修掉了 now() 谓词，但
			//   **614 的台账已记为 applied ⇒ 修复永远到不了生产**；⇒ 且
			//   614 **不在本通道 files=() 里** ⇒ 通道也不补 ⇒ **双通道都到不了**。
			//   ⇒ ⇒ 这就是「台账说已应用 ≠ 实际对象是那一版」的实例。
			//
			//   ★ 另一条独立通道也拿不到它：707b（**在**通道里）用
			//   `LIKE public.session_bodies INCLUDING ALL` 建 hot 表 ⇒ 索引集
			//   **继承自父表** ⇒ 而父表上**没有** ts 首列索引（862 只给
			//   session_turns / _request_logs 的父表加了 idx_*_ts）⇒
			//   ⇒ LIKE 也拷不出 ts 索引 ⇒ 必须有一条新迁移直接建它。
			//
			//   ★ 形态：照抄仓内样板 idx_session_turns_hot_ts ⇒ 建
			//   (ts, id, partition_date) 三列（hot 表有 id 列，614 第 8 行）。
			//   ★ 用 CONCURRENTLY（888 第 2 段同款）：hot 表是**活写表**
			//   （bodies_writer 持续写入，pg_stat_statements 实测 72,813 次
			//   INSERT / mean 20.2ms）⇒ 普通 CREATE INDEX 会在构建期持
			//   ShareLock 阻断写入。
			//
			//   ⚠ **诚实边界**：本索引**不**是 60s 超时的解。★ 2026-10-23
			//   实测批次选取只花 **30ms**（Sort quicksort 4198kB，**未溢写**
			//   ⇒ ★ 早前「12.29× 溢写」的结论是量具口径错，已作废）；
			//   ★ 60s 的去向在**写侧**（实测 ≈79ms/行：154 日志 511 行耗
			//   40.45s）。⇒ 本迁移消除的是「批次选取随表体量线性劣化」这个
			//   **放大器**，是前置条件而非 60s 的解。
			//   ⇒ ★ 本迁移**只落仓**；★ 生产应用需属主另行授权。
			"898_session_bodies_hot_ts_index.sql",
			// 899：把 promote_session_bodies_hot_to_partition 的**生产现行体**
			//   **下沉成迁移**（★ session_bodies 族的 copy-gap 修复）。
			//
			//   ★ 病灶（★ 2026-10-23 生产只读实测 prosrc 指纹）：活库上装的是
			//   **裸 ON CONFLICT DO NOTHING**（无仲裁目标）+ reconciled CTE +
			//   形状守卫 + try-lock 的形态（src_len=3486）⇒ 这个形态**逐字
			//   等于** db/db.go 的 ensureSessionBodiesPromoteDrain
			//   （db/db.go:3984-4095，启动链 db/db.go:527 ⇒ **每次网关启动
			//   都重装一次**）⇒ ⇒ **它只活在 Go 源码里，不在任何迁移里**。
			//   ⇒ 而仓内四版迁移（615/626/638/708）**全是**带仲裁目标的
			//   `ON CONFLICT (id, partition_date) DO NOTHING` ⇒ ⇒
			//   **「只跑迁移不启 Go」会把 708 的形态装回生产** ⇒ ⇒ 而
			//   708 的仲裁目标**不覆盖**父表的
			//   session_bodies_tenant_request_partition_key UNIQUE
			//   (tenant_id, request_id, partition_date) ⇒ ⇒ **23505 复发 +
			//   毒丸行卡死 hot 窗口**（2026-09-17 PG 日志审计 65 轮实测，
			//   逐字记录在 db/db.go:3965-3972）⇒ ⇒ **修法只活在 Go 侧 ⇒
			//   修法在通道路径上无效**。
			//
			//   ★ 函数体**逐字等于生产现行体**（纪律同 897：基线必须是生产
			//   现行体）⇒ ⇒ **语义零变更**；本迁移只做一件事：把这个形态从
			//   「只活在 Go 源码里」变成「也活在迁移里」。
			//   ★ 签名不变 ⇒ CREATE OR REPLACE 是替换、不是新增 ⇒ 无重载
			//   残留、调用方零改动。
			//   ⇒ ★ 本迁移**只落仓**；★ 生产应用需属主另行授权。
			"899_session_bodies_promote_body_sink.sql",
			// 900：把 FreeDiscovery 数据模型（084 + 087 的列）**再投递**给
			//   升级通道库与新装链（896 同款交付缺口修复）。
			//
			//   ★ 交付缺口：升级通道只把 version ≥ 412 视为 pending ⇒
			//   084（<412）从未到达任何生产库（db-changelog 084 行自证
			//   「applied+verified (本地 llm-gateway 库, psql 手动应用)」）。
			//   ⇒ 生产共享库实测无 provider_templates / discovery_tasks /
			//   discovery_results ⇒ R39 的 ProviderTemplatesProvisioned()=false
			//   ⇒ /api/free-discovery/* 一律 503 "database disabled"
			//   （245 journal 逐字佐证，2026-10-23）。
			//   ★ 形态：逐字复用 084 的幂等 DDL + 并入 087 三个健康反馈列；
			//   新装链此前同样缺这三张表 ⇒ 本迁移同时补齐新装路径；
			//   升级库全 no-op（IF NOT EXISTS）。
			"900_freediscovery_schema_reassert.sql",
			// 901：给分区父表 public.usage_ledger 补
			//   (api_key_id, ts DESC) WHERE api_key_id IS NOT NULL 部分索引。
			//
			//   ★ 既有四组分区索引首列全是 request_id / tenant_id / ts，没有
			//   api_key_id ⇒ /api/usage/{id} 三端点的按 key 聚合只能走 ts
			//   首列索引 + Filter ⇒ EXPLAIN ANALYZE 实测「7 天窗口 0 行」耗
			//   6,983 ms（key 73 空窗）⇒ keys 详情页使用统计在负载窗口整体
			//   超时（2026-10-23 生产只读实测）。
			//   ★ 形态照抄 hot 侧样板 usage_ledger_hot_api_key_id_ts_idx；
			//   普通 CREATE INDEX（父表不支持 CONCURRENTLY）+ lock_timeout=60s
			//   / statement_timeout=10min（891 同款）。
			"901_usage_ledger_api_key_id_ts_idx.sql",
			// 902：stats_event_dedup 索引合并 —— pkey 换体
			//   UNIQUE(event_id) INCLUDE(occurred_at)，删除 845 所建的
			//   (event_id, occurred_at) 复合索引（252 实测 151 MB）。
			//
			//   ★ 冗余证明：pkey(event_id) 保证全表 event_id 唯一 ⇒ 复合键
			//   第二列从无「同 event_id 第二行」可排序；该复合索引相对 pkey
			//   的唯一增值是 covering ⇒ INCLUDE 体 pkey 完整承接。
			//   ★ 消费方逐个过（本地一次性库实测）：ON CONFLICT (event_id)
			//   仍工作（INCLUDE 不参与推断）、rollup/reconciliation 四处
			//   双列等值 JOIN 仍 Index Only、唯一性不变式仍强制。
			//   ★ 252 净省 ≈114 MB（旧索引 265 MB → 单条 ~151 MB；表 409 → ~295 MB）。
			//   幂等：换体段按
			//   目录态判定 + 兜底 DROP tmp（无守卫重跑会留 20 MB 孤儿，
			//   本地实验阶段 4 抓出后补）。dbinit:no-transaction 标记
			//   （CREATE INDEX CONCURRENTLY）。
			"902_stats_event_dedup_pkey_include.sql",
			// 903：public.sessions 补 (updated_at DESC, session_id DESC) 索引。
			//
			//   ★ 根因：会话列表（admin/turns_sessions.go）的排序键
			//   (updated_at DESC, session_id DESC) **没有任何索引能提供该序**
			//   ⇒ 规划器只能 Seq Scan + top-N Sort ⇒ Sort 必须把 439,106 行
			//   全部物化才能吐出第一行 ⇒ Limit 无法提前停 ⇒ 整条 JOIN 链跑满。
			//
			//   ★ 252 EXPLAIN ANALYZE 实测（生产只读）：原查询
			//   32,089.911 ms；只加本索引（事务内建索引后 ROLLBACK）⇒
			//   3.639 ms，shared hit 9,876,509 → 640，去重 SubPlan
			//   439,570 次循环 → 22 次。
			//
			//   ★ 口径零变化：同一 REPEATABLE READ 快照内，生产查询与改写
			//   形态各跑一次，两个结果集 22 行（含表头）**逐字一致**。
			//
			//   ★ 普通 CREATE INDEX（分区父表不支持 CONCURRENTLY）+
			//   lock_timeout=60s / statement_timeout=10min（891/901 同款）。
			//   public.sessions 是热写表（session_aggregator.go:216 每请求
			//   一次 upsert）⇒ 构建期 SHARE 锁阻塞该分区写 ⇒ 落在低峰窗口。
			"903_sessions_updated_at_session_id_index.sql",
			// 904：inspector-stats 的 L2/L3 两条聚合腿补部分索引。
			//
			//   ★ 病灶（252 生产只读实测，2026-10-27）：
			//   GET /api/admin/sessions/inspector-stats 的两条聚合腿
			//   **都没有时间窗** ⇒ 都是全表聚合：
			//     L3 `status='closed' AND closed_at >= CURRENT_DATE`
			//        → 全扫 90 万行 session_dim 再丢掉 100%
			//          （Rows Removed by Filter: 301,024）
			//     L2 `AVG(health_score) WHERE health_score IS NOT NULL`
			//        → 全扫 86 万行 session_summaries 再丢掉 55 万行
			//          （Rows Removed by Filter: 184,361）
			//
			//   ★ 根因：session_dim 的 7 条既有索引**没有一条含 closed_at**；
			//   session_summaries 的 19 条里只有 health_score IS **NULL** 侧
			//   的 idx_session_summaries_health_pending ⇒ NOT NULL 侧
			//   **无索引可承接** ⇒ 规划器只能 Seq Scan + Filter。
			//
			//   ★ 修复效果（事务内建索引 + EXPLAIN ANALYZE + ROLLBACK，
			//   生产零持久变更；回滚后残留索引实测 = 0）：
			//     L2 389.373 ms → **119.706 ms**（→ Parallel Index Only Scan）
			//     L3 195.432 ms → **0.100 ms**  （→ Index Only Scan）
			//     索引体积 8,192 B + 2,120 kB
			//
			//   ★ 口径零变化：本迁移**不改任何查询**，只加索引
			//   ⇒ 结果集取值不变（构造性成立，不依赖快照比对）。
			//
			//   ★ 两张表都**不是分区表**（relkind='r'）⇒ 可用普通
			//   CREATE INDEX；仍不用 CONCURRENTLY（startup 迁移默认在
			//   事务里跑）。两张都是热写表 ⇒ 构建期持 SHARE 锁阻塞
			//   该表写入 ⇒ 落在低峰窗口。普通 CREATE INDEX +
			//   lock_timeout=60s / statement_timeout=10min（903 同款）。
			"904_inspector_stats_partial_indexes.sql",
			// 905：`?model=` 这类**稀有**轮次级过滤补 model 维度复合索引。
			//
			//   ★ 病灶（252 生产只读实测，2026-10-24）：
			//   GET /api/admin/turns/sessions 带 `?model=` 时**恒 500@30s**。
			//   build 2841（R90 的两腿 EXISTS 已上线）直连 REPEAT=3：
			//     ?limit=20                       200 200 200   0.878 0.155 0.111 s
			//     ?limit=20&api_key_id=105        200 200 200   9.576 0.216 0.151 s
			//     ?limit=20&model=deepseek-ai%2F…  500 500 500  30.008 30.007 30.018 s
			//     ?limit=20&provider=14           200 200 200   0.358 0.100 0.080 s
			//     ?limit=20&status_code=500       200 200 200   0.554 0.181 0.153 s
			//   ⇒ ★★ R90「四条全修」的结论被证伪（只有 model 那条仍 500）。
			//
			//   ★ 真因不是「形状」而是**稀有过滤值 ⇒ 外层会话深扫**：
			//   该 model 命中 458 轮 / **30 个会话**，而 public.sessions
			//   有 441,112 个会话；列表是 `ORDER BY s.updated_at DESC
			//   LIMIT 21` ⇒ 必须扫到凑够 21 行 ⇒ EXPLAIN 实测外层
			//   Rows Removed by Filter: 72,455 + 277,899 = **350,354**。
			//
			//   ★ 修复效果（事务内建索引 + EXPLAIN ANALYZE + ROLLBACK，
			//   生产零持久变更；回滚后残留索引实测 = 0）：
			//     model（稀有） 30,008 ms → **8.0 ms**
			//     provider='14'    358 ms → **48.6 ms**
			//     status_code=500  554 ms → **249.6 ms**
			//
			//   ★ 只给 model 建索引：provider n_distinct=20、
			//   status_code n_distinct=8 ⇒ **没有选择性** ⇒ 给它们建索引
			//   会让规划器更倾向「轮次驱动 + 大哈希构建」⇒ 实测退化到
			//   **10,080 ms**。
			//
			//   ★ public.session_turns 是**分区热写表**（父表 4.7 GB /
			//   1,014,911 行）⇒ 分区父表不支持 CONCURRENTLY ⇒ 普通
			//   CREATE INDEX + lock_timeout=60s / statement_timeout=10min
			//   （903/904 同款）。实测构建 **18,512 ms**（父表）+ 28 ms（hot）
			//   ⇒ ★ 这 ~18.5 秒里 session_turns 的写入被 SHARE 锁阻塞
			//   ⇒ 落在低峰窗口。
			"905_turns_session_model_index.sql",
			// ★ R94：给「会话列表的 api_key 回填」补**部分索引**。
			//   探针会话（sys:probe:*）的 api_key_id 全是 NULL ⇒ 过滤条件
			//   无法从索引判定 ⇒ 必须回表 ⇒ 实测扫 82,297 行堆、全部被过滤掉、
			//   产出 0 行，却花了 1,350~1,569 ms。
			//   部分索引只含「api_key_id 是数字」的行 ⇒ 探针流量在索引里一行
			//   都没有 ⇒ Index Only Scan ⇒ 实测 **4.57 ms**（295×）。
			//   ★ 谓词必须与 db.SessionFamilyAPIKeyIDNumericPredExpr 逐字一致
			//   （PG 的谓词蕴含判定是语法的 ⇒ 写成 CASE 形式就认不出来）。
			//   实测构建 6,026 ms（父表）+ 21 ms（hot）。
			"906_turns_api_key_numeric_partial_index.sql",
			// ★ R96：routing_analytics_source 换成「会话族两腿 + LATERAL 回定位」
			//   ⇒ 消除刷新链里 45 万行 session_turn_details 顺序扫
			//   （252 实测中位 4,482.8 ms → 2,270.8 ms；最坏一轮 20,378.6 → 1,917.1 ms）。
			//   ★ 口径逐行等价（源层/聚合层双向 EXCEPT ALL 均 0/0）。
			//   ★ 零 DDL：只做 CREATE OR REPLACE VIEW（保留 OID ⇒ matview 依赖不动，
			//     下一次 REFRESH 自动采用新定义 ⇒ **不需要重建 matview**）。
			"907_routing_analytics_source_lateral.sql",
			// 908：删除 route_incident_events 的零消费方索引
			//   idx_route_incident_events_type_created (event_type,
			//   created_at DESC)（252 实测 110 MB / idx_scan=1，5.2 天窗口）。
			//
			//   ★ 判决链：全仓 Go 代码无任何 event_type 谓词查询本表；
			//   历史上唯一使用者（30 天留存 DELETE）已被 871 判为
			//   「非首列条件 ⇒ 全索引扫描」的坏计划并被 created_at
			//   首列索引取代；不背书任何约束（pg_namespace 实查 0 行）。
			//   ★ 写路径热度：同表 uq_idem 5.2 天 42 万次扫描 ⇒
			//   每次写入白维护一个零读者的 110 MB btree。
			//   ★ 配套：db/db.go ensureRouteIncidentSchema 同批删除同名
			//   CREATE INDEX（否则 ensure 链会把本迁移刚删的索引复活）。
			//   baseline 刻意保留（718 先例：fresh-install 链建出→本条删掉）。
			//   dbinit:no-transaction（DROP INDEX CONCURRENTLY）。
			"908_route_incident_events_type_created_drop.sql",
			//   909：给 session_summaries 的三列（session_key / title / primary_model）
			//   补 GIN trigram 索引。★ 修的是 admin 会话搜索（catalogSearchSQL）
			//   对 868,711 行做三列 OR 的 `%q%` ILIKE ⇒ Parallel Seq Scan
			//   ⇒ 252 生产实测 Execution Time = 40,415.968 ms（40.4 秒）
			//   ⇒ 而调用点 ctx 超时只有 2 秒 ⇒ ★★ 每次都超时 ⇒ 静默返回「没搜到」。
			//   ★ Go 侧同步把谓词改成裸列（title ILIKE），★ 因为 PG 的表达式索引
			//   匹配是逐字的 ⇒ COALESCE(title,'') ILIKE 用不上裸列索引。
			//   dbinit:no-transaction（CREATE INDEX CONCURRENTLY）。
			"909_session_summaries_search_trgm_indexes.sql",
			// ★ R166（迁移 910）：routing_analytics_source 父表腿从 LATERAL
			//   逐行回定位换成「普通 LEFT JOIN + partition_date 日界下推」
			//   ⇒ details 侧执行期分区裁剪 + Hash Join 可选，消掉 ~2.75 万次/刷新
			//   的逐行探针（252 实测整计划 buffers 147K → 52K，-64%）。
			//   ★ 口径逐行等价（窗口内父表腿双向 EXCEPT ALL 0/0 @66,838 行；
			//     ukey 唯一键 + 错位行 0 实测）。
			//   ★ 零 DDL：只做 CREATE OR REPLACE VIEW（保留 OID ⇒ matview 依赖不动，
			//     下一次 REFRESH 自动采用新定义 ⇒ **不需要重建 matview**）。
			"910_routing_analytics_source_hashjoin.sql",
			// 911：本地音频（ASR/TTS）供应商 catalog 三条目（whisper-cpp /
			//   whisper-cpp-large / macos-say），2026-10-26 多模态标准接口轮。
			//   ★ 合并时与上游 R166 的迁移 910 撞号 ⇒ 本条让位改为 911。
			//   纯 INSERT ON CONFLICT，幂等；不 seed providers 行（运营经
			//   admin API 创建，catalog kind='local' 自动建占位凭据）。
			"911_local_audio_catalog.sql",
			// 912：W1.5 session_bodies trash + session_hidden（录制同意轮）
			"912_session_recording_trash.sql",
			// 913: monthly bills + coupons + invoice placeholder (pkg 07).
			"913_maas_monthly_bills_coupons.sql",
			// 914: 补回会话族 origin_stage 的 DB 级 CHECK。
			// v1 族退役时那条 origin_stage 的 DB 级约束一起消失了，会话族从未重建
			// ⇒ session_turns / session_turns_hot 上**无任何约束**
			// ⇒ 「探针流量排除」只剩 Go 侧闸门，DB 级兜底消失。
			// 允许集 = internal/probekind.ValidStages()（8 个探针 stage + 'business'）；
			// 现役约束名 session_turns_origin_stage_check（父表）
			// / session_turns_hot_origin_stage_check（hot 表）。
			"914_session_turns_origin_stage_check.sql",
			// 915: recent_cache_hit_rate — P1-B-③（audit 2026-10-28 §4）路由侧
			//   prompt-cache 亲和信号。(credential, model) 最近 N 条业务行的
			//   token 加权 cache_read/prompt 占比，形态与探针排除同 428 的
			//   recent_success_rate。纯 CREATE OR REPLACE FUNCTION，幂等，
			//   零 DDL 风险；评分侧把 NULL/低样本当「无信号 → 中性」fail-open。
			"915_recent_cache_hit_rate.sql",
			// 916: 会话族 api_key_id **归一表达式**的 (CASE, ts DESC) 索引。
			// ★ 缺口本体（R177 轮，252 生产只读实测）：按 api_key 过滤会话的读点
			//   （admin/usage.go:841-859，7 天窗）实测「窗口内 75,449 行全读、
			//   丢掉 37,825 行」，一次读点 220,401 块 ⇒ **一半的行是白读的**。
			// ★ 为什么 906 那条部分索引承不住：它的**键是 (session_id, turn_no DESC)**，
			//   api_key_id 只在**谓词**里 ⇒ 它承的是「按 session_id 找轮次」。
			// ★ 为什么必须是【表达式索引】：canonical 视图把 api_key_id 投影成
			//   CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END
			//   （唯一真值源 db/request_logs_view_schema.go 的 SessionFamilyAPIKeyIDExpr）
			//   ⇒ 普通 btree 用不上；**部分索引也用不上**（PG 的谓词蕴含是语法的）。
			// ★ 含 CREATE INDEX CONCURRENTLY ⇒ 本文件带 dbinit:no-transaction 标记（首行），
			//   applySQL 据此不加 --single-transaction（887/888/729 同款）。
			"916_session_turns_api_key_ts_index.sql",
			// 917: 拦截审核/放行/硬阻断状态的持久化表（拦截持久化轮，2026-10-29）。
			//   纯新增三表 + 索引，全部 IF NOT EXISTS ⇒ 幂等、零锁面风险。
			"917_sanitize_block_reviews.sql",
			// ★ 919（R176 轮）：P11 第二期 —— slot_daily.active_minutes + 填充函数升级
			//   （巡检 base_active 走汇总表的落点）。无 CONCURRENTLY ⇒ 事务通道。
			//   ★ 让号记录：初拟 917，push 前 fetch 实测 origin 已被并行会话
			//   「拦截审核与放行落库」（ddd762e7a）占用 ⇒ 重取 919/920。
			"919_ursm_slot_daily_active_minutes.sql",
			// ★ 920（R176 轮）：node_probe_runs (credential_id, completed_at) 覆盖索引
			//   （INV-4 双模型探测成功门的 24h 关联子查询，252 实测 549 次/天 慢+取消）。
			//   ★ 含 CREATE INDEX CONCURRENTLY ⇒ 首行 dbinit:no-transaction 标记。
			"920_node_probe_runs_cred_completed_idx.sql",
			// ★ 921（2026-10-29 输出合规误报轮）：拦截审核工作台 —— 审核单追加
			//   session_id / label 四件套 + 证据表 sanitize_block_evidence（原始
			//   会话内容的有界摘录，7 天 TTL）+ 放行来源扩 'auto_label'。
			"921_sanitize_block_workbench.sql",
			// ★ 922（2026-10-29 模型可信度轮）：模型可信度（防掺水）体系 ——
			//   provider_model_credibility（当前判定）+ provider_model_credibility_runs
			//   （判定历史）+ 补齐 provider_quality_profiles 的 reliability_score /
			//   calculated_at（435 遗漏 ⇒ 全新安装缺列 ⇒ internal/quality INSERT 42703）。
			//   纯新增两表 + 幂等补列，全部 IF NOT EXISTS ⇒ 无 CONCURRENTLY，走事务通道。
			"922_model_credibility.sql",
			// ★ 923（2026-10-29 记账补齐轮）：把 900~920 这 20 条迁移补登记进
			//   public.schema_migrations（人读台账）—— ★ **只补记账，不改 schema**。
			//   缺口本体：两个台账记的不是同一段 —— 部署通道的权威台账
			//   public.gateway_db_revision_sequences 里 900~920 全在（本地真库实测
			//   287 行），而给人读的 public.schema_migrations 只到 899（74 行）
			//   ⇒ 运维读它会把「已应用」误判成「待应用」。
			//   ★ 为什么排在这里：它是**记账**动作，必须排在 900~920 全部投递之后
			//   （排在前面会给还没投递的库写上「已应用」的假账）；
			//   ★ 幂等：ON CONFLICT (version) DO NOTHING（version 上有主键），
			//     重复执行不写重行、也不覆盖运维手改过的 description；
			//   ★ 纯 DML ⇒ 不拿强锁、无 CONCURRENTLY ⇒ 走事务通道。
			"923_schema_migrations_ledger_backfill.sql",
			// ★ 924（2026-10-30 R184 轮）：credential_probe_queue
			//   (probe_command, tenant_id, credential_id, raw_model, created_at)
			//   复合索引，承接 integrity_probe_planner 的 NOT EXISTS 反连接
			//   （252 实测 132 次/24h 1,369.7s 恒定在榜、单次 15,455 buffers 全量
			//   过队、产出恒 0 行）。★ 含 CREATE INDEX CONCURRENTLY ⇒ 文件首行带
			//   dbinit:no-transaction 标记 ⇒ applySQL 走非事务通道。
			"924_credential_probe_queue_command_key_idx.sql",
			// ★ 926（2026-10-30 输出合规审计轮；★ 让号记录：925 曾被
			//   session_bodies 叶 TRP 索引 heal 占用后撤销，db-changelog 留有
			//   记录 ⇒ 按「后到者取下一个空闲号」取 926）：拦截审核单区分
			//   「降级放行」与「硬阻断」—— sanitize_block_reviews 末尾追加
			//   degraded BOOLEAN（921 把守卫缓冲越界的降级放行也登记成 pending
			//   拦截单 ⇒ 211 实测 24h 内 226/226 条全是降级单灌爆审核面），
			//   并回填 builtin.guard.% 存量行。
			"926_sanitize_block_review_degraded.sql",
			// ★ 927（2026-10-31 12h 审计轮）：routing_policy 单例化 ——
			//   去重（每 tenant_id 只留一行；内容不一致 ⇒ 报错不猜）
			//   + UNIQUE (tenant_id) 约束 ⇒ ★ 02-seed.sql 的 ON CONFLICT
			//   DO NOTHING 第一次真正生效 ⇒ 后续安装不再堆重复行
			//   （★ 根因：表无任何唯一约束 ⇒ 每次安装插一行 id=1
			//   ⇒ 211 生产已堆到 4 行 ⇒ CROSS JOIN 把 targets 放大 4 倍）。
			//   ★ 含 DELETE + ADD CONSTRAINT ⇒ 自带 lock_timeout / statement_timeout。
			"927_routing_policy_singleton_unique.sql",
		},
	}
}

// WaitForPG 等待 PG 就绪（最多 timeout 秒）
func (r *Runner) WaitForPG(timeout int) error {
	for i := 0; i < timeout; i++ {
		cmd := exec.Command("docker", "exec", r.CitusContainer, "pg_isready", "-U", r.DBUser)
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("PostgreSQL 启动超时 (%ds)", timeout)
}

// InitSchema 应用 3 个 SQL 文件（00-prereqs → 01-schema → 02-seed）
func (r *Runner) InitSchema(logger func(string)) error {
	files := []struct {
		name string
		desc string
	}{
		{"00-prereqs.sql", "扩展依赖"},
		{"01-schema.sql", "完整 schema"},
		{"02-seed.sql", "配置字典数据"},
	}

	for _, f := range files {
		logger(fmt.Sprintf("  ▶ 应用 %s (%s) ...", f.name, f.desc))
		if err := r.applySQL(f.name); err != nil {
			return fmt.Errorf("应用 %s 失败: %w", f.name, err)
		}
		logger(fmt.Sprintf("  ✅ %s 完成", f.name))
	}
	for _, name := range r.StartupFiles {
		logger(fmt.Sprintf("  ▶ 应用 startup migration %s ...", name))
		if err := r.applySQL(filepath.Join("startup", name)); err != nil {
			return fmt.Errorf("应用 startup migration %s 失败: %w", name, err)
		}
		logger(fmt.Sprintf("  ✅ startup migration %s 完成", name))
	}
	return nil
}

// noTransactionMarker 是逐文件事务豁免标记。
//
// applySQL 默认对每个文件加 --single-transaction（全文件原子）。但
// PostgreSQL 有若干语句**不能**在事务块内执行（DDL 之外的一类），
// 例如 DROP INDEX CONCURRENTLY / CREATE INDEX CONCURRENTLY /
// REINDEX CONCURRENTLY / VACUUM / CREATE DATABASE。
//
// 携带本标记（独立注释行，允许前导空白与额外 --）的迁移改走非事务通道。
// 标记写进文件本身（而非 Go 侧名单），使它随迁移一起分发到三份副本，
// 与仓库既有 sqlreadguard:allow 行级标记同一约定。
//
// 代价：非事务通道下失败会留下部分已应用的语句。因此豁免必须尽量窄，
// TestNoTransactionMarkerIsJustified 用「标记文件必须真的含有不可事务化
// 语句」这条真实不变式防止它扩散。
const noTransactionMarker = "dbinit:no-transaction"

// requiresNoTransaction 报告 SQL 内容是否声明了逐文件事务豁免。
func requiresNoTransaction(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--") {
			continue
		}
		if strings.Contains(line, noTransactionMarker) {
			return true
		}
	}
	return false
}

// RequiresNoTransaction 是 requiresNoTransaction 的导出形态：集成测试
// （cmd/llm-gw-installer 的 fresh-install e2e）自带 apply 通道，必须与本
// runner 的 applySQL 采用同一标记分流——否则带 dbinit:no-transaction 标记
// 的迁移（如 718 的 DROP INDEX CONCURRENTLY）会被 --single-transaction 包裹
// 而在全新安装上失败。
func RequiresNoTransaction(content []byte) bool {
	return requiresNoTransaction(content)
}

// applySQL 应用单个 SQL 文件（通过 docker exec + stdin）
func (r *Runner) applySQL(filename string) error {
	sqlPath := filepath.Join(r.SQLDir, filename)
	content, err := readFile(sqlPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	args := []string{
		"exec", "-i",
		r.CitusContainer, "psql",
		"-U", r.DBUser,
		"-d", r.DBName,
		"-v", "ON_ERROR_STOP=1",
	}
	// 除非文件显式声明豁免，否则整文件原子。
	if !requiresNoTransaction(content) {
		args = append(args, "--single-transaction")
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(content)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql 失败: %w\n%s", err, truncate(string(out), 500))
	}
	return nil
}

// VerifySchema 检查 schema 是否加载成功
func (r *Runner) VerifySchema() (int, error) {
	cmd := exec.Command("docker", "exec", r.CitusContainer, "psql",
		"-U", r.DBUser, "-d", r.DBName,
		"-t", "-c", "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var count int
	fmt.Sscanf(string(out), "%d", &count)
	return count, nil
}

// readFile 读取文件内容
// readFile 读取文件全部内容（用 os.ReadFile 自动处理短读）
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
