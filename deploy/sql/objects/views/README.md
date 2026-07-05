# Views

> 本目录包含 views 对象的 DDL 定义，从 `sql/objects/views/` 同步。

## 统计

- **文件数量**: 9
- **同步来源**: `sql/objects/views/`
- **同步方式**: 通过 `sync-objects.sh` 自动同步

## 文件列表

```
customer_cost_view.sql
model_cost_per_task_view.sql
model_offers.sql
tenant_model_policies_active.sql
tuning_signals_5m.sql
tuning_signals_daily.sql
v_fp_slot_policy.sql
v_recent_model_probe_failures.sql
v_routable_credential_models.sql
```

## 使用说明

### 查看对象定义

```bash
# 查看某个对象的定义
cat deploy/sql/objects/views/<object_name>.sql
```

### 应用对象

```bash
# 应用单个对象
psql "$DATABASE_URL" -f deploy/sql/objects/views/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/views/*.sql; do
  psql "$DATABASE_URL" -f "$f"
done
```

## 维护说明

- **不要手动编辑本目录**：所有更改应在 `sql/objects/views/` 进行
- **同步方式**：运行 `bash deploy/sql/sync-objects.sh` 重新同步
- **验证方式**：运行 `bash deploy/sql/verify-migration.sh` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
