// Package dbinit 提供数据库初始化能力（应用 schema + seed）
package dbinit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dockerutil"
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
			// ★ 928（2026-11-02）：探测流量物理隔离车道（热表 + 分钟聚合；写路径另由 env 门控）。
			"928_probe_traffic_physical_lane.sql",
			// ★ 929（2026-11-02 12h 审计轮 P1）：self_check_runs.error_type 的
			//   CHECK 闭集补齐 —— 与 errorsx.ErrorKind 闭集对齐。
			//   ★ 缺口本体：上游给 errorsx 加了 6 个新 kind（request_rejected 等），
			//     而 CHECK 的字面量清单没跟着长 ⇒ 写侧写的就是 kind 名 ⇒
			//     finalizeRun 的 UPDATE 被 23514 拒绝 ⇒ 整条回滚 ⇒ run 行半写。
			//   ★ 只重建约束，不改数据；自带 BEGIN/COMMIT + SET LOCAL 锁守卫。
			"929_self_check_error_type_kind_closure.sql",
			// ★ 930（2026-11-03 同步轮）：self_check_runs.error_type 的 CHECK
			//   闭集再补一个 kind —— `model_unavailable`。
			//   ★ 缺口本体：errorsx 加 `KindModelUnavailable` 的那一笔
			//     （`4fe043dc3`，2026-11-02 12:23）比 929 的落地（07:47）
			//     晚 5 小时 ⇒ CHECK 闭集少 1 项（37 → 38）。
			//   ★ 写侧写的就是 kind 名 ⇒ finalizeRun 的 UPDATE 被 23514 拒绝
			//     ⇒ 整条回滚 ⇒ self_check run 行停在 status='running' 的半写态。
			//   ★ 只重建约束，不改数据；自带 BEGIN/COMMIT + SET LOCAL 锁守卫。
			"930_self_check_error_type_kind_closure_model_unavailable.sql",
			// 931（P0-3 M1）：userscript capture 三表族（ADR
			// userscript-capture-gateway-endpoint D5）。含 RLS 策略与
			// 幂等唯一约束；down 提供对称复原。
			"931_userscript_capture_family.sql",
			// 932：fresh-install 链补 outbox_events（211 手工 V357 从未入链，
			// P0-3 live E2E 实测 42P01；幂等，已有环境 no-op）。
			"932_outbox_events_fresh_install.sql",
			// 933（P1-1 S1）：taskbus 的 task_events 表（ADR
			// outbox-consolidation-via-taskbus 目标表形态）。
			"933_task_events.sql",
			// 934–941（2026-11-04 运营管理与价格计费体系 P0/P1 数据地基）：
			// 商业骨架补列（934）、credit_ledger 三列两表同改 + 两臂读视图
			// （935）、套餐按模型额度（936）、活动族（937）、租户预算（938）、
			// 租户分层价（939）、市场情报三表（940）、供应商视图市场列（941）。
			"934_subscription_plan_commerce_columns.sql",
			"935_credit_ledger_owner_campaign_detail.sql",
			"936_subscription_plan_entitlements.sql",
			"937_promotion_campaigns.sql",
			"938_tenant_budgets.sql",
			"939_tenant_model_rates.sql",
			"940_market_price_reference_family.sql",
			"941_supplier_view_market_columns.sql",
			// 942–949（2026-11-05 报价系统 MVP W1）：供给池登记（942）、
			// 需求池（943）、报价头 + 状态迁移审计（944）、不可变版本 +
			// 不可变触发器（945）、行快照（946）、审批链（947）、分享
			// token（948）、供应商视图追加 binding 身份列（949）。
			"942_sales_supply_annotations.sql",
			"943_sales_demand_cards.sql",
			"944_sales_quotes.sql",
			"945_sales_quote_versions.sql",
			"946_sales_quote_items.sql",
			"947_sales_quote_approvals.sql",
			"948_sales_quote_shares.sql",
			"949_supplier_view_binding_id.sql",
			// 950（2026-11-07 探测时间点规划轮）：credential_probe_queue
			// source CHECK 6→7 值（补 credential_recovery）。幂等 DO 块；
			// down 有意 fail-closed。漏登的后果＝全新安装停在 538 的 6 值
			// 闭集 ⇒ 恢复验证探针提交从第一条恢复事件起被 23514 拒绝
			// （persistSubmitFailure 再覆写真实错误证据）。
			"950_probe_queue_source_credential_recovery.sql",
			// 951（R189 252 SQL 审计轮，2026-11-07）：armor_judgments 补
			// (created_at) 单列前导索引（trim 批顺扫根修，252 实测 ~13,800×）。
			"951_armor_judgments_created_at_idx.sql",
			// 952（2026-11-09 运营审计轮项2）：model_price_versions ——
			// (模型, 套餐模式) 价格期版本账（额效期自动闭前期）。
			"952_model_price_versions.sql",
			// 953（2026-11-09 恢复检测及时性优化 P0-3）：models_canonical.featured ——
			// 标记需要优先探测的模型（免费/入门级），以减少「上游已恢复但系统 ~24h 未感知」问题。
			"953_models_canonical_featured.sql",
			"954_probe_lane_trace_events.sql",
			// 955（2026-11-09 模型指纹验证增强轮）：passive_model_fingerprints ——
			// 被动式模型指纹基线（真实流量聚合的 TTFT/ITT/吞吐/Token 签名），
			// 供零成本渗水检测对比。漏登的后果＝全新安装没有基线表 ⇒
			// 被动判定维度恒缺（EvaluatePassive 无处落盘/读基线）。
			"955_passive_model_fingerprints.sql",
			// 956（2026-11-10 上游恢复检测及时性轮）：minimax-m3.1-flash 标 featured ——
			// 判据 = 实测有 24h 真实业务流量（110 次）的免费模型。免费面 24h 有流量的
			// canonical 只有 glm-5.3-flash（585 次，953 已标）与本项两个。
			// 配套代码改动：bg/model_tier.go 装载 featured canonical 时展开
			// provider_models.raw_model_name 变体 —— 探针侧三门都只比裸名，
			// 而免费供应商 raw 名多带命名空间前缀（z-ai/…、minimaxai/…）
			// ⇒ 只标裸 canonical 名对 namespaced 免费对零效果。
			// ★★ 2026-11-23 L29 轮（★ 上游 A1）：★ 956 的前置守卫（`models_canonical`
			//   里已有 active 的 `minimax-m3.1-flash`）由**安装器侧的基线补种**满足
			//   （★ dbinit.Runner 的 modelsCanonicalSeedSQL ⇒ ★ 零迁移字节变更）
			//   ⇒ ★★ **故本处不再登记任何播种迁移**（★ 同一阻断点不留两套并行解法）。
			"956_minimax_m31_flash_featured.sql",
			// 957（2026-11-11 闭集门第三次复发轮）：self_check_runs.error_type 的
			// CHECK 闭集补 `rate_limit_observed`（errorsx 新 kind 的镜像面）——
			// 缺它 ⇒ 上游请求侧 4xx 回限流形态正文时 finalizeRun 的 UPDATE 被 DB
			// 以 23514 拒绝 ⇒ 整条回滚 ⇒ self_check run 行半写 ⇒ 这次自检在系统里
			// 「根本没发生过」，但调度器以为发生过。
			"957_self_check_error_type_kind_closure_rate_limit_observed.sql",
			// 958（2026-11-11 总体设计定稿轮 G1；六面补登记 2026-11-12）：市场平台
			// 注册表 —— 双币制（A-17）区域/币种裁定唯一权威 + A-5 我方平台排除。
			// 漏登的后果＝installer 链的全新安装没有 registry ⇒ A-17 同步器
			// platform_unmapped_region 拒绝族把全部源平台拒掉 ⇒ 行情面空。
			"958_market_platform_registry.sql",
			// 959（2026-11-12 A-17 双币收口轮）：market_price_reference 双地板 17 列
			// （P1 原生币 + overseas/domestic 地板 + 快照折算）+
			// supplier_market_position 双读数 5 列 + registry 对拍补种 12 平台
			// （958 种子取自 provider_catalog，与同步源 salesdecorate.platform
			// 对拍缺 12 ⇒ 不补则一半源平台被拒）+ v_supplier_price_vs_baseline
			// v3 追加双地板投影（append-only）。
			"959_market_dual_floor_a17.sql",
			// 960（2026-11-12 批判复审轮）：迁移记账连续性修复 —— 950/951/957/958
			// 文件缺自登记行 ⇒ schema_migrations 断层；守卫式回填 + ≥952 自登记门。
			"960_migration_ledger_continuity_repair.sql",
			// 961（2026-11-12 A-17 收口 M2-T4 轮）：market_scoring_runs 每日评分
			// 运行台账（price_confidence_degraded 环比的两日读数源）。
			"961_market_scoring_runs.sql",
			// 962（2026-11-12 48h 审计第五轮）：recent_success_rate +
			// recent_cache_hit_rate 的谓词改成与既有索引
			// idx_session_turns_hot_credential_ts **逐字同形**（CASE 形态）
			// ⇒ 修「凭据监控摘要在 245/154 上必 500」（15s context 超时；
			// 真库实测 13184 ms → 94 ms）；同时把在役的 session_turns_hot
			// 函数体**补回仓内**（此前只有已退役的 v1 形态 ⇒ 仓内无法复现在役）。
			"962_recent_success_rate_index_aligned.sql",
			// 963（2026-11-13 24h 审计轮路径③）：credential_total_requests_rollup
			// —— monitor-summary 的 total_requests 预物化表（写入方
			// bg.CredentialRollupRefresher；读面 O(1)、空表回落旧两臂实时计数）。
			"963_credential_total_requests_rollup.sql",
			// 963（2026-11-13 全面审计v3 53 增量移交轮）：promote_session_turns
			// 的签名默认保留期 '7 days' → '8 hours' —— 688 的对齐被 640 的投递序
			// 事故覆盖掉、869 再把在役错值有意保守保留（migration_869_test.go 钉死）
			// ⇒ 963 完成再对齐（F-43 收口最后一步）；函数体 = 897 逐字，仅改
			// DEFAULT 一行，PartitionManager 显式传参 ⇒ 自动调度零行为变化。
			"964_promote_session_turns_default_retention_realign.sql",
			"965_api_key_concurrency_minute.sql",
			"966_auth_id_mapping_canonical_channel.sql",
			// 967（2026-11-16 12h 修订审计轮 P1 根修）：prompt_injection_policies
			// 补三列（llm_detection_fail_closed / llm_detection_sample_rate /
			// llm_detection_timeout_sec）。
			//
			// ★ 与 966 同型病：5a73df106 在 detector.go 的投影里用了这三列，
			//   但全仓 *.sql 零命中 ⇒ getPolicy 报 42703 column does not exist
			//   ⇒ Detect 报错 ⇒ hook 记一行 metadata 后 return nil（verdict=allow）
			//   ⇒ **整个提示词注入检测域静默全放行**。
			// ★ 提交说明里「读侧 COALESCE 已防 NULL」是误判：COALESCE 挡 NULL，
			//   挡不住「列根本不存在」。
			"967_prompt_injection_llm_policy_columns.sql",
			// ★ 968（2026-11-16 12h 审计轮 0820）：plugin_config 表——插件
			//   provider 选择（Native / Casdoor）的持久化。
			//   ★ 此前该表的 DDL 被写在**顶层 migrations/ 目录**（死文件区，
			//     无执行器接线）⇒ 全新安装/211 升级序列**永远不建这张表**。
			//   ⇒ cmd/gateway/plugin_init.go 的 loadConfigFromDatabase 查询
			//     报 42P01 ⇒ 静默回落 default（全部 native）⇒ admin 面上
			//     改的 provider 选择无法持久化。
			"968_plugin_config.sql",
			// ★ 969（2026-11-16 12h 审计轮 0820）：approval_casdoor_mapping 表
			//   ——llm-gateway 请求ID ↔ Casdoor 任务ID 的本地映射。
			//   ★ 此前该表的 DDL 被写在 **db/migrations/**（README 明令冻结
			//     禁新增）⇒ 同样永不建表 ⇒ internal/approval.PgxApprovalStore
			//     的第一个 INSERT 就42P01 ⇒ **Casdoor 审批链路不可用**（P1）。
			"969_approval_casdoor_mapping.sql",
			// ★ 970（2026-11-17 定价参考完善）：market_price_platform_quotes
			//   ——salesdecorate 按平台现价明细的网关侧只读副本，供运营
			//   /operations/price-compare 跨平台比价（对齐 Compare 现价模式）。
			"970_market_price_platform_quotes.sql",
			// ★ 971（2026-11-17 同步并行车 + 部署验证轮）：pi_error_analysis_system
			//   —— PI 智能体错误自动分析系统 Phase 1 基础设施
			//   （error_analysis_queue / error_analysis_results / 统计视图 /
			//    updated_at 触发器 / 保留期清理函数）。
			//   ★ 原件落在 sql/migrations/domain/641_pi_error_analysis_system.sql
			//     —— **死文件区**（该目录 95 个文件无一件登记进任何执行器）
			//     ⇒ 它既不会被安装器应用，也不会被 211 部署通道应用，
			//     且**逃过了本仓两道投递门**（sqlguard 三通道覆盖门只扫
			//     sql/migrations/startup/；embed 强制门只管 ≥849 的 canonical 件）
			//     ⇒ 「没登记」这件事在 CI 里不会变红。
			//   ★ 改号 971（而非沿用 641）：641 < 849 ⇒ 不在 embed 强制门覆盖内；
			//     641 远离 maxNum ⇒ 不在三通道覆盖门的近邻窗口（maxNum-19..maxNum）
			//     ⇒ 两道门都保护不到。971 同时满足 ≥849 与 = maxNum ⇒ 双门接管。
			"971_pi_error_analysis_system.sql",
			// ★ 972（2026-11-17 F3 残留地平线根修轮）：model_offers_update_trigger
			//   补 unavailable_recover_at。
			//
			//   ★ 根因：`model_offers` 是 INSTEAD OF UPDATE 触发器视图，触发器的
			//     SET 清单是**手写的一串列名**；211 实测（pg_get_functiondef）清单里
			//     有 available / unavailable_reason / unavailable_at，**唯独没有
			//     unavailable_recover_at** ⇒ 经视图的「置可用」写入只清两列、把恢复
			//     标记留下 ⇒ 行落进矛盾态 available=TRUE ∧ recover_at≠NULL
			//     ⇒ 节点状态恢复地平线在 37~231 行之间反复反弹（观测 19 轮从未归零）。
			//   ★ 仓内早已自述（credentialhealth/checker.go:469-473「writing it here
			//     would be silently dropped」），但只绕了它没修它。
			//   ★ 本件同时一次性清掉存量残留行（谓词 = 监控口径本身，
			//     211 实测 admin_protected 全 0 ⇒ 不改变任何绑定的可路由性）。
			"972_model_offers_trigger_recover_at.sql",
			// ★ 974（2026-11-17 定价对比完善轮）：行情面模型身份对齐 +
			//   阶梯/缓存价列。market_price_platform_quotes 加 tier /
			//   model_kind / cache_hit 列、PK 改 tier 感知、清理 940 里
			//   canonical_id 与网关 canonical 名不一致的遗留行
			//   （旧口径存的是 salesdecorate 的源 key 名 ⇒ 与评分面
			//   supplier_market_position 名字空间交集仅 12.5% ⇒ 对账页
			//   大量「我方价格 vs 市场地板」没有地板可比）。
			"974_market_price_identity_and_tier.sql",
			// ★ 975（2026-11-17 定价对比完善轮）：定价建议一键执行的审计
			//   留痕面。model_price_versions.source 闭集加入
			//   market_suggestion（**不新增第二套变更历史** —— 期次账仍是
			//   唯一权威）+ pricing_suggestion_batches 批次级留痕表。
			"975_pricing_suggestion_audit.sql",
			// ★ 976（2026-11-17 定价对比完善轮）：970 平台现价明细的**表键粒度**
			//   对齐源侧观测键 —— 加 source_region 列并把 source_region/currency
			//   并入 PK（起因：970 首次在生产写入即撞 23505 —— 同一 tier 有 5 个
			//   region，价差 7 百分点）。
			"976_market_price_platform_quote_region.sql",
			// ★ 977（2026-11-17 PI 错误分析语义订正轮）：pi_error_analysis_semantics
			//   —— 订正 971 留下的四语义缺陷（cleanup 改时间口径 / 统计视图去 JOIN
			//   膨胀 / error_analysis_results(queue_id) 唯一 / L2 规则 pattern 分组）。
			//   ★ 必须排在 971 **之后**（它 OR REPLACE 971 建出来的函数与视图）。
			//   ★ 零建表 / 零列变更 ⇒ 与并行的 Go 侧改动零冲突面。
			"977_pi_error_analysis_semantics.sql",
			// ★ 978（2026-11-17 PI 规则集交付轮）：pi_canonical_rule_set
			//   —— 把 PI 的 L1/L2 规则集纳入**安装器链**（此前 315 / 364 两条
			//   种子迁移都不在链里 ⇒ 全新安装的 prompt_injection_rules 恒空）。
			//   ★ 必须排在 977 **之后**（它种入的 privilege_escalation /
			//     command_injection 用的是 977 订正后的 pattern ⇒ 两件的
			//     先后次序会改变最终值）。
			//   ★ 幂等（ON CONFLICT DO NOTHING）⇒ 与存量库重放零冲突。
			"978_pi_canonical_rule_set.sql",
			// ★ 979（2026-11-18 PI 分析管理面优化轮）：pi_analysis_applied_actions
			//   —— error_analysis_results 加 applied_actions JSONB（应用修复
			//   的动作明细 + 旧值快照，供审计与 revert）。
			//   ★ 必须排在 971 **之后**（它加列的目标表由 971 建出；
			//     表不在场时本件自带守卫跳过 ⇒ 防御性 no-op）。
			"979_pi_analysis_applied_actions.sql",
			// ★ 980（2026-11-19 PI 统计一致性批判审计轮）：pi_analysis_stats_view_alignment
			//   —— v_error_analysis_stats 补 processing/failed/skipped 计数（对齐 GetStats）。
			//   ★ 必须排在 971/977 **之后**（视图由 971 建、977 订正；不在场时本件守卫跳过）。
			"980_pi_analysis_stats_view_alignment.sql",
			// ★ 990（2026-11-19 stats 统计口径轮）：stats_consistency_fixes——
			//   session_summaries 积分/TTFT 字段 + stats_usage_daily 成本分解 +
			//   stats_consistency_check 视图（幂等；211 真库试跑通过）。
			//   ★ 2026-11-19 文档一致性轮补五点同步（首提交只写正典）。
			// ★★★ 2026-11-23 L29 轮（真库空库实测）：★★ **必须排在 981 之前**
			//   （★ 原先排在 981 之后 ⇒ 空库新装在 981 处必炸：
			//   `column ss.total_credits_charged does not exist`）。
			//   ★ 981 的 4 个会话分析物化视图读 `session_summaries.total_credits_charged`，
			//     而该列由**本件**添加 ⇒ ★★ 981 在前就是一次真实的依赖倒置。
			//   ★ 本件只依赖 `session_summaries` / `stats_usage_daily`（基线已有）
			//     ⇒ ★ 提前到 981 之前无副作用（★ 实测：全链 392 条在空库跑通）。
			"990_stats_consistency_fixes.sql",
			// ★ 981（2026-11-19 会话分析系统基础设施）：已修正列依赖，现已就绪。
			//   为 sessions 表添加 project/task/task_category/is_probe 四列，
			//   创建 4 个物化视图（user_session_aggregates, project_task_aggregates,
			//   agent_usage_aggregates, model_usage_aggregates），创建刷新函数。
			//   列名已修正为真实 schema：owner_user, total_tokens, total_cost_usd, total_turns。
			// ★★ 2026-11-23 L29 轮：★ **排在 990 之后** —— ★ 本件的物化视图读
			//   `session_summaries.total_credits_charged`，★ 该列由 990 添加。
			"981_session_analytics_foundation.sql",
			// ★ 991：stats_timezone_standardization——统计 API 时区标准化
			//   （UTC 日边界辅助函数；真库试跑通过；自登记原指向不存在的
			//   migration_metadata 表已改回 schema_migrations）。
			"991_stats_timezone_standardization.sql",
			// ★ 992（2026-11-19 CST业务时区函数轮）：add_cst_timezone_functions
			//   —— 创建 CST（Asia/Shanghai UTC+8）业务函数族（cst_today/cst_days_ago/cst_day_start/cst_date）
			//   —— 面向用户的统计API使用CST函数，与用户认知的"今天""昨天"对齐
			"992_add_cst_timezone_functions.sql",
			// ★ 993（2026-11-20 看板对账口径对齐轮 / F4 落实）：report_snapshots_exact_cost_cents
			//   —— estimated_cost_cents bigint → numeric(24,6)：逐分组取整求和 ≠ 区间一次取整，
			//   写面同步去 ROUND 存精确分值，对账总费用与看板（usage_facts 一次求和）逐字相等。
			"993_report_snapshots_exact_cost_cents.sql",
			// ★ 994（2026-11-20 会话分析闭环审计轮）：session_analytics_owner_key_and_close_path
			//   —— 981 会话级三视图 owner 键统一 COALESCE 双源 + end_user_count；
			//   —— 四视图 DROP+CREATE 重建（自愈 252 台账记 applied 而视图缺失，
			//      视图级 SELECT 实测 90s~197s，文件自带 statement_timeout=30min 守卫）；
			//   —— session_dim 补 stop_reason 列（关闭路径 42703 根修，904 头注登记的根因）。
			//   ★ 编号注记：首落 993 与并行车撞号，重编 994（七面同步）。
			"994_session_analytics_owner_key_and_close_path.sql",
			// ★ 995（2026-11-20 错误处理手册轮）：error_playbook
			//   ——「错误类型 → 预定执行方案」手册两表（entries + executions）
			//     + 5 条种子条目（prompt 型、auto_execute=false）。
			//   ★ 幂等（CREATE TABLE IF NOT EXISTS + 表达式唯一索引 +
			//     种子 ON CONFLICT DO NOTHING）⇒ 内容指纹重放零冲突。
			//   ★ 零外部依赖（不引用其他迁移的产物）⇒ 位置只受编号序约束。
			"995_error_playbook.sql",
			// ★ 996（2026-11-21 青岛联通方案 W1-Q1）：tenant_budgets
			//   enforcement_mode —— 租户月度预算硬拦截开关（warn 默认仅告警；
			//   hard 达限网关 429 tenant_budget_exceeded，default 租户与
			//   内部 key 豁免）。幂等加列 + 闭集 CHECK DO 块。
			//   ★ 新迁移而非改 938 字节：938 已 applied+verified（245 部署）
			//     ⇒ 改字节走 content replay 连锁 break（914/928 教训）。
			"996_tenant_budget_enforcement_mode.sql",
			"997_tasks_dimension_table.sql",
			"998_session_summaries_task_association.sql",
			"999_session_summary_layers.sql",
			// ★ 918/925/973/986（2026-11-23 团队功能底座轮，★ 原编号 055-058 ⇒ 曾用
			// 982-985 ⇒ 与上游 982_provider_call_attempts（aac6602ab，已 push）撞号
			// ⇒ 按 919 让号轮先例「未 push 方避让」改占真空号）：
			//   teams / user_teams / api_keys.team_id / team_budgets 四件套。
			//   ★ ★ 重编号的原因（★ 原件**永远不会被应用**，★ 三条通道都不投它）：
			//     ① **存量升级通道**（deploy-lib 的 _deploy_pending_startup_migrations）
			//        只投「版本号 ≥ 对账地板 412」或「登记进
			//        scripts/migration-low-version-delivery.list」的 ⇒ ★ 原 055-058
			//        落在地板以下**且**不在清单里 ⇒ 不投；
			//     ② **211 seamless 通道**（scripts/apply-db-revision-sequence.sh）
			//        只投 files=(...) 数组里点名的 ⇒ 原件没点名 ⇒ 不投；
			//     ③ **installer 通道**只投本表点名的 ⇒ 原件没点名 ⇒ 不投。
			//   ⇒ ⇒ ★ 三条通道**一条都没登记** ⇒ 新装与存量都拿不到这三张表与这一列
			//        ⇒ ★ 而现役代码无条件写 api_keys.team_id ⇒ 生产「发 API key」必失败
			//        （★ 2026-11-23 生产实测：api_keys 无 team_id 列、teams/user_teams/
			//          team_budgets 三表不存在、schema_migrations 水位 999）。
			//   ★ ★ 避让重编号为 918/925/973/986（★ > 缺口棘轮纪元 914 ⇒ 缺口按「待部署」豁免、
			//     ★ 不计真缺口；★ 而投递判据是「版本号不在账本里」、★ 不是「大于水位」
			//     ⇒ 新号仍会被正常投递）。
			//   ★ 幂等（CREATE TABLE/INDEX IF NOT EXISTS ＋ ADD COLUMN IF NOT EXISTS
			//     ＋ 自登记 ON CONFLICT DO NOTHING）⇒ 重放安全。
			//   ★ 次序：918 → 925/973/986（★ 925/973/986 的外键都指向 teams.id
			//     ⇒ ★ teams 必须先建）。
			//   ★ ★ **位置放在本表末尾**（★ 而不是按编号插到 981 与 990 之间）：
			//     ★ 因为 installed_startup_migrations.tsv 的首列是**下标** ⇒ ★ 插在中间
			//     会把 996/997/998/999 的下标整体后移 4 位 ⇒ ★ 而逐迁移投递测试
			//     （migration_996/997_998_999_delivery_test.go）**硬编码了这些下标**
			//     ⇒ ⇒ ★ 追加在末尾 ⇒ ★ 既有下标一个都不动 ⇒ ★ 既有门不被无关位移打红。
			// ★ 989（2026-11-23 L20 修红轮）：会话分析族记账连续性修复 ——
			//   981 的文件**缺 schema_migrations 自登记行** ⇒ 211 的记账链在
			//   980 与 990 之间**断一格**（实测：211 有 '980' 与 '990'、独缺 '981'，
			//   而 981 的产物全部在场 ⇒ 它确实跑过、只是没记账）。
			//   ★ 另立迁移而非改 981 字节：981 在 211/252 都已 applied 且
			//     252 台账 checksum 与磁盘逐字一致 ⇒ 改字节即 L23 同款分叉
			//     （914/928 教训同款）。
			//   ★ 守卫式回填（981 头产物在场才记账）+ 自身自登记（≥952 约定）。
			//   ★ 编号取空号 989（三位数惯例：sqlguard 的 migrationNumPrefix
			//     钉死 ^[0-9]{3}_ ⇒ 四位数对全部守卫隐形；996 的台账行记过同款）。
			"989_migration_ledger_session_analytics_981_repair.sql",
			"982_provider_call_attempts.sql",
			// ★ 983（2026-11-24 统计专用表 request_metrics）：独立建表、无依赖，
			//   执行顺序不敏感 ⇒ **排在清单末尾**。
			//   ★ 为什么必须排末尾：sql/schema/installed_startup_migrations.tsv 是本清单的
			//     机器可读镜像，首列是**序号**；在中间插一行会把后面所有迁移的序号整体 +1
			//     （996/997/998/999 被顶成 388/389/390/391）⇒ 按序号断言的守卫门会红，
			//     且报错形态是「tsv 缺 996 登记行」——像是台账丢行、实际是序号漂移 ⇒ 极难定位。
			//     ★ 要重排清单时，先同步更新那些按序号断言的门，别让序号悄悄漂移。
			"983_request_metrics.sql",
			// ★ 984（2026-11-24 P1 会话宇宙探测误杀修复轮）：会话宇宙谓词加
			//   「真实计费豁免」—— 有真实计费（ss.total_cost_usd>0 / rl.cost_usd>0）
			//   的会话不因 owner 名字命中探测正则而被剔（根因：(^deployment-gate)
			//   正则误杀真实 agent 流量；211 实测 30 天 16,698 会话 / $7,163.51）。
			//   ★ 依赖 994 的四个物化视图（它重建它们）⇒ 必须排在 994 之后。
			//   ★ 排在清单末尾：tsv 首列是序号，中间插行会把后面所有序号整体 +1。
			"984_session_universe_cost_aware_probe_scope.sql",
			// ★ 985-989（2026-11-23 至 11-24 请求与会话对账）：逐次供应商调用记录、
			//   请求关联、媒体用量、账单存证、用量汇总、供应商合同费率。
			"985_request_reconciliation_links.sql",
			"986_provider_attempt_media_usage.sql",
			"987_provider_invoice_evidence.sql",
			"988_provider_invoice_usage_buckets.sql",
			// ★★ 供应商合同费率件 —— **原名 989_supplier_contract_rates.sql**
			//   ⇒ ★ 2026-11-24 还号轮（12h 修订审计轮第三轮）：它与
			//     989_migration_ledger_session_analytics_981_repair.sql **同占 989**
			//     ⇒ ① 迁移号唯一性门红（Go 门 + 壳层门「≥500 号段禁止任何撞号」）
			//     ⇒ ② **本安装器以文件名前缀为版本键**（migrationVersionOf：
			//        取 "_" 前的数字）⇒ 台账里有 '989' 就跳过 ⇒ **增量安装会跳过
			//        本件** ⇒ public.supplier_contract_rates 建不出来（审计档 L2）。
			//   ★ 还号到 **925**（真空号：磁盘无 / git 历史无 / 211 台账无）：
			//     ① **零字节变更** —— sha256 仍是 b32e8fb9…，与 211 部署通道账本
			//        gateway_db_revision_sequences 记的 content_sha256 逐字一致
			//        ⇒ 不触发 L23 内容指纹分叉（改字节才有分叉风险）；
			//     ② **保留两道唯一性门的有效性** —— 若改用字母后缀 989b，
			//        Go 门（`^[0-9]+_`）与壳层门（家族后缀豁免）会**同时对字母
			//        后缀失明** ⇒ 把「响亮的撞号」变成「静默的撞号」；
			//     ③ 文件自身的自登记仍是 '988'（**未改字节**）⇒ 211 台账里那行
			//        '988' 依然属本件 ⇒ 台账语义不变、**零生产写**。
			//   ★ 清单位置不动（仍排在 988 之后）：tsv 首列是序号，中间插行会把
			//     后面所有序号整体 +1（仓内纪律：tsv 只追加）。
			"925_supplier_contract_rates.sql",
			// ★ 983b（2026-11-24 12h 修订审计轮）：request_metrics 补记原始模型名列。
			//   ★ 原名 994_add_raw_model_name 与 994_session_analytics_owner_key_and_close_path
			//     撞号（迁移号唯一性门红），且**任何通道都没登记它**
			//     ⇒ raw_model_name 列在生产上不存在 ⇒ writer.go 写这一列必然 42703
			//     ⇒ 整条 request_metrics 管线静默丢数。
			//   ★ 改号过程：994 → 1000（四位数对按文件名解析的门隐形，被当场拦下）
			//     → 973（★ **逆序依赖**：973 < 983，而两条自动枚举通道
			//     （offline_apply.go 数字序 / init-local-db.sh 字典序）只按号排序、
			//     不读 Dependencies 头 ⇒ 本迁移会排在建表件之前 ⇒ ALTER 落空）
			//     → **983b**（★ 主号+字母后缀，仓内既有合法记法 15 条）：
			//     versionPrefix 解析成 983 ⇒ 与 983_request_metrics.sql 同号 ⇒
			//     排序按文件名 ⇒ '983_' < '983b_' ⇒ **排在 983 建表件之后** ✓。
			//   ★ 排在清单末尾：tsv 首列是序号，中间插行会把后面所有序号整体 +1。
			"983b_add_raw_model_name.sql",
			// ★ 055-058（2026-11-23 团队层级预算基础设施 W2-Q2）：团队族四表。
			//   ★ 原先只建了 canonical + embeddata 双副本、**没登记进任何通道**
			//     ⇒ 团队族四表**从来没被任何通道建过**
			//     ⇒ maas/team_budget_gate 读不到 teams/user_teams/team_budgets。
			//   ★ 四条自带幂等守卫（CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS）
			//     与 ON CONFLICT 自登记 ⇒ 补接线不产生重复应用风险。
			//   ★ 相对顺序不可打乱：057 给 api_keys 加 team_id 并外键引用 teams(id)
			//     ⇒ 必须排在 055 之后。
			//   ★ 排在清单末尾：tsv 首列是序号，中间插行会把后面所有序号整体 +1。
			"055_teams_table.sql",
			"056_user_teams.sql",
			"057_api_keys_team_id.sql",
			"058_team_budgets.sql",
			// ★ 983c（2026-11-24 12h 修订审计轮第二轮）：request_metrics 补复合游标索引。
			//   ★ processor.go 的 watermarkSQL 按 (occurred_at DESC, request_id DESC) 取水位线，
			//     **每批跑一次** ⇒ 而该索引**从来不存在**（迁移 983 的 11 个索引里没有它）
			//     ⇒ 2026-11-24 生产只读 EXPLAIN 实测：全表并行扫描 + top-N 排序
			//     ⇒ 340,904 行时 26,572 buffers / **117ms 每批**。
			//   ★ 依赖 983（request_metrics 表）⇒ 必须排在 983 之后（已满足）。
			//   ★ 排在清单末尾：tsv 首列是序号，中间插行会把后面所有序号整体 +1。
			"983c_add_occurred_cursor_index.sql",
			// ★ 999a（2026-11-24 数据维护手册轮；编号取字母后缀空号——三位数惯例 983b/983c 先例）：运维/数据维护手册条目六条
			//   —— 数据面异常（模型归因缺失/计费口径缺口/会话维度缺失/
			//     迁移双账本分叉/跨面视图列序漂移/归档残片）的标准处置路径。
			//   ★ 全部 prompt 型 + auto_execute=FALSE（fail-closed 同 995）；
			//     探测器（DataQualitySource）在 Go 侧随批接线。
			//   ★ 幂等（纯 INSERT + ON CONFLICT DO NOTHING，零 DDL）⇒
			//     内容指纹重放零冲突；零外部依赖 ⇒ 位置只受清单序约束。
			"999a_data_maintenance_playbook.sql",
			"973_request_metrics_projection.sql",
			// ★ 999b/999c/999d（2026-11-26 存量缺口收口轮 · 第 31 轮）：★ 三条都是
			//   「让生产 SQL 已经引用的列真的有投递来源」—— 来源是第 30 轮新立的
			//   「生产 SQL 引用的表与列必须有投递来源创建」门（internal/schemadelivery）
			//   第一次校准时抓出的存量缺口。
			//   ★ 999b：system_probe_runs.total_tokens —— bg/systemmonitor/audit.go 的
			//     INSERT 写这一列，而创建它的 346 那份件躺在 sql/migrations/domain/
			//     （不被任何通道消费）⇒ 探针审计行静默丢失。读侧聚合同批恢复。
			//   ★ 999c：tool_usage_stats / tool_usage_stats_hot 的 134 形状 13 列
			//     （tool_name / date / total_calls / p50…）＋ tool_id、tenant_id 去
			//     NOT NULL（代码的 INSERT 不提供这两个值）＋ (tool_name, date) 唯一
			//     约束（供 UPSERT）＋ 分区子表同步补列。
			//   ★ 999d：licenses / releases 的 tenant_id ＋ 两个索引 —— tenantops 的
			//     两个租户运维接口读这一列，而 389 那份件躺在
			//     sql/migrations/startup/up/（★ 那个目录整体不被任何通道消费）。
			//   ★ 三条都排在**末尾**：tsv 首列是序号，中间插行会把后面所有序号整体 +1。
			//   ★ 三条都是幂等 DDL（ADD COLUMN IF NOT EXISTS / DROP NOT EXISTS）；
			//     999c 的唯一约束用「先查 pg_constraint 再建」（PG 没有 IF NOT EXISTS）。
			"999b_system_probe_run_tokens.sql",
			"999c_tool_usage_stats_columns.sql",
			"999d_tenant_ops_scope.sql",
			// ★ 633（2026-11-27 一周审计轮投递收口的安装器五点补全）：
			//   tenant_attachment_quotas —— quota.go 的读写面此前在
			//   **全新安装链路上没有建表来源**（633 只投了序列通道/安装器 TSV/
			//   embeddata 副本/installer/migrations 四点 ⇒ 新装库缺表 ⇒ 42P01）。
			//   ★ 幂等 DDL（CREATE TABLE/INDEX IF NOT EXISTS）⇒ 补接线零重复应用风险；
			//   ★ 633 从未在任何环境应用过 ⇒ 零已应用字节变更。
			"633_tenant_attachment_quotas.sql",
			// ★ 1000-1002（2026-11-26 上游计费 P0 轮）：计费状态追踪 ＋ 账单付款
			//   追踪 ＋ 计费重试队列。上游已把三条登记进安装器 TSV 清单
			//   （manifest 411 → 412）⇒ 五点同步补全（embeddata 副本/go:embed/
			//   embeddedSQLFiles/StartupFiles/parity 豁免）。
			//   ★ 三条自带 BEGIN/COMMIT ＋ 5s lock_timeout 守卫（999a/999d 口径）。
			//   ★ 相对顺序：1001 读 1000 的 billing_status ⇒ 顺序不可打乱；
			//     1002 建 billing_retry_queue 表 ⇒ 排在 1000 之后即可。
			"1000_billing_status_and_credit_limit.sql",
			"1001_bill_payment_tracking.sql",
			"1002_billing_retry_queue.sql",
			// ★ 492（2026-11-27 W4.3 渠道商车道轮）：channels / customers /
			//   channel_users / channels_customers 四张表 —— 渠道商门户的归属层级。
			//   ★ 本轮投递收口：492 源文件存在但【从未在任何通道】⇒ 生产库无表
			//   ⇒ admin/channels.go 四处引用 42P01。五点同步补全（embeddata/
			//   go:embed/StartupFiles/apply脚本/TSV manifest）。
			//   ★ 492 自带事务包裹 ＋ 自登记 ⇒ 符合≥952双账本纪律。
			//   ★ 从未应用过 ⇒ 零已应用字节变更。
			"492_channels_customers.sql",
			// ★ 983j-983r（V2 合并 2026-11-27，随 codex/request-metrics-v2 并入）：
			//   request_metrics 973 projection 复用面 —— repair 视图（983j，
			//   request_metrics_pending_events = pending ∪ refresh_pending）、
			//   日汇总/日看板/日历史/租户历史/历史明细/看板小时/小时分布（983k-r）。
			//   ★ 追加在清单末尾（tsv 只追加纪律，中间插行会整体 +1）；
			//   ★ 983j 基于 211 现役表 request_metrics_pending 建 UNION 视图 ⇒
			//     对已发版库零破坏、全新安装两表齐建后视图可用。
			"983j_request_metrics_973_repair.sql",
			"983k_request_metrics_daily_summary.sql",
			"983l_request_metrics_daily_board.sql",
			"983m_request_metrics_daily_history.sql",
			"983n_request_metrics_daily_tenant_history.sql",
			"983o_request_metrics_daily_history_details.sql",
			"983p_request_metrics_history_pages.sql",
			"983q_request_metrics_board_hours.sql",
			"983r_request_metrics_board_hour_distributions.sql",
			// ★ 1003（2026-11-27 三级配额制轮）：组织层级 + 组织/用户积分钱包。
			//   organizations ＋ users.org_id ＋ org_credit_wallets ＋
			//   user_credit_wallets（租户→组织→用户三级额度链数据面）。
			//   自带 BEGIN/COMMIT ＋ 5s lock_timeout 守卫（1000 同款）。
			//   ★ 排在 1000 之后（同族四位数排序惯例，无硬依赖）。
			"1003_org_hierarchy_and_wallets.sql",
			// ★ 1004（2026-11-27 三级配额制轮）：三级模型过滤。
			//   tenant_model_policies.mode（allow/deny 双模，缺省 deny=既有
			//   黑名单语义）＋ org_model_policies / user_model_policies
			//   （024 范式：软删 + active 视图 + 审计触发器）。
			//   自带 BEGIN/COMMIT ＋ 5s lock_timeout 守卫；依赖 1003。
			"1004_three_tier_model_policies.sql",
		},
	}
}

// WaitForPG 等待 PG 就绪（最多 timeout 秒）
//
// ★★★ **契约（★ 2026-11-23 修、★ 之前是破的）**：★★★
//
// ★ 这条函数的注释**一直写着**「★ 最多 timeout 秒 ★」⇒ ★★ 而旧实现里
// ★ **每一次 `docker exec` 都没有任何上限** ⇒ ★★ Docker 守护进程僵死时
// ★ （★ `docker exec` 客户端永不返回 ★）**第一次调用就无限挂**
// ★ ⇒ ★★ **注释里的契约今天本来就是破的** ⇒ ★★ 而且连
// ★ 「PostgreSQL 启动超时 (Ns)」这句话**都打不出来**（★ 运维只看到「卡住」 ★）★★。
//
// ★★ **修法（★ 两条一起 ★）**：★★
//
//	★ ① **每次调用单独封顶**（★ `dockerutil.DockerCmdTimeoutFast` ★）
//	★ ② **整段用 deadline 收口**（★ 而不是「循环 N 次」★）
//
// ★ ⇒ ★★ **总耗时回到「最多 timeout 秒」** ★★（★ 而不是无限期 ★）
// ★ ⇒ ★★ **且能区分两种失败**：★★
//
//	★ **PG 还没起来**（★ docker 正常、★ `pg_isready` 快速返回非零 ★）
//	★   ⇒ 保持原口径：**「PostgreSQL 启动超时 (Ns)」** ★
//	★ **docker 本身无响应**（★ 调用被超时砍掉 ★）
//	★   ⇒ 追加一句「★ docker exec 无响应、★ Docker 守护进程可能僵死 ★」
//	★   ⇒ ★★ **让下一次撞上它的人一眼看出根因** ★★。
func (r *Runner) WaitForPG(timeout int) error {
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	unresponsive := false
	for {
		// ★ ★ 单次预算**同时受两个上限约束** ★ ★：
		// ★ ① 快档上限（★ `DockerCmdTimeoutFast` ★）★ —— 防止一次调用吃掉整个预算；
		// ★ ② **剩余 deadline**（★ ★ ⇒ ★ 总耗时真正回到「最多 timeout 秒」 ★ ★）★。
		// ★ ★ 只加 ① 不加 ② ⇒ ★ 总耗时会是「timeout ＋ 一档」⇒ ★ 契约仍然被破坏 ★★。
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		budget := dockerutil.DockerCmdTimeoutFast
		if remaining < budget {
			budget = remaining
		}
		_, err := dockerutil.RunDocker(budget,
			"exec", r.CitusContainer, "pg_isready", "-U", r.DBUser)
		if err == nil {
			return nil
		}
		// ★ 本次失败是「★ docker 无响应 ★」还是「★ PG 还没起来 ★」？
		// ★ 二者的区别就是：**调用有没有被自己的超时砍掉**。
		if errors.Is(err, context.DeadlineExceeded) {
			unresponsive = true
		}
		if time.Until(deadline) <= 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if unresponsive {
		return fmt.Errorf("PostgreSQL 启动超时 (%ds): ★ docker exec pg_isready 无响应（★ Docker 守护进程可能僵死）", timeout)
	}
	return fmt.Errorf("PostgreSQL 启动超时 (%ds)", timeout)
}

// ledgerQuery 是已有库探测语句：读 schema_migrations 台账（迁移文件尾部
// 的自登记 INSERT 维护）。提成常量是为了让「增量模式判据读的是哪张表」
// 可被测试钉住，防止漂移到别的口径。
const ledgerQuery = "SELECT version FROM public.schema_migrations"

// modelsCanonicalSeedSQL 是**新装路径**的基线补种（★ 背景见 InitSchema 的说明）。
//
// ★ 为什么需要它：★ `models_canonical` 在空库基线里只有少数几行（★ 种子 6 行
// ＋ `446_volcengine_model_aliases` 补的 13 行）⇒ ★ 而 `956` 的前置守卫要求
// 存在 **active 的 `minimax-m3.1-flash`** ⇒ ★★ 空库新装在 956 处必然中止
// （★ 真库实测：`migration 956 aborted: canonical_name=minimax-m3.1-flash
// does not exist or is not active`）。
//
// ★ 为什么**补种**而不是**改 956 的字节**：★ 956 已在生产 applied（★ 211 记账
// 2026-11-10 13:16:14）⇒ ★ 改它的字节＝制造分叉（★ 字节冻结原则）⇒ ★★ 而
// 「空库本来就没有业务模型目录」这个前提才是真正的缺口 ⇒ ★★ 补种是**只动
// 安装器代码**的修法（★ 不碰任何迁移字节）。
//
// ★ 幂等：★ `ON CONFLICT (canonical_name)` ⇒ 重复执行不报错、不产生重复行。
const modelsCanonicalSeedSQL = `
INSERT INTO public.models_canonical (canonical_name, family, status, source)
VALUES ('minimax-m3.1-flash', 'minimax', 'active', 'installer-baseline-seed')
ON CONFLICT (canonical_name) DO UPDATE
    SET status = 'active', updated_at = now()
    WHERE public.models_canonical.status IS DISTINCT FROM 'active';
`

// InitSchema 应用基线三件（00-prereqs → 01-schema → 02-seed）+ startup 迁移。
//
// 已有库走**增量模式**：schema_migrations 台账存在且非空 ⇒ 基线三件是全新
// 安装的一次性底座（幂等，但整库 DDL 重放代价大），不再重放；startup 迁移
// 只应用台账未登记的 pending 项 —— 「数据库已在 ⇒ 不重建，只补变更的结构
// 与数据」。种子（02-seed）本来就全部 ON CONFLICT DO NOTHING，跳过它不会
// 丢任何新种子：新版本的种子投递按仓内纪律走更高编号的 startup 迁移。
//
// 与内部升级通道（apply-db-revision-sequence.sh）的差异要知情：那条通道按
// content_sha256 指纹判断「内容被加固后重放」；本通道只按 version 跳过 ——
// 同编号文件的内容加固不会在这里重放（需要内容变更时按仓内纪律出更高编号
// 的增量迁移，而不是改老文件）。
//
// ★★★ 2026-11-23 L33 轮（真库空库 + 装完再升实测）：★ 本通道的「跳过」判据
//
// ★★★ 原来只认「版本号在 schema_migrations 台账里」⇒ ★★ 而台账是**迁移文件
// ★★ 自己**在尾部 INSERT 维护的 ⇒ ★★ 392 条 startup 迁移里只有 110 条自登记
// ★★ ⇒ ★★ 其余 280 条**从不记账** ⇒ ★★ **每次增量都被重放** ⇒ ★★ 而「重放
// ★★ 安全」是个**未经证明的假设**（★ 实测两条反例：★ bootstrap 撞已被 890
// ★★ 下线的 public.request_logs_hot；★ 392 的 SELECT * UNION ALL 视图在列序
// ★★ 漂移的库上炸）⇒ ★★ ⇒ **同一个刚装成功的库，第二次升级必失败**。
//
// ★★ 本轮补三条（★ **零迁移字节变更**，★ 全在安装器代码里）：
//
//	★ F1 记账补齐：应用成功后把版本补记进人读台账 schema_migrations
//	  （★ 923 那条迁移就是为同一目的写的：★ 「人读视图落后 ⇒ 运维把已应用
//	  ★ 误判成待应用」）⇒ ★★ 增量通道从此**只补未登记的变更**。
//	★ F2 安装器专属资产（★ bootstrap）只在**全新安装**路径投递
//	  （★ 判据来自文件自身的契约声明，★ 见 freshInstallOnlyAssets）。
//	★ F3 **只读**咨询部署通道的权威台账 gateway_db_revision_sequences
//	  （★ 已被部署通道按同一内容指纹应用过的文件 ⇒ 不重放；★ 这与部署通道
//	  ★ 自己的跳过判据同款 ⇒ ★ 两条通道从此对「什么算已应用」口径一致）。
func (r *Runner) InitSchema(logger func(string)) error {
	return r.initSchema(logger, false)
}

// UpgradeSchema requires an existing ledger because offline packages only contain startup SQL.
func (r *Runner) UpgradeSchema(logger func(string)) error {
	return r.initSchema(logger, true)
}

func (r *Runner) initSchema(logger func(string), upgradeOnly bool) error {
	// ★★★ 2026-11-23（L30 轮·补②⑤）：★ **重号守卫（fail-closed）**。
	//
	//	★ 为什么这道守卫必须住在【安装器】里、而不只住在测试套件里：
	//	★ 仓里已有 `sql/migrations/startup` 那道目录级重号门
	//	★ （TestNumericUpMigrationVersionsAreUnique）⇒ ★ 它能抓住
	//	★ 「同一个目录里两个文件同号」⇒ ★ 但它**只在那道门被跑到时**生效。
	//	★ 而本安装器**可以被绕过测试套件直接跑**（★ 灾备重建 / 新节点首装 /
	//	★ 运维手工执行安装器）⇒ ★★ **那时重号的后果是【静默的】**：
	//
	//	★★ 机制：★ 增量通道按【版本号】跳过（★ startupPending ⇒ 台账里有该
	//	★★ 版本 ⇒ 跳过）⇒ ★★ 于是**同号的第二份文件被静默跳过** ⇒
	//	★★ 「一个版本号两份 schema」＝ 其中一份【永久缺席】、★ 且**不报错**；
	//	★★ 全新装路径下两份都会跑（F1 补记时第二份被 WHERE NOT EXISTS 吞掉）
	//	★★ ⇒ 台账只记一行 ⇒ **台账与实际不符**。
	//
	//	★★ **实测过的真事故（★ 2026-11-23）**：★ origin/main 的
	//	★★ `982_provider_call_attempts.sql`（★ 供应商逐次调用落库）
	//	★★ 与并行车分支的 `982_teams_table.sql`（★ teams 族首件）
	//	★★ 同号 ⇒ 合并后 StartupFiles 里两行同号（★ 实测 2049/2064 行）。
	//
	//	★ 处置：★ **在任何数据库动作之前**校验清单 ⇒ 重号即中止、★ 明确报错
	//	★ （★ fail-closed ⇒ 宁可拒绝启动、★ 不可静默缺席）。
	if err := validateStartupVersionsUnique(r.StartupFiles); err != nil {
		return fmt.Errorf("startup 迁移清单版本号重号，拒绝启动（fail-closed，不做任何数据库动作）: %w", err)
	}

	applied, hasLedger, err := r.appliedVersions()
	if err != nil {
		return err
	}
	incremental := hasLedger && len(applied) > 0
	if upgradeOnly && !incremental {
		return fmt.Errorf("offline startup upgrade requires an existing migration ledger")
	}

	// ★ F3：增量路径**只读**部署通道的权威台账（★ 表不存在 ⇒ 空表 ⇒ 不影响）。
	var deployApplied map[string]string
	if incremental {
		deployApplied, err = r.deployLedgerApplied()
		if err != nil {
			return err
		}
		if len(deployApplied) > 0 {
			logger(fmt.Sprintf("  ▶ 部署通道权威台账 gateway_db_revision_sequences 命中 %d 条已应用文件（★ 只读咨询，★ 不写）", len(deployApplied)))
		}
		if err := r.verifyReleasedProjectionFingerprint(applied, deployApplied); err != nil {
			return err
		}
	}

	if incremental {
		logger("  ▶ 检测到已有数据库（schema_migrations 台账非空）—— 增量模式：跳过基线三件，仅应用 pending 迁移")
	} else {
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
			if _, err := r.applySQL(f.name); err != nil {
				return fmt.Errorf("应用 %s 失败: %w", f.name, err)
			}
			logger(fmt.Sprintf("  ✅ %s 完成", f.name))
		}

		// ★★ 2026-11-23 L29 轮（真库空库实测）：★ 基线三件只带进少数几行
		//   `models_canonical` ⇒ ★ 而 956 的前置守卫要求存在 **active 的
		//   `minimax-m3.1-flash`** ⇒ ★★ 空库新装在 956 处**必然中止**（★ 实测报错
		//   `migration 956 aborted: canonical_name=minimax-m3.1-flash does not
		//   exist or is not active`）⇒ ★★ 这里在基线之后、启动迁移之前**补种**。
		//
		// ★ 只在**新装路径**执行（★ 增量路径不写 ⇒ ★ **零生产写**）。
		// ★ 不改 956 的字节（★ 956 已在生产 applied ⇒ 字节冻结）。
		logger("  ▶ 补种 models_canonical 基线行（956 的前置守卫所需） ...")
		if err := r.execSQL(modelsCanonicalSeedSQL); err != nil {
			return fmt.Errorf("补种 models_canonical 基线行失败: %w", err)
		}
		logger("  ✅ models_canonical 基线行补种完成")
		if err := r.execSQL(createFreshHandoffSQL); err != nil {
			return fmt.Errorf("初始化安装器交接凭证: %w", err)
		}
	}

	appliedCount := 0
	skippedCount := 0
	retiredLegacyLogs := false
	if incremental && applied[890] {
		retired, err := r.psqlQuery(requestLogsRetiredSQL)
		if err != nil {
			return fmt.Errorf("检查旧请求日志表族退役状态: %w", err)
		}
		retiredLegacyLogs = strings.TrimSpace(retired) == "t"
	}
	var plan []freshStartupStep
	var receipts []installerMigrationReceipt
	if incremental {
		for _, name := range r.StartupFiles {
			plan = append(plan, freshStartupStep{name: name})
		}
	} else {
		plan = freshStartupPlan(r.StartupFiles)
	}
	for _, step := range plan {
		name := step.name
		if incremental {
			if reason, skip := r.startupSkipReason(name, applied, deployApplied); skip {
				logger(fmt.Sprintf("  ▶ 跳过 startup migration %s（%s）", name, reason))
				skippedCount++
				continue
			}
			if retiredLegacyLogs && retiredLegacyLogFiles[name] {
				logger("  ▶ 跳过 startup migration " + name + "（890 已登记，旧请求日志表族已退役）")
				skippedCount++
				continue
			}
			if retiredLegacyLogs && name == "688_promote_default_retention_align_go_scheduler.sql" {
				current, err := r.psqlQuery(promotionDefaultsPresentSQL)
				if err != nil {
					return fmt.Errorf("检查 688 的迁移产物: %w", err)
				}
				if strings.TrimSpace(current) != "t" {
					return fmt.Errorf("688: 旧请求日志已退役，但当前晋升函数或保留期不齐全")
				}
				logger("  ▶ 跳过 startup migration " + name + "（当前晋升函数已存在，保留现用函数与保留期）")
				skippedCount++
				continue
			}
			if name == "568_credential_priority_flag.sql" {
				current, err := r.psqlQuery(bindingPriorityPresentSQL)
				if err != nil {
					return fmt.Errorf("检查 568 的优先凭据产物: %w", err)
				}
				if strings.TrimSpace(current) == "t" {
					logger("  ▶ 跳过 startup migration " + name + "（当前报价视图、写入触发器和路由失效链已支持 priority）")
					skippedCount++
					continue
				}
			}
			if successor, ok := startupSupersessions[name]; ok {
				_, recorded := r.startupSkipReason(successor.file, applied, deployApplied)
				if recorded {
					proven, err := r.psqlQuery(successor.check)
					if err != nil {
						return fmt.Errorf("检查 %s 的后继产物: %w", name, err)
					}
					if strings.TrimSpace(proven) == "t" {
						logger(fmt.Sprintf("  ▶ 跳过 startup migration %s（%s 的产物已验证）", name, successor.file))
						skippedCount++
						continue
					}
				}
			}
		}
		logger(fmt.Sprintf("  ▶ 应用 startup migration %s ...", name))
		if incremental {
			_, err = r.applySQL(filepath.Join("startup", name))
		} else {
			var receipt installerMigrationReceipt
			receipt, err = r.applyFreshStartup(step)
			if err == nil {
				receipts = append(receipts, receipt)
			}
		}
		if err != nil {
			return fmt.Errorf("应用 startup migration %s 失败: %w", name, err)
		}
		logger(fmt.Sprintf("  ✅ startup migration %s 完成", name))
		if !step.supplemental {
			if err := r.recordAppliedVersion(name); err != nil {
				return fmt.Errorf("补记 %s 的台账行失败: %w", name, err)
			}
		}
		appliedCount++
	}
	if incremental {
		logger(fmt.Sprintf("  ✅ 增量迁移完成：本次应用 %d 个 startup 迁移（跳过 %d 个）", appliedCount, skippedCount))
	} else {
		if err := r.sealFreshHandoff(receipts); err != nil {
			return fmt.Errorf("提交安装器交接凭证: %w", err)
		}
		logger(fmt.Sprintf("  ✅ 安装器交接凭证已登记 %d 个 startup 迁移", len(receipts)))
	}
	return nil
}

// These migrations only maintain the retired tables, their views or their
// promotion routine. A surviving table or detached August child disables
// the exception so it cannot hide an incomplete retirement.
var retiredLegacyLogFiles = map[string]bool{
	"562_fix_request_logs_bodies_partitions_heap.sql": true,
	"573_drop_request_logs_body_columns.sql":          true,
	"577_request_logs_view_customer_id.sql":           true,
	"600_outbound_body_to_bodies_hot.sql":             true,
	"601_request_logs_bodies_drop_metadata.sql":       true,
	"602_request_logs_promote_atomic.sql":             true,
	"603_repair_request_logs_schema_consistency.sql":  true,
}

const requestLogsRetiredSQL = `SELECT
	EXISTS (SELECT FROM public.schema_migrations WHERE version='890')
	AND to_regclass('public.request_logs') IS NULL
	AND to_regclass('public._request_logs') IS NULL
	AND to_regclass('public.request_logs_hot') IS NULL
	AND to_regclass('public._request_logs_hot') IS NULL
	AND to_regclass('public.request_logs_bodies') IS NULL
	AND to_regclass('public._request_logs_bodies') IS NULL
	AND to_regclass('public.request_logs_bodies_hot') IS NULL
	AND to_regclass('public._request_logs_bodies_hot') IS NULL
	AND to_regclass('public.request_logs_bodies_2026_08') IS NULL
	AND to_regclass('public._request_logs_bodies_2026_08') IS NULL
	AND NOT EXISTS (SELECT FROM pg_proc
	    WHERE pronamespace='public'::regnamespace AND proname='ensure_request_logs_bodies_partition'
	      AND pronargs=1 AND proargtypes[0]='timestamptz'::regtype)`

const bindingPriorityPresentSQL = `SELECT
	EXISTS (SELECT FROM pg_attribute WHERE attrelid=to_regclass('public.credential_model_bindings')
		AND attname='priority' AND atttypid='boolean'::regtype AND attnotnull AND NOT attisdropped)
	AND EXISTS (SELECT FROM pg_attribute WHERE attrelid=to_regclass('public.model_offers')
		AND attname='priority' AND atttypid='boolean'::regtype AND NOT attisdropped)
	AND EXISTS (SELECT FROM pg_trigger WHERE tgrelid=to_regclass('public.model_offers')
		AND tgname='model_offers_update' AND tgenabled <> 'D'
		AND tgfoid=to_regprocedure('public.model_offers_update_trigger()'))
	AND EXISTS (SELECT FROM pg_trigger WHERE tgrelid=to_regclass('public.credential_model_bindings')
		AND tgname='trg_notify_auto_route_cmb_update' AND tgenabled <> 'D'
		AND strpos(pg_get_triggerdef(oid), 'old.priority IS DISTINCT FROM new.priority') > 0)
	AND (SELECT count(*) FROM pg_proc WHERE oid IN (
		to_regprocedure('public.model_offers_update_trigger()'),
		to_regprocedure('public.bump_candidate_binding_scope_revision_insert()'),
		to_regprocedure('public.bump_candidate_binding_scope_revision_delete()'),
		to_regprocedure('public.bump_candidate_binding_scope_revision_update()')
	) AND strpos(prosrc, 'priority') > 0) = 4`

const sessionBodiesKindPresentSQL = `SELECT
	EXISTS (SELECT FROM pg_class WHERE oid=to_regclass('public.session_bodies_hot') AND relrowsecurity)
	AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass('public.session_bodies_hot')
		AND conname IN ('session_bodies_hot_pkey','session_bodies_hot_unique','session_bodies_with_current_month'))=3
	AND to_regclass('public.uq_session_bodies_final_full') IS NOT NULL
	AND to_regclass('public.uq_session_bodies_hot_final_full') IS NOT NULL
	AND EXISTS (SELECT FROM pg_attribute WHERE attrelid=to_regclass('public.session_bodies_unified')
		AND attname='kind' AND NOT attisdropped)`

const sessionBodiesPromotePresentSQL = `SELECT EXISTS (
	SELECT FROM pg_proc WHERE oid=to_regprocedure('public.promote_session_bodies_hot_to_partition(interval,integer)')
	  AND strpos(prosrc, 'v_parent_shape IS DISTINCT FROM v_hot_shape') > 0
	  AND strpos(prosrc, 'FOR UPDATE SKIP LOCKED') > 0)`

// 628 carries aggregation_id across the atomic move. Its deployed body
// predates 698's timezone pin and must still survive older 535/624 replays.
const candidateFailureCarriesAggregationSQL = `strpos(p.prosrc, 'FOR UPDATE SKIP LOCKED') > 0
	  AND strpos(p.prosrc, 'h.aggregation_id') > 0
	  AND p.prosrc ~ 'aggregation_id[[:space:]]+FROM moved_rows'`

const candidateFailurePromotePresentSQL = `SELECT EXISTS (
	SELECT FROM pg_proc p
	WHERE p.oid=to_regprocedure('public.promote_candidate_failure_logs_hot_to_partition(interval,integer)')
	  AND ` + candidateFailureCarriesAggregationSQL + `)`

const promotionDefaultsPresentSQL = `SELECT count(*)=8
	FROM (VALUES ('usage_ledger'), ('request_wal'), ('routing_decision_log'), ('credential_model_index'),
	             ('tool_usage_stats'), ('credit_ledger'), ('candidate_failure_logs'), ('session_turns')) AS f(family)
	JOIN pg_proc p ON p.oid=to_regprocedure('public.promote_' || f.family || '_hot_to_partition(interval,integer)')
	WHERE p.pronargdefaults=2
	  AND (pg_get_function_arguments(p.oid) LIKE '%DEFAULT ''08:00:00''::interval%'
	       OR (f.family='session_turns' AND pg_get_function_arguments(p.oid) LIKE '%DEFAULT ''7 days''::interval%')
	       OR (f.family='candidate_failure_logs'
	           AND pg_get_function_arguments(p.oid) LIKE '%DEFAULT ''24:00:00''::interval%'))
	  AND (f.family<>'candidate_failure_logs' OR (` + candidateFailureCarriesAggregationSQL + `))`

var startupSupersessions = map[string]struct{ file, check string }{
	"535_candidate_failure_logs_atomic_promote.sql": {"628_candidate_failure_logs_promote_atomic_v3.sql", candidateFailurePromotePresentSQL},
	"614_session_bodies_hot.sql":                    {"865_session_bodies_kind_reconcile.sql", sessionBodiesKindPresentSQL},
	"615_session_bodies_hot_promote_function.sql":   {"899_session_bodies_promote_body_sink.sql", sessionBodiesPromotePresentSQL},
	"625_session_bodies_unified_explicit.sql":       {"865_session_bodies_kind_reconcile.sql", sessionBodiesKindPresentSQL},
	"626_session_bodies_hot_promote_reconcile.sql":  {"899_session_bodies_promote_body_sink.sql", sessionBodiesPromotePresentSQL},
	"637_session_bodies_unified_today_visible.sql":  {"865_session_bodies_kind_reconcile.sql", sessionBodiesKindPresentSQL},
	"638_session_bodies_promote_guard.sql":          {"899_session_bodies_promote_body_sink.sql", sessionBodiesPromotePresentSQL},
	"649_routing_analytics_probe_filter.sql": {"907_routing_analytics_source_lateral.sql", `SELECT
		to_regclass('public.routing_analytics_7d') IS NOT NULL AND to_regclass('public.routing_audit_summary_7d') IS NOT NULL
		AND COALESCE(strpos(pg_get_viewdef(to_regclass('public.routing_analytics_source')), 'LATERAL'),0)>0`},
}

// freshInstallOnlyAssets：**只在全新安装路径投递**的资产（★ 增量路径跳过）。
//
// ★ 判据来自**文件自身的契约声明**——不是本代码的偏好：
//
//	session_turns_hot_bootstrap.sql 的文件头写着：
//	  "This installer-only asset ... It is not an upgrade migration and
//	   must not be applied to an existing deployment."
//
// ★ 而安装器对**无数字前缀**的文件一向「恒执行」（★ 靠幂等契约兜底）
// ⇒ ★★ 两条契约直接对撞 ⇒ ★★ 增量路径执行它 ⇒ 撞已被 890 下线的
// ★★ public.request_logs_hot ⇒ ★★ 报错（★ 本机实测：
// ★★ ERROR:  relation "public.request_logs_hot" does not exist）。
//
// ★ 另一条无前缀文件 361b_provider_models_source_prelude.sql **不在**本表：
// ★ 它的契约是「host-first 部署通道 prelude」（★ 幂等 ADD COLUMN，★ 纯加列）
// ⇒ ★ 它**必须**在增量路径照跑 ⇒ ★★ ⇒ 本表按**逐文件契约**列，★ 不按
// ★★ 「无前缀即跳过」一刀切。
//
// ★ 本表与 embeddata 的一致性由 TestFreshInstallOnlyAssetsMatchFileContract
// ★ 双向核对（★ 新增同款契约声明的资产 ⇒ 那道门立刻报红 ⇒ 不会静默漏登）。
var freshInstallOnlyAssets = map[string]string{
	"session_turns_hot_bootstrap.sql": "文件头自述：installer-only，must not be applied to an existing deployment",
}

// startupSkipReason 判定增量模式下该 startup 文件是否跳过（★ 并给出可读理由）。
//
// ★ 三条判据（★ 顺序即优先级）：
//  1. 安装器专属资产（freshInstallOnlyAssets）⇒ 跳过（★ 契约声明）。
//  2. 版本号已在 schema_migrations 台账 ⇒ 跳过（★ 既有判据，★ F1 之后覆盖面
//     从 110 条扩到全部 392 条）。
//  3. 部署通道权威台账里已有该文件 ⇒ 跳过（★ F3：★ 同一内容指纹 ⇒ 已应用；
//     ★ 遗留无指纹记账行 ⇒ 部署通道自己按「已应用」处理 ⇒ 同款；★ 指纹不同 ⇒
//     ★ 内容被加固过 ⇒ **不跳过**（★ 与部署通道的「内容加固 ⇒ 重放」同款）。
//
// ★ 其余（★ 无前缀且不在专属资产表里、★ 或台账里没有的版本）⇒ 不跳过（★ 执行）。
func (r *Runner) startupSkipReason(name string, applied map[int]bool, deployApplied map[string]string) (string, bool) {
	if reason, ok := freshInstallOnlyAssets[name]; ok {
		return reason, true
	}
	v, hasVersion := migrationVersionOf(name)
	if !hasVersion {
		return "", false
	}
	if !startupPending(name, applied) {
		return fmt.Sprintf("版本 %d 已在 schema_migrations 台账", v), true
	}
	storedSHA, hit := deployApplied[name]
	if !hit {
		return "", false
	}
	if storedSHA == "" {
		return "部署通道已应用（★ 遗留无指纹记账行 ⇒ 同部署通道口径按已应用处理）", true
	}
	currentSHA, err := r.fileSHA256(filepath.Join("startup", name))
	if err != nil {
		// ★ 读不到文件 ⇒ 不在这里判红（★ 让 applySQL 去报真实的错）。
		return "", false
	}
	if currentSHA == storedSHA {
		return "部署通道已按同一内容指纹应用", true
	}
	return "", false
}

// verifyReleasedProjectionFingerprint runs before applying the 973 repair.
// The version row alone cannot distinguish the released 973 from other builds.
func (r *Runner) verifyReleasedProjectionFingerprint(applied map[int]bool, deployApplied map[string]string) error {
	const file = "973_request_metrics_projection.sql"
	deployedSHA, deployed := deployApplied[file]
	if !slices.Contains(r.StartupFiles, file) || (!applied[973] && !deployed) {
		return nil
	}
	packagedSHA, err := r.fileSHA256(filepath.Join("startup", file))
	if err != nil {
		return fmt.Errorf("read released 973 fingerprint: %w", err)
	}
	if deployed && deployedSHA != packagedSHA {
		return fmt.Errorf("released 973 deployment fingerprint differs from %s", file)
	}
	hasInstallerReceipts, err := r.psqlQuery(`SELECT to_regclass('public.gateway_db_installer_migration_receipts') IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("check installer receipts for 973: %w", err)
	}
	var installedSHA string
	if strings.TrimSpace(hasInstallerReceipts) == "t" {
		installedSHA, err = r.psqlQuery(`SELECT content_sha256 FROM public.gateway_db_installer_migration_receipts
			WHERE file_name='973_request_metrics_projection.sql'`)
		if err != nil {
			return fmt.Errorf("read installer fingerprint for 973: %w", err)
		}
		installedSHA = strings.TrimSpace(installedSHA)
	}
	if installedSHA != "" && installedSHA != packagedSHA {
		return fmt.Errorf("released 973 installer fingerprint differs from %s", file)
	}
	if !deployed && installedSHA == "" {
		return fmt.Errorf("released 973 requires a deployment or installer fingerprint for %s", file)
	}
	return nil
}

// recordAppliedVersion 把「这条 startup 文件已被应用」补记进人读台账
// public.schema_migrations（★ 只在缺行时补一行；★ 不覆盖运维改过的 description）。
//
// ★ 为什么需要它（★ 本轮的核心修法 F1）：
//
//	· 台账的行是**迁移文件自己**在尾部 INSERT 维护的（★ 本仓定式）；
//	· ★ 而 392 条 startup 迁移里只有 110 条有那句 INSERT ⇒ ★★ 其余 280 条
//	  **从不记账** ⇒ ★★ 增量通道按「版本在台账里 ⇒ 跳过」判定时，★★ 那 280 条
//	  **每次增量都被重放**；
//	· ★★ 而重放安全是**未经证明的假设**（★ 本轮实测两条反例）。
//	· ★★ 923_schema_migrations_ledger_backfill.sql 就是为同一目的写的迁移
//	  （★ 「schema_migrations = 人读视图；它落后 ⇒ 运维把『已应用』误判成
//	  ★ 『待应用』」）⇒ ★★ 本函数把同一件事**前移到安装器** ⇒ ★★ 从此增量
//	  ★★ 通道只补**未登记**的变更。
//
// ★ 幂等：★ WHERE NOT EXISTS ⇒ 不写重行；★ 自登记的文件重复调用是空操作。
// ★ 无版本号的资产不入版本台账（★ 它们不参与版本判定）。
func (r *Runner) recordAppliedVersion(name string) error {
	v, ok := migrationVersionOf(name)
	if !ok {
		return nil
	}
	desc := "installer:" + name
	sql := fmt.Sprintf(`INSERT INTO public.schema_migrations (version, description)
SELECT '%d', '%s'
WHERE NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '%d')`, v, desc, v)
	return r.execSQL(sql)
}

// deployLedgerApplied 读部署通道的**权威台账** public.gateway_db_revision_sequences
// （★ ★ 只读：★ 本函数只 SELECT，★ 不写、★ 不建表）。
//
// ★ 台账行形态（★ 见 scripts/apply-db-revision-sequence.sh）：★ 每行一个
// ★ **文件**标记，sequence_name = "<序列名>:<文件名>"，★ content_sha256 = 该
// ★ 文件当时的内容指纹（★ 空串 = 指纹通道上线前的遗留记账行）。
//
// ★ 返回「文件名 → 内容指纹」映射（★ 文件名取 sequence_name 最后一个 ':' 之后
// ★ 的部分 ⇒ ★ 不依赖部署序列的名字 ⇒ ★ 换序列名也不影响）。
func (r *Runner) deployLedgerApplied() (map[string]string, error) {
	exists, err := r.psqlQuery("SELECT to_regclass('public.gateway_db_revision_sequences')")
	if err != nil {
		return nil, fmt.Errorf("探测部署通道权威台账失败: %w", err)
	}
	if strings.TrimSpace(exists) == "" {
		return map[string]string{}, nil
	}
	rows, err := r.psqlQuery("SELECT sequence_name || '|' || COALESCE(content_sha256, '') FROM public.gateway_db_revision_sequences")
	if err != nil {
		return nil, fmt.Errorf("读取部署通道权威台账失败: %w", err)
	}
	return parseDeployLedgerMarkers(rows), nil
}

// parseDeployLedgerMarkers 把权威台账的原始行解析成「文件名 → 内容指纹」。
//
// ★ 行形态（★ 见 scripts/apply-db-revision-sequence.sh 的写入点）：
// ★   `sequence_name || '|' || COALESCE(content_sha256, ”)`
// ★   其中 sequence_name = 「<序列名>:<文件名>」（★ 每行一个**文件**标记），
// ★   content_sha256 = 该文件当时的内容指纹（★ 空串 = 指纹通道上线前的遗留行）。
//
// ★ 抽出成纯函数是为了**能单测**：★ marker 的 basename 切分规则（★ 取最后一个
// ★ ':' 之后）★ 是跨通道口径对齐的关键（★ 换序列名不影响识别）⇒ ★ 不能只靠
// ★ 真库读数兜着。
func parseDeployLedgerMarkers(rows string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(rows, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		marker, sha, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		idx := strings.LastIndex(marker, ":")
		if idx < 0 || idx+1 >= len(marker) {
			continue
		}
		out[marker[idx+1:]] = sha
	}
	return out
}

// fileSHA256 计算安装器 SQL 目录下一个文件的内容指纹（★ 与部署通道
// sha256_file 同口径：★ 原始字节的 sha256 小写十六进制）。
func (r *Runner) fileSHA256(relPath string) (string, error) {
	content, err := readFile(filepath.Join(r.SQLDir, relPath))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

// appliedVersions 读取目标库的 schema_migrations 台账。
//
// 表是否存在用 to_regclass 探测（对不存在的表返回 NULL 而不是报错）——
// 「全新库」是普通的查询结果，不是要捕获的错误路径；因此本函数不需要
// 依赖任何 PG 错误码字面量。其余失败（容器不可达/权限）原样上抛 ——
// 宁可中止安装也不在「不知道库里有什么」的状态下盲跑 DDL。
func (r *Runner) appliedVersions() (versions map[int]bool, hasLedger bool, err error) {
	exists, err := r.psqlQuery("SELECT to_regclass('public.schema_migrations')")
	if err != nil {
		return nil, false, fmt.Errorf("探测 schema_migrations 台账失败: %w", err)
	}
	if strings.TrimSpace(exists) == "" {
		return nil, false, nil // 全新库：基线尚未铺
	}
	out, err := r.psqlQuery(ledgerQuery)
	if err != nil {
		return nil, false, fmt.Errorf("读取 schema_migrations 台账失败: %w", err)
	}
	versions = make(map[int]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// -t -A 输出纯数字行；混入的非数字行（如 psql 提示）直接忽略，
		// 不让它们把整个探测打成失败。
		if v, perr := strconv.Atoi(line); perr == nil {
			versions[v] = true
		}
	}
	return versions, true, nil
}

// psqlQuery 在目标库跑一条只读查询，返回 psql 的 stdout（-t -A 纯值形态）。
func (r *Runner) psqlQuery(query string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", r.CitusContainer,
		"psql", "-U", r.DBUser, "-d", r.DBName, "-X", "-t", "-A", "-v", "ON_ERROR_STOP=1",
		"-c", query)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, truncate(stderr.String(), 300))
	}
	return string(out), nil
}

// validateStartupVersionsUnique 校验 startup 清单里没有两个文件共用同一个数字版本号。
//
// ★ 判据口径（★ 与仓内 `sql/migrations/startup` 的目录级重号门同款）：
//   - 版本号＝文件名第一个下划线前的数字前缀（★ 复用 migrationVersionOf ⇒
//     `361b_...prelude` 这类「数字＋字母」前缀解析不出 ⇒ 不参与重号判定、
//     ★ 与增量通道「无版本号 ⇒ 恒执行」的口径一致）；
//   - `.down.sql` 不在 StartupFiles 里（★ 清单只投递 up 迁移）⇒ 不额外排除；
//   - 同号即报错、★ **不设豁免表**（★ 新清单不该有重号；★ 真要豁免必须写清
//     「量在哪」★ 而不能像历史目录级门那样把旧账留在表里）。
func validateStartupVersionsUnique(files []string) error {
	seen := make(map[int]string, len(files))
	for _, name := range files {
		v, ok := migrationVersionOf(name)
		if !ok {
			continue
		}
		if prev, dup := seen[v]; dup {
			return fmt.Errorf("startup migration version %d is duplicated by %s and %s", v, prev, name)
		}
		seen[v] = name
	}
	return nil
}

// startupPending 报告增量模式下该 startup 文件是否需要执行：
// 台账未登记该文件的 version 前缀 ⇒ pending。解析不出数字前缀的文件
// （如 head cluster 的 session_turns_hot_bootstrap.sql）恒执行 —— 它们
// 没有台账行可对照，靠幂等契约兜底，行为与全新安装一致。
func startupPending(name string, applied map[int]bool) bool {
	v, ok := migrationVersionOf(name)
	if !ok {
		return true
	}
	return !applied[v]
}

// migrationVersionOf 从迁移文件名解析 version 前缀（"978_xxx.sql" → 978）。
func migrationVersionOf(name string) (int, bool) {
	base := filepath.Base(name)
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, false
	}
	v, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, false
	}
	return v, true
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
func (r *Runner) applySQL(filename string) (installerMigrationReceipt, error) {
	sqlPath := filepath.Join(r.SQLDir, filename)
	content, err := readFile(sqlPath)
	if err != nil {
		return installerMigrationReceipt{}, err
	}
	sourceHash := sha256.Sum256(content)
	switch filename {
	case "startup/520_durable_task_settlement_intents.sql":
		content, err = adaptDurableSettlementPolicy(content)
	case "startup/565_cost_usd_pricing_backfill.sql":
		content, err = adaptRetiredRequestCostBackfill(content)
	case "startup/624_candidate_failure_logs_promote_atomic_v2.sql":
		content, err = adaptCandidateFailurePromotion(content)
	}
	if err != nil {
		return installerMigrationReceipt{}, err
	}

	if err := r.runSQL(content, !requiresNoTransaction(content)); err != nil {
		return installerMigrationReceipt{}, err
	}
	return installerMigrationReceipt{filepath.Base(filename), sourceHash, sha256.Sum256(content)}, nil
}

func (r *Runner) runSQL(content []byte, singleTransaction bool) error {
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
	if singleTransaction {
		args = append(args, "--single-transaction")
	}
	args = append(args, "-X", "-f", "-")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(content)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql 失败: %w\n%s", err, truncate(string(out), 20000))
	}
	return nil
}

// Older databases may have the 520 policy without a receipt. Retain an
// equivalent policy in place. If the definition differs, the original CREATE
// raises a duplicate-policy error and the migration transaction rolls back.
func adaptDurableSettlementPolicy(content []byte) ([]byte, error) {
	const start = "CREATE POLICY durable_task_settlement_access ON durable_task_settlement_intents\n"
	const commit = "\nCOMMIT;"
	sql := string(content)
	if strings.Count(sql, start) != 1 || strings.Count(sql, commit) != 1 || strings.Index(sql, start) > strings.Index(sql, commit) {
		return nil, fmt.Errorf("520: unexpected durable settlement policy statement boundaries")
	}
	const expr = `$expr$((tenant_id = current_setting('app.current_tenant'::text, true)) OR (current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text))$expr$`
	const guard = `DO $installer_520$
BEGIN
    IF NOT EXISTS (
        SELECT FROM pg_policy
        WHERE polrelid = 'durable_task_settlement_intents'::regclass
          AND polname = 'durable_task_settlement_access'
          AND polcmd = '*' AND polpermissive AND polroles = ARRAY[0::oid]
          AND pg_get_expr(polqual, polrelid) = ` + expr + `
          AND pg_get_expr(polwithcheck, polrelid) = ` + expr + `
    ) THEN
`
	sql = strings.Replace(sql, start, guard+start, 1)
	sql = strings.Replace(sql, commit, "\n    END IF;\nEND\n$installer_520$;\n"+commit, 1)
	return []byte(sql), nil
}

// Keep 565's pricing catalog updates. Its last step can only target the
// original v1 hot table, so omit it after the proven 890 retirement.
func adaptRetiredRequestCostBackfill(content []byte) ([]byte, error) {
	const section = "-- 4) recompute hot rows with priced offers + tokens\n"
	const commit = "\nCOMMIT;"
	sql := string(content)
	if strings.Count(sql, section) != 1 || strings.Count(sql, commit) != 1 || strings.Index(sql, section) > strings.Index(sql, commit) {
		return nil, fmt.Errorf("565: unexpected request cost backfill statement boundaries")
	}
	const condition = `SELECT NOT (` + requestLogsRetiredSQL + `) AS apply_legacy_hot_backfill
\gset
\if :apply_legacy_hot_backfill
`
	sql = strings.Replace(sql, section, condition+section, 1)
	sql = strings.Replace(sql, commit, "\n\\endif"+commit, 1)
	return []byte(sql), nil
}

// Apply 624's RLS policy while preserving a later, month-aware promotion
// routine. The historical migration did not separate those two changes.
func adaptCandidateFailurePromotion(content []byte) ([]byte, error) {
	const start = "CREATE OR REPLACE FUNCTION public.promote_candidate_failure_logs_hot_to_partition("
	const end = "\n$function$;\n"
	sql := string(content)
	if strings.Count(sql, start) != 1 || strings.Count(sql, end) != 1 || strings.Index(sql, start) > strings.Index(sql, end) {
		return nil, fmt.Errorf("624: unexpected candidate failure promotion statement boundaries")
	}
	probe := candidateFailurePromotePresentSQL + ` AS keep_current_promotion
\gset
\if :keep_current_promotion
\else
`
	sql = strings.Replace(sql, start, probe+start, 1)
	sql = strings.Replace(sql, end, end+"\\endif\n", 1)
	return []byte(sql), nil
}

// execSQL 在目标库执行一段内联 SQL。
//
// 用于安装器的基线补种和交接凭证。
// ★ 迁移内容**一律**走 applySQL 的逐文件事务通道（★ 免得绕过
// `dbinit:no-transaction` 的分流与逐文件失败定位）。
func (r *Runner) execSQL(sql string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", r.CitusContainer, "psql",
		"-U", r.DBUser,
		"-d", r.DBName,
		"-v", "ON_ERROR_STOP=1",
		"--single-transaction")
	cmd.Stdin = strings.NewReader(sql)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql 失败: %w\n%s", err, truncate(string(out), 20000))
	}
	return nil
}

// VerifySchema 检查 schema 是否加载成功
func (r *Runner) VerifySchema() (int, error) {
	// ★★ 2026-11-23：★ 带**硬超时**（★ 与 `WaitForPG` 同族 ★）。
	out, err := dockerutil.RunDocker(dockerutil.DockerCmdTimeoutSlow,
		"exec", r.CitusContainer, "psql",
		"-U", r.DBUser, "-d", r.DBName,
		"-t", "-c", "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';")
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
