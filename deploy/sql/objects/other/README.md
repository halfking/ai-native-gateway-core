# Other

> 本目录包含 other 对象的 DDL 定义，从 `sql/objects/other/` 同步。

## 统计

- **文件数量**: 42
- **同步来源**: `sql/objects/other/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
064_convert_partition_to_heap_text.sql
agent_relationships_fk_agent_rel_dst.sql
agent_relationships_fk_agent_rel_src.sql
asset_relationships_fk_asset_rel_dst.sql
asset_relationships_fk_asset_rel_src.sql
backfill_request_logs_bodies_integer.sql
credential_probe_configs_credential_probe_configs_credential_id_fkey.sql
credential_probes_credential_probes_credential_id_fkey.sql
dashboard_access_events_chk_dae_event_type.sql
dashboard_access_events_hot_chk_dae_hot_event_type.sql
donations_donations_holder_id_fkey.sql
download_events_download_events_holder_id_fkey.sql
fault_action_logs_fault_action_logs_event_id_fkey.sql
gray_release_rules_gray_release_rules_release_id_fkey.sql
injection_action.sql
injection_category.sql
instance_release_status_instance_release_status_release_id_fkey.sql
intent_analysis_adjustments_intent_analysis_adjustments_superseded_by_fkey.sql
license_devices_license_devices_license_id_fkey.sql
license_modules_license_modules_license_id_fkey.sql
license_modules_license_modules_module_key_fkey.sql
license_trial_consents_license_trial_consents_license_id_fkey.sql
licenses_licenses_holder_id_fkey.sql
ops_node_registrations_ops_node_registrations_license_id_fkey.sql
output_compliance_policies_fk_output_compliance_tenant.sql
product_module_features_product_module_features_module_key_fkey.sql
prompt_injection_detections_prompt_injection_detections_rule_id_fkey.sql
prompt_injection_policies_fk_prompt_injection_tenant.sql
provider_quality_configs_provider_quality_configs_provider_id_fkey.sql
provider_quality_profiles_provider_quality_profiles_provider_id_fkey.sql
route_incident_events_route_incident_events_incident_id_fkey.sql
runtime_metrics_runtime_metrics_license_id_fkey.sql
runtime_telemetry_consent_events_runtime_telemetry_consent_events_license_id_fkey.sql
runtime_telemetry_preferences_runtime_telemetry_preferences_license_id_fkey.sql
self_check_round_results_self_check_round_results_run_id_fkey.sql
session_module_executions_chk_sme_status.sql
session_module_executions_hot_chk_sme_hot_status.sql
session_summaries_fk_session_tenant.sql
tier_module_map_tier_module_map_module_key_fkey.sql
tier_module_map_tier_module_map_tier_code_fkey.sql
vibe_code_reviews_vibe_code_reviews_session_id_fkey.sql
vibe_coding_sessions_vibe_coding_sessions_project_id_fkey.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/other/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/other/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/other/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/other/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
