# Tables

> 本目录包含 tables 对象的 DDL 定义，从 `sql/objects/tables/` 同步。

## 统计

- **文件数量**: 271
- **同步来源**: `sql/objects/tables/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
agent_relationships.sql
agents.sql
analysis_events.sql
api_key_auto_profile.sql
api_key_model_cost.sql
api_keys.sql
applications.sql
approval_approvers.sql
approval_configs.sql
approval_queue.sql
approval_requests.sql
approval_routing_rules.sql
approval_rules.sql
armor_judgments.sql
asset_relationships.sql
assets.sql
attachments.sql
auto_tune_audit.sql
background_tasks_duplicates.sql
background_tasks.sql
billing_orders.sql
canary_tokens.sql
candidate_failure_logs.sql
center_commands.sql
compression_bench_results.sql
credential_capabilities.sql
credential_health_checks.sql
credential_model_bindings.sql
credential_model_call_history.sql
credential_model_index_2026_07.sql
credential_model_index_2026_08.sql
credential_model_index_archive.sql
credential_model_index_hot.sql
credential_model_index.sql
credential_model_peak_1m.sql
credential_model_stats_1m.sql
credential_model_weekly_peak.sql
credential_probe_configs.sql
credential_probe_model_log.sql
credential_probes.sql
credential_quota_usage.sql
credential_quotas.sql
credential_state_log.sql
credentials.sql
credit_ledger_2026_07.sql
credit_ledger_2026_08.sql
credit_ledger_hot.sql
credit_ledger_old.sql
credit_ledger.sql
dashboard_access_events_2026_07.sql
dashboard_access_events_2026_08.sql
dashboard_access_events_hot.sql
dashboard_access_events.sql
diagnostic_runs.sql
donations.sql
download_events.sql
download_publish_runs.sql
fault_action_logs.sql
fault_events.sql
fault_rules.sql
gateway_instances.sql
goal_sessions.sql
gray_release_rules.sql
handoff_logs.sql
injection_attack_vectors.sql
instance_heartbeats.sql
instance_release_status.sql
instance_status_reports.sql
integrity_fingerprint_baseline.sql
intent_aggregates.sql
intent_analysis_adjustments.sql
intent_classification_feedback.sql
intent_classifier_config.sql
internal_service_keys.sql
ip_blocklist.sql
key_applications.sql
key_rpm_daily.sql
license_devices.sql
license_holders.sql
license_module_audit.sql
license_modules.sql
license_trial_consents.sql
licenses.sql
llm_gateway_migration_checksums.sql
local_models.sql
local_runtimes.sql
maas_credit_consumption_buckets.sql
maas_settings.sql
model_aliases.sql
model_credit_rates.sql
model_discovery_runs.sql
model_families.sql
model_fingerprints.sql
model_integrity_events.sql
model_lifecycle_jobs.sql
model_name_mapping.sql
model_offer_events.sql
model_pricing_history.sql
model_pricing.sql
model_probe_runs_2026_07.sql
model_probe_runs_hot.sql
model_probe_runs.sql
model_probe_state.sql
model_reconcile_log.sql
model_task_index.sql
models_canonical.sql
node_probe_runs.sql
node_probe_state.sql
node_stats.sql
offline_activation_requests.sql
ops_node_registrations.sql
output_compliance_audit.sql
output_compliance_custom_keywords.sql
output_compliance_feedback.sql
output_compliance_policies.sql
output_compliance_review_queue.sql
passive_probe_state.sql
pii_patterns.sql
price_change_events.sql
pricing_plans.sql
pricing_refresh_log.sql
product_module_features.sql
product_modules.sql
prompt_injection_detections.sql
prompt_injection_llm_engines.sql
prompt_injection_policies.sql
prompt_injection_rules.sql
provider_catalog.sql
provider_cost_reconciliation.sql
provider_credibility_tests.sql
provider_error_details.sql
provider_events.sql
provider_header_profiles.sql
provider_health_events.sql
provider_metrics_hour.sql
provider_metrics_minute.sql
provider_models_v1000_backup.sql
provider_models.sql
provider_profile_alerts.sql
provider_profile_daily.sql
provider_profile_metrics.sql
provider_profile_whitelist.sql
provider_quality_configs.sql
provider_quality_profiles.sql
provider_quality_rollup.sql
provider_scores.sql
provider_settings.sql
providers.sql
release_artifacts.sql
releases.sql
request_attachments.sql
request_context_attrs.sql
request_envelope.sql
request_logs_2026_07.sql
request_logs_2026_08.sql
request_logs_archive.sql
request_logs_bodies_2026_07.sql
request_logs_bodies_2026_08.sql
request_logs_bodies_2026_09.sql
request_logs_bodies_hot.sql
request_logs_bodies.sql
request_logs_hot.sql
request_logs.sql
request_stage_events.sql
request_stats_dim_minute.sql
request_stats_error_drill_minute.sql
request_stats_minute.sql
request_stats_rollup_cursor.sql
request_wal_2026_07.sql
request_wal_2026_08.sql
request_wal_archive.sql
request_wal_bodies.sql
request_wal_hot.sql
request_wal.sql
response_format_anomalies.sql
route_decisions.sql
route_incident_events.sql
route_incidents.sql
routing_audit_log.sql
routing_decision_log_2026_07.sql
routing_decision_log_2026_08.sql
routing_decision_log_archive_2026_08.sql
routing_decision_log_archive.sql
routing_decision_log_hot.sql
routing_decision_log.sql
routing_health_checks.sql
routing_overrides_audit.sql
routing_overrides.sql
routing_policy.sql
runtime_alert_events.sql
runtime_metrics.sql
runtime_telemetry_consent_events.sql
runtime_telemetry_preferences.sql
schema_migration_audit.sql
schema_migrations_backup_20260722.sql
schema_migrations.sql
security_audit_log.sql
security_detector_config.sql
self_check_round_results.sql
self_check_runs.sql
self_check_settings.sql
session_audit_records.sql
session_bodies_2026_07.sql
session_bodies_2026_08.sql
session_bodies.sql
session_intent_evolution.sql
session_last_requests.sql
session_memora_extraction_log.sql
session_module_executions_2026_07.sql
session_module_executions_2026_08.sql
session_module_executions_hot.sql
session_module_executions.sql
session_summaries.sql
session_titles.sql
session_turn_logs.sql
session_turn_snapshots.sql
session_turns_2026_07.sql
session_turns_2026_08.sql
session_turns.sql
sessions_2026_07.sql
sessions_2026_08.sql
sessions.sql
settings_audit.sql
settings_kv.sql
severity_action_matrix.sql
sticky_sessions.sql
subscription_plans.sql
subscription_tiers.sql
system_identity_pool.sql
system_probe_runs_default.sql
system_probe_runs.sql
system_settings.sql
task_default_routing_audit.sql
task_default_routing.sql
tenant_credit_wallets.sql
tenant_model_policies_audit.sql
tenant_model_policies.sql
tenant_settings_kv.sql
tenant_subscriptions.sql
tenant_tool_policies.sql
tenants.sql
test_columnar_new.sql
tier_module_map.sql
token_audit_events.sql
tool_call_events.sql
tool_categories.sql
tool_registry.sql
tool_usage_stats_2026_07.sql
tool_usage_stats_2026_08.sql
tool_usage_stats_hot.sql
tool_usage_stats_old.sql
tool_usage_stats.sql
topup_packages.sql
toxic_keywords.sql
tuning_params.sql
tuning_proposals.sql
tuning_signals.sql
upgrade_logs.sql
ursm_node_snapshot_min.sql
usage_ledger_2026_07.sql
usage_ledger_2026_08.sql
usage_ledger_hot.sql
usage_ledger_old.sql
usage_ledger.sql
usage_minute.sql
users.sql
vibe_code_reviews.sql
vibe_coding_projects.sql
vibe_coding_sessions.sql
work_type_config.sql
work_type_model_route.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/tables/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/tables/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/tables/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/tables/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
