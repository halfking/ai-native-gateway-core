# Tables

> 本目录包含 tables 对象的 DDL 定义，从 `sql/objects/tables/` 同步。

## 统计

- **文件数量**: 103
- **同步来源**: `sql/objects/tables/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
api_key_auto_profile.sql
api_key_model_cost.sql
api_keys.sql
applications.sql
auto_tune_audit.sql
background_tasks.sql
billing_orders.sql
candidate_failure_logs.sql
credential_capabilities.sql
credential_health_checks.sql
credential_model_bindings.sql
credential_model_call_history.sql
credential_model_index.sql
credential_model_peak_1m.sql
credential_model_stats_1m.sql
credential_model_weekly_peak.sql
credential_probe_model_log.sql
credential_quota_usage.sql
credential_quotas.sql
credentials.sql
credit_ledger.sql
internal_service_keys.sql
key_applications.sql
key_rpm_daily.sql
local_models.sql
local_runtimes.sql
maas_settings.sql
model_aliases.sql
model_credit_rates.sql
model_discovery_runs.sql
model_families.sql
model_fingerprints.sql
model_lifecycle_jobs.sql
model_offer_events.sql
model_offers_legacy.sql
model_probe_runs.sql
model_probe_state.sql
model_reconcile_log.sql
model_task_index.sql
models_canonical.sql
ops_model_offers_backup.sql
passive_probe_state.sql
price_change_events.sql
pricing_plans.sql
pricing_refresh_log.sql
provider_catalog.sql
provider_events.sql
provider_header_profiles.sql
provider_models.sql
provider_quality_rollup.sql
provider_scores.sql
provider_settings.sql
providers.sql
request_envelope.sql
request_logs_2026_04.sql
request_logs_2026_05.sql
request_logs_2026_06.sql
request_logs_2026_07.sql
request_logs_2026_08.sql
request_logs_default.sql
request_logs.sql
request_wal_2026_06.sql
request_wal_2026_07.sql
request_wal_bodies.sql
request_wal.sql
route_decisions.sql
routing_audit_log.sql
routing_decision_log.sql
routing_overrides_audit.sql
routing_overrides.sql
routing_policy.sql
schema_migration_audit.sql
schema_migrations.sql
security_audit_log.sql
session_memora_extraction_log.sql
session_titles.sql
settings_audit.sql
settings_kv.sql
sticky_sessions.sql
subscription_plans.sql
system_identity_pool.sql
tenant_credit_wallets.sql
tenant_model_policies_audit.sql
tenant_model_policies.sql
tenant_settings_kv.sql
tenant_subscriptions.sql
tenant_tool_policies.sql
tenants.sql
test_columnar_new.sql
token_audit_events.sql
tool_call_events.sql
tool_categories.sql
tool_registry.sql
tool_usage_stats.sql
topup_packages.sql
tuning_params.sql
tuning_proposals.sql
tuning_signals.sql
usage_ledger.sql
usage_minute.sql
users.sql
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
