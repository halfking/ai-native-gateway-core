# Policies

> 本目录包含 policies 对象的 DDL 定义，从 `sql/objects/policies/` 同步。

## 统计

- **文件数量**: 30
- **同步来源**: `sql/objects/policies/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
billing_orders_tenant_isolation_billing_orders.sql
billing_orders.sql
credit_ledger_tenant_isolation_credit_ledger.sql
credit_ledger.sql
model_probe_runs_tenant_isolation_model_probe_runs.sql
model_probe_runs.sql
request_logs_tenant_isolation_request_logs.sql
request_logs.sql
settings_audit_tenant_isolation_settings_audit.sql
settings_audit.sql
tenant_credit_wallets_tenant_isolation_tenant_credit_wallets.sql
tenant_credit_wallets.sql
tenant_model_policies_audit_tenant_isolation_tmp_audit.sql
tenant_model_policies_audit.sql
tenant_model_policies_tenant_isolation_tmp.sql
tenant_model_policies.sql
tenant_settings_kv_tenant_isolation_tenant_settings_kv.sql
tenant_settings_kv.sql
tenant_subscriptions_tenant_isolation_tenant_subscriptions.sql
tenant_subscriptions.sql
tenant_tool_policies_tenant_isolation_tenant_tool_policies.sql
tenant_tool_policies.sql
tool_call_events_tenant_isolation_tool_call_events.sql
tool_call_events.sql
tool_registry_tenant_isolation_tool_registry.sql
tool_registry.sql
tool_usage_stats_tenant_isolation_tool_usage_stats.sql
tool_usage_stats.sql
users_tenant_isolation_users.sql
users.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/policies/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/policies/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/policies/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/policies/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
