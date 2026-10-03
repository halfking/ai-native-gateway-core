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
			// 820：给 session_turns 补 is_abandoned 标记列（审计 §9.92，
			// 取代被推翻的 819 独立表方案）。母表 + hot 两张都要有——只加一张
			// 会让另一张上的同名列不存在，UPDATE 直接报 42703。
			// 写方（telemetry markAbandonedTurn）在 updateRequestLog 的
			// upsert 竞态回落分支上，fail-open，故本迁移未应用时只是标记缺失。
			"820_session_turns_abandoned_marker.sql",
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
