// Package dbinit 提供数据库初始化能力（应用 schema + seed）
package dbinit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			"478_auto_route_affinity.sql",
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
			"600_outbound_body_to_bodies_hot.sql",
			"601_request_logs_bodies_drop_metadata.sql",
			"602_request_logs_promote_atomic.sql",
			"606_session_summaries_agent_expert_tags.sql",
			"614_session_bodies_hot.sql",
			"615_session_bodies_hot_promote_function.sql",
			"618_request_journey_snapshot_receipts.sql",
			"620_provider_error_details_tenant_scope.sql",
			"621_provider_error_details_cleanup_index.sql",
			"622_provider_error_aggregator_state.sql",
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
			"682_model_offers_context_window_columns.sql",
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
			"session_turns_hot_bootstrap.sql",
			// 733/734 (taskprofile 后续轮, 2026-09-21): 会话存储解耦 v3
			// 五点同步补齐 —— session v3 落地（035f9382e）只跑了 db.Open
			// 与 schema 通道，安装器同步移交本轮收口。733 建
			// session_turn_details（hot + 月分区，50000/批回填），734
			// 把 canonical 视图 session 分支的 30 个 NULL 占位换成 details
			// LEFT JOIN。必须 733 在前、734 在后，且均在
			// session_turns_hot_bootstrap 之后（依赖 hot 表存在）。
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
			// 800 (2026-09-24, supplier-protocol-optimization §3.2): 每
			// provider 多端点表 + 从 providers 旧行回填（ON CONFLICT DO
			// NOTHING 幂等）。原 deploy V800 文件从未进任何存量库通道，
			// 本轮移入 startup 并补登记。
			"800_provider_endpoint_protocols.sql",
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

// applySQL 应用单个 SQL 文件（通过 docker exec + stdin）
func (r *Runner) applySQL(filename string) error {
	sqlPath := filepath.Join(r.SQLDir, filename)
	content, err := readFile(sqlPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i",
		r.CitusContainer, "psql",
		"-U", r.DBUser,
		"-d", r.DBName,
		"-v", "ON_ERROR_STOP=1",
		"--single-transaction",
	)
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
