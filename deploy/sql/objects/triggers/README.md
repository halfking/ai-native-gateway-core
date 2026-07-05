# Triggers

> 本目录包含 triggers 对象的 DDL 定义，从 `sql/objects/triggers/` 同步。

## 统计

- **文件数量**: 14
- **同步来源**: `sql/objects/triggers/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
api_keys_trg_notify_auto_route_apikeys.sql
credential_model_bindings_cmb_protect_manual_disable.sql
credential_model_bindings_trg_notify_auto_route_cmb.sql
credentials_trg_auto_fp_slot_limit_insert.sql
credentials_trg_check_credential_dates.sql
credentials_trg_notify_auto_route_creds.sql
key_applications_trg_key_applications_updated_at.sql
model_offers_model_offers_delete.sql
model_offers_model_offers_insert.sql
model_offers_model_offers_update.sql
provider_settings_trigger_provider_settings_updated_at.sql
request_logs_trg_update_api_key_model_cost.sql
routing_overrides_routing_overrides_audit_trg.sql
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
