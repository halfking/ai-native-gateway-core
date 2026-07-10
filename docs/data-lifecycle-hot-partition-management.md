# Hot 表数据迁移和分区清理功能

**日期**: 2026-07-10  
**功能**: 手动迁移 hot 表数据到分区表 + 删除旧分区

---

## 🎯 功能概述

新增两个管理端点，用于手动控制数据生命周期：

1. **手动迁移 hot 表** - 立即将 hot 表中的数据迁移到月度分区
2. **删除旧分区** - 删除指定的月度分区表以释放磁盘空间

---

## 📡 API 端点

### 1. 手动迁移 hot 表数据

**POST** `/api/admin/data-lifecycle/hot/promote`

**权限**: super_admin

**功能**: 
- 手动触发 hot 表数据迁移到分区表
- 不需要等待自动任务（每小时执行一次）
- 可以自定义保留时间和批次大小

**请求体**:
```json
{
  "table_name": "request_logs_hot",
  "retention_hours": 0,     // 0 = 立即迁移所有数据，168 = 保留 7 天
  "batch_size": 1000,       // 每批次迁移行数
  "max_batches": 0          // 0 = 不限制，10 = 最多执行 10 批
}
```

**支持的表名**:
- `request_logs_hot` - 请求日志（占用最大）
- `usage_ledger_hot` - 用量账本
- `request_wal_hot` - 请求变更日志
- `routing_decision_log_hot` - 路由决策日志
- `credential_model_index_hot` - 凭据模型索引
- `request_logs_bodies_hot` - 请求响应体
- `credit_ledger_hot` - 积分账本
- `tool_usage_stats_hot` - 工具使用统计

**响应体**:
```json
{
  "table_name": "request_logs_hot",
  "total_migrated": 16361,
  "batches_executed": 17,
  "started_at": "2026-07-10T19:45:00Z",
  "finished_at": "2026-07-10T19:45:12Z",
  "duration_seconds": 12,
  "status": "success",
  "message": "all eligible rows migrated",
  "warning": "已迁移数据，但 TOAST 空间尚未释放。请在【存储总览】页面对 request_logs_hot 执行 VACUUM FULL 以回收磁盘空间。"
}
```

**状态码**:
- `success` - 全部迁移成功
- `partial` - 部分迁移（超时或达到批次限制）
- `failed` - 迁移失败

### 2. 删除旧分区表

**POST** `/api/admin/data-lifecycle/partitions/drop`

**权限**: super_admin

**功能**:
- 删除指定的月度分区表及其所有数据
- 立即释放磁盘空间
- 不可恢复，需要确认

**请求体**:
```json
{
  "partition_name": "request_logs_2026_06",
  "confirm": true
}
```

**响应体**:
```json
{
  "partition_name": "request_logs_2026_06",
  "parent_table": "request_logs",
  "rows_deleted": 45232,
  "space_freed_bytes": 125829120,
  "space_freed_human": "120 MB",
  "executed_at": "2026-07-10T19:50:00Z",
  "status": "success",
  "message": "successfully dropped partition request_logs_2026_06 from request_logs"
}
```

**安全措施**:
1. 必须 `confirm: true` 才执行
2. 不能删除 hot 表（会返回错误）
3. 只能删除已存在的分区表
4. 操作会记录到日志

---

## 🔧 使用场景

### 场景 1：立即清空 request_logs_hot 以释放 TOAST 空间

**问题**: request_logs_hot 的 TOAST 表占用 7.5 GB，但只有 16,361 行数据。

**解决方案**:

1. **迁移所有数据到分区表**:
```bash
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/hot/promote' \
  -H 'Content-Type: application/json' \
  -H 'Cookie: llmgw_session=...' \
  -d '{
    "table_name": "request_logs_hot",
    "retention_hours": 0,
    "batch_size": 1000,
    "max_batches": 0
  }'
```

2. **执行 VACUUM FULL 回收空间**:
```bash
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/storage/tables/vacuum-full' \
  -H 'Content-Type: application/json' \
  -H 'Cookie: llmgw_session=...' \
  -d '{
    "schema": "public",
    "table": "request_logs_hot"
  }'
```

**预期效果**: request_logs_hot 从 7.7 GB 缩小到 < 100 MB

### 场景 2：删除 3 个月前的旧数据

**问题**: 数据库 8.4 GB，需要删除 2026-05 及之前的数据。

**解决方案**:

```bash
# 删除 2026-05 分区
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/partitions/drop' \
  -H 'Content-Type: application/json' \
  -H 'Cookie: llmgw_session=...' \
  -d '{
    "partition_name": "request_logs_2026_05",
    "confirm": true
  }'

# 删除 2026-04 分区
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/partitions/drop' \
  -H 'Content-Type: application/json' \
  -H 'Cookie: llmgw_session=...' \
  -d '{
    "partition_name": "request_logs_2026_04",
    "confirm": true
  }'
```

### 场景 3：定期维护（每周一次）

```bash
# 1. 迁移超过 7 天的数据
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/hot/promote' \
  -H 'Content-Type: application/json' \
  -d '{
    "table_name": "request_logs_hot",
    "retention_hours": 168,
    "batch_size": 1000
  }'

# 2. VACUUM 回收空间
curl -X POST 'https://llm.kxpms.cn/api/admin/data-lifecycle/storage/tables/vacuum' \
  -H 'Content-Type: application/json' \
  -d '{
    "schema": "public",
    "table": "request_logs_hot"
  }'
```

---

## ⚠️ 注意事项

### request_logs_hot 的特殊性

1. **columnar 分区问题**: 
   - 2026-07 和 2026-08 分区使用 columnar 存储
   - columnar 不支持 `INSERT ... ON CONFLICT`
   - 迁移时会出现警告但数据会保留在 hot 表
   - 解决方案：等待下个月自动创建 heap 分区，或手动修改分区为 heap

2. **TOAST 空间**:
   - 迁移数据后，TOAST 表不会自动缩小
   - 必须执行 `VACUUM FULL` 才能回收磁盘空间
   - `VACUUM FULL` 会锁表，建议低峰期执行

3. **大字段清理**:
   - 考虑先清理 request_body/response_body 大字段
   - 使用 `/api/admin/data-lifecycle/blobs/cleanup/execute`
   - 可以只清理大字段，保留元数据

### 删除分区的风险

1. **不可恢复**: 删除后数据无法恢复，除非有备份
2. **业务影响**: 确认没有查询会访问该分区的数据
3. **建议保留期**: 至少保留 3 个月的数据

---

## 🔍 监控和验证

### 检查迁移进度

```sql
-- 查看 hot 表行数
SELECT COUNT(*), MIN(ts), MAX(ts) 
FROM request_logs_hot;

-- 查看分区表大小
SELECT 
    tablename,
    pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size
FROM pg_tables
WHERE tablename LIKE 'request_logs_2026%'
ORDER BY tablename;
```

### 检查空间释放

```sql
-- 数据库总大小
SELECT pg_size_pretty(pg_database_size('llm_gateway'));

-- request_logs_hot 大小明细
SELECT 
    pg_size_pretty(pg_table_size('request_logs_hot')) AS table_size,
    pg_size_pretty(pg_indexes_size('request_logs_hot')) AS indexes_size,
    pg_size_pretty(pg_total_relation_size('request_logs_hot') - pg_table_size('request_logs_hot') - pg_indexes_size('request_logs_hot')) AS toast_size;
```

---

## 📝 日志记录

所有操作都会记录到应用日志：

```
data-lifecycle: manual promote hot table start table=request_logs_hot retention_hours=0 batch_size=1000
data-lifecycle: manual promote batch complete table=request_logs_hot batch=1 migrated=1000 total=1000
data-lifecycle: manual promote complete table=request_logs_hot total_migrated=16361 batches=17 status=success

data-lifecycle: dropping partition partition=request_logs_2026_06 parent_table=request_logs rows=45232 size=120 MB
data-lifecycle: partition dropped partition=request_logs_2026_06 rows_deleted=45232 space_freed=120 MB
```

---

## 🎯 前端集成建议

在 `https://llm.kxpms.cn/admin/data-lifecycle` 页面添加：

### 1. "Hot 表管理"卡片

- 显示各 hot 表的大小和行数
- "立即迁移"按钮
- 可选保留时间（0 小时/7 天/30 天）
- 迁移后提示执行 VACUUM FULL

### 2. "分区管理"卡片

- 列出所有分区（按月份）
- 显示每个分区的大小、行数、创建时间
- "删除"按钮（需要二次确认）
- 标记建议删除的分区（> 3 个月）

---

**作者**: OpenCode Agent  
**日期**: 2026-07-10
