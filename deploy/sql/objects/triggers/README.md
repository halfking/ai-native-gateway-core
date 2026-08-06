# Triggers

> 本目录包含 triggers 对象的 DDL 定义，从 `sql/objects/triggers/` 同步。

## 统计

- **文件数量**: 29
- **同步来源**: `sql/objects/triggers/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
api_keys_trg_notify_auto_route_apikeys.sql
approval_approvers_approval_approvers_updated_at.sql
approval_configs_approval_configs_updated_at.sql
approval_rules_approval_rules_updated_at.sql
canary_tokens_update_canary_tokens_modtime.sql
credential_model_bindings_cmb_protect_manual_disable.sql
credential_model_bindings_trg_notify_auto_route_cmb_insert_delete.sql
credential_model_bindings_trg_notify_auto_route_cmb_update.sql
credentials_trg_auto_fp_slot_limit_insert.sql
credentials_trg_check_credential_dates.sql
credentials_trg_notify_auto_route_creds.sql
diagnostic_runs_diagnostic_runs_touch.sql
intent_classification_feedback_trigger_intent_feedback_correctness.sql
key_applications_trg_key_applications_updated_at.sql
model_name_mapping_model_name_mapping_updated_at.sql
model_offers_model_offers_update.sql
model_pricing_model_pricing_change_log.sql
model_pricing_model_pricing_updated_at.sql
output_compliance_custom_keywords_update_output_compliance_custom_keywords_modtime.sql
prompt_injection_llm_engines_update_prompt_injection_llm_engines_modtime.sql
provider_settings_trigger_provider_settings_updated_at.sql
providers_trg_notify_auto_route_providers.sql
route_incidents_route_incidents_touch.sql
routing_overrides_routing_overrides_audit_trg.sql
session_audit_records_session_audit_records_updated_at.sql
session_last_requests_trigger_session_last_requests_updated_at.sql
severity_action_matrix_update_severity_action_matrix_modtime.sql
system_settings_trigger_system_settings_updated_at.sql
tenant_model_policies_tenant_model_policies_audit_trg.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/triggers/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/triggers/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/triggers/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/triggers/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
