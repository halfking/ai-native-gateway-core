# Functions

> 本目录包含 functions 对象的 DDL 定义，从 `sql/objects/functions/` 同步。

## 统计

- **文件数量**: 18
- **同步来源**: `sql/objects/functions/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
auto_set_fp_slot_limit.sql
check_credential_dates.sql
ensure_request_logs_partition_timestamp_with_time_zone.sql
get_current_tenant.sql
key_applications_set_updated_at.sql
model_offers_delete_trigger.sql
model_offers_insert_trigger.sql
model_offers_update_trigger.sql
model_probe_backoff_integer.sql
model_probe_backoff_v2_integer_timestamp_with_time_zone.sql
model_probe_passive_boost_bigint_text_timestamp_with_time_zone.sql
notify_auto_route_refresh.sql
recent_success_rate_bigint_text_integer_integer.sql
routing_overrides_audit_fn.sql
tenant_model_policies_audit_fn.sql
trg_cmb_protect_manual_disable.sql
update_api_key_model_cost.sql
update_provider_settings_updated_at.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/functions/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/functions/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/functions/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/functions/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
