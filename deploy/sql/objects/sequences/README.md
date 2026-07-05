# Sequences

> 本目录包含 sequences 对象的 DDL 定义，从 `sql/objects/sequences/` 同步。

## 统计

- **文件数量**: 113
- **同步来源**: `sql/objects/sequences/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
api_keys_id_seq.sql
api_keys_id.sql
applications_id_seq.sql
applications_id.sql
auto_tune_audit_id_seq.sql
auto_tune_audit_id.sql
background_tasks_id_seq.sql
background_tasks_id.sql
billing_orders_id_seq.sql
billing_orders_id.sql
candidate_failure_logs_id_seq.sql
candidate_failure_logs_id.sql
credential_capabilities_id_seq.sql
credential_capabilities_id.sql
credential_health_checks_id_seq.sql
credential_health_checks_id.sql
credential_model_bindings_id_seq.sql
credential_model_bindings_id.sql
credential_probe_model_log_id_seq.sql
credential_probe_model_log_id.sql
credential_quota_usage_id_seq.sql
credential_quota_usage_id.sql
credential_quotas_id_seq.sql
credential_quotas_id.sql
credentials_id_seq.sql
credentials_id.sql
credit_ledger_id_seq.sql
credit_ledger_id.sql
local_models_id_seq.sql
local_models_id.sql
local_runtimes_id_seq.sql
local_runtimes_id.sql
model_aliases_id_seq.sql
model_aliases_id.sql
model_discovery_runs_id_seq.sql
model_discovery_runs_id.sql
model_fingerprints_id_seq.sql
model_fingerprints_id.sql
model_lifecycle_jobs_id_seq.sql
model_lifecycle_jobs_id.sql
model_offer_events_id_seq.sql
model_offer_events_id.sql
model_offers_id_seq.sql
model_offers_legacy_id.sql
model_probe_runs_id_seq.sql
model_probe_runs_id.sql
model_reconcile_log_id_seq.sql
model_reconcile_log_id.sql
models_canonical_id_seq.sql
models_canonical_id.sql
ops_model_offers_backup_backup_id_seq.sql
ops_model_offers_backup_backup_id.sql
price_change_events_id_seq.sql
price_change_events_id.sql
pricing_plans_id_seq.sql
pricing_plans_id.sql
pricing_refresh_log_id_seq.sql
pricing_refresh_log_id.sql
provider_events_id_seq.sql
provider_events_id.sql
provider_header_profiles_id_seq.sql
provider_header_profiles_id.sql
provider_models_id_seq.sql
provider_models_id.sql
provider_scores_id_seq.sql
provider_scores_id.sql
provider_settings_id_seq.sql
provider_settings_id.sql
providers_id_seq.sql
providers_id.sql
request_logs_id_seq.sql
route_decisions_id_seq.sql
route_decisions_id.sql
routing_audit_log_id_seq.sql
routing_audit_log_id.sql
routing_overrides_audit_id_seq.sql
routing_overrides_audit_id.sql
routing_overrides_id_seq.sql
routing_overrides_id.sql
security_audit_log_id_seq.sql
security_audit_log_id.sql
settings_audit_id_seq.sql
settings_audit_id.sql
subscription_plans_id_seq.sql
subscription_plans_id.sql
tenant_model_policies_audit_id_seq.sql
tenant_model_policies_audit_id.sql
tenant_model_policies_id_seq.sql
tenant_model_policies_id.sql
tenant_subscriptions_id_seq.sql
tenant_subscriptions_id.sql
tenant_tool_policies_id_seq.sql
tenant_tool_policies_id.sql
token_audit_events_id_seq.sql
token_audit_events_id.sql
tool_call_events_id_seq.sql
tool_call_events_id.sql
tool_registry_id_seq.sql
tool_registry_id.sql
tool_usage_stats_id_seq.sql
tool_usage_stats_id.sql
topup_packages_id_seq.sql
topup_packages_id.sql
tuning_proposals_id_seq.sql
tuning_proposals_id.sql
tuning_signals_id_seq.sql
tuning_signals_id.sql
usage_ledger_id_seq.sql
usage_ledger_id.sql
users_id_seq.sql
users_id.sql
work_type_model_route_id_seq.sql
work_type_model_route_id.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/sequences/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/sequences/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/sequences/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/sequences/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
