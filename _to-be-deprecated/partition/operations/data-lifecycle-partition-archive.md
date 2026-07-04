# ⚠️ 本文件已废弃 / This File Is Deprecated

> **归档日期**: 2026-07-05
> **替代文档**: [`docs/partition/OPERATIONS_RUNBOOK.md`](../../docs/partition/OPERATIONS_RUNBOOK.md) 第 §8 节
> **原因**: 内容已合并到主运维手册（数据生命周期管理方案、API 端点、权限要求、监控告警）
> **保留原因**: 提供历史归档追溯

---

# 数据生命周期管理 - 分区表列存储归档功能

## 概述

本功能扩展了现有的数据生命周期管理系统，支持多个分区表的列存储归档管理。通过将历史数据从常规 heap 存储迁移到 Citus columnar 存储，可实现 15-40x 的压缩比，显著降低存储成本。

## 支持的表

目前支持以下分区表的自动归档：

| 表名 | 归档表 | 分区字段 | 归档函数 | 说明 |
|------|--------|----------|----------|------|
| `request_logs` | `request_logs_archive` | `ts` | `archive_request_logs()` | 请求日志主表 |
| `request_wal` | `request_wal_archive` | `created_at` | `archive_request_wal()` | 请求预写日志表 |

## 归档策略

- **自动归档触发**：每月 1-3 号，自动归档 2 个月前的分区
- **手动归档**：通过管理界面或 API 手动触发特定月份的归档
- **归档流程**：
  1. 创建 columnar 存储的归档分区
  2. 将数据从源分区迁移到归档分区（列式存储）
  3. 删除源分区，释放空间
  4. 执行 VACUUM 回收磁盘空间

## API 端点

### 1. 查询分区状态

**GET** `/api/admin/data-lifecycle/partitions`

返回所有支持的分区表及其分区的详细状态。

**响应示例**：
```json
[
  {
    "table_name": "request_logs",
    "description": "请求日志表（主表）",
    "total_partitions": 12,
    "archived_count": 8,
    "archivable_count": 2,
    "total_rows": 15000000,
    "total_size_bytes": 52428800000,
    "total_size_human": "49 GB",
    "has_archive_func": true,
    "archive_table_name": "request_logs_archive",
    "partitions": [
      {
        "partition_name": "request_logs_2026_04",
        "parent_table": "request_logs",
        "start_date": "2026-04-01T00:00:00Z",
        "end_date": "2026-05-01T00:00:00Z",
        "row_count": 1234567,
        "size_bytes": 4294967296,
        "size_human": "4096 MB",
        "is_archived": false,
        "is_columnar": false,
        "can_archive": true
      },
      {
        "partition_name": "request_logs_archive_2026_02",
        "parent_table": "request_logs_archive",
        "start_date": "2026-02-01T00:00:00Z",
        "end_date": "2026-03-01T00:00:00Z",
        "row_count": 2345678,
        "size_bytes": 134217728,
        "size_human": "128 MB",
        "is_archived": true,
        "is_columnar": true,
        "can_archive": false
      }
    ]
  },
  {
    "table_name": "request_wal",
    "description": "请求预写日志表",
    "total_partitions": 8,
    "archived_count": 5,
    "archivable_count": 1,
    "total_rows": 8500000,
    "total_size_bytes": 12884901888,
    "total_size_human": "12 GB",
    "has_archive_func": true,
    "archive_table_name": "request_wal_archive",
    "partitions": [...]
  }
]
```

### 2. 归档单个分区（需要 super_admin 权限）

**POST** `/api/admin/data-lifecycle/partitions/archive`

手动归档指定表的指定月份分区。

**请求体**：
```json
{
  "table_name": "request_logs",
  "archive_month": "2026-04",
  "dry_run": false
}
```

**参数说明**：
- `table_name`: 表名（`request_logs` 或 `request_wal`）
- `archive_month`: 归档月份，格式 `YYYY-MM`
- `dry_run`: 是否试运行（true 时仅检查，不实际执行）

**响应示例**：
```json
{
  "status": "success",
  "table_name": "request_logs",
  "archive_month": "2026-04",
  "rows_migrated": 1234567,
  "partition_dropped": true,
  "message": "Successfully migrated 1234567 rows to columnar storage"
}
```

**状态值**：
- `success`: 归档成功
- `skipped`: 分区不存在或已归档
- `error`: 归档失败
- `dry_run`: 试运行模式

### 3. 批量归档（需要 super_admin 权限）

**POST** `/api/admin/data-lifecycle/partitions/archive-batch`

批量归档多个月份的分区。

**请求体**：
```json
{
  "table_name": "request_logs",
  "months": ["2026-02", "2026-03", "2026-04"],
  "dry_run": false
}
```

**响应示例**：
```json
{
  "total_requested": 3,
  "results": [
    {
      "status": "success",
      "table_name": "request_logs",
      "archive_month": "2026-02",
      "rows_migrated": 1100000,
      "partition_dropped": true,
      "message": "Successfully migrated 1100000 rows to columnar storage"
    },
    {
      "status": "success",
      "table_name": "request_logs",
      "archive_month": "2026-03",
      "rows_migrated": 1200000,
      "partition_dropped": true,
      "message": "Successfully migrated 1200000 rows to columnar storage"
    },
    {
      "status": "skipped",
      "table_name": "request_logs",
      "archive_month": "2026-04",
      "rows_migrated": 0,
      "partition_dropped": false,
      "message": "Partition request_logs_2026_04 not found"
    }
  ]
}
```

## 使用场景

### 场景 1：查看可归档的分区

```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions
```

查看返回结果中 `can_archive: true` 的分区即可归档。

### 场景 2：试运行归档（推荐先执行）

```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "table_name": "request_logs",
    "archive_month": "2026-04",
    "dry_run": true
  }' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive
```

返回结果会显示将要迁移的行数，但不会实际执行。

### 场景 3：执行归档

确认无误后，将 `dry_run` 改为 `false` 执行实际归档：

```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "table_name": "request_logs",
    "archive_month": "2026-04",
    "dry_run": false
  }' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive
```

### 场景 4：批量归档多个月份

```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "table_name": "request_logs",
    "months": ["2026-01", "2026-02", "2026-03"],
    "dry_run": false
  }' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive-batch
```

## 自动化管理

### PartitionManager 后台服务

系统已集成 `bg.PartitionManager` 后台服务，自动执行以下任务：

1. **自动创建下月分区**：确保当前月和下月的分区存在
2. **自动归档旧分区**：每月 1-3 号自动归档 2 个月前的分区

启动代码（已集成到 `cmd/gateway/main.go`）：
```go
pm := bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
pm.Start(context.Background())
defer pm.Stop()
```

### 查看自动归档日志

```bash
# 查看归档操作日志
grep "partition_manager" /var/log/llm-gateway.log

# 查看最近的归档记录
grep "archive succeeded" /var/log/llm-gateway.log | tail -5
```

## 数据库迁移

### Migration 305

本功能依赖 Migration 305，该迁移创建了以下对象：

1. `request_wal_archive` 表（分区父表）
2. `archive_request_wal()` 函数
3. `ensure_request_wal_partition()` 函数

**应用迁移**：
```bash
# 迁移会在系统启动时自动执行
# 或手动执行：
psql -h $DB_HOST -U $DB_USER -d llm_gateway -f db/migrations/305_partition_archive_functions.sql
```

**回滚迁移**（仅在必要时）：
```bash
psql -h $DB_HOST -U $DB_USER -d llm_gateway -f db/migrations/305_partition_archive_functions.down.sql
```

## 监控和告警

### 关键指标

1. **归档率**：`archived_count / total_partitions`
2. **可归档分区数**：`archivable_count`（应保持较低，避免积压）
3. **压缩比**：归档前后的 `size_bytes` 对比
4. **归档延迟**：最老未归档分区的年龄

### Prometheus 指标（建议添加）

```go
// 可在 data_lifecycle_metrics.go 中添加
var (
	partitionArchivableCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_partition_archivable_count",
			Help: "Number of partitions eligible for archive",
		},
		[]string{"table"},
	)
	
	partitionArchivedRatio = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_partition_archived_ratio",
			Help: "Ratio of archived partitions to total partitions",
		},
		[]string{"table"},
	)
)
```

### 告警规则（Prometheus）

```yaml
groups:
  - name: data_lifecycle
    rules:
      - alert: PartitionArchiveBacklog
        expr: llmgw_partition_archivable_count > 3
        for: 24h
        labels:
          severity: warning
        annotations:
          summary: "Partition archive backlog detected"
          description: "Table {{ $labels.table }} has {{ $value }} partitions eligible for archive"
      
      - alert: PartitionArchiveStuck
        expr: llmgw_partition_archivable_count > 5
        for: 48h
        labels:
          severity: critical
        annotations:
          summary: "Partition archive appears stuck"
          description: "Table {{ $labels.table }} has {{ $value }} unarchived partitions for >48h"
```

## 权限要求

- **查询分区状态**：需要 `platform_ops` 或 `super_admin` 权限（通过 `h.admin()` 中间件）
- **执行归档操作**：需要 `super_admin` 权限（通过 `h.superAdmin()` 中间件）

原因：归档操作会删除源分区，属于高风险操作，需要最高权限。

## 故障排查

### 问题 1：归档失败，提示 "archive function not available"

**原因**：数据库中未创建归档函数或 Migration 305 未执行。

**解决**：
```bash
# 检查函数是否存在
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "\df archive_request_*"

# 应用迁移
psql -h $DB_HOST -U $DB_USER -d llm_gateway -f db/migrations/305_partition_archive_functions.sql
```

### 问题 2：归档后空间未释放

**原因**：PostgreSQL 需要 VACUUM 才能真正回收磁盘空间。

**解决**：
```sql
VACUUM FULL ANALYZE request_logs;
VACUUM FULL ANALYZE request_wal;
```

注意：`VACUUM FULL` 会锁表，建议在低峰期执行。

### 问题 3：columnar 扩展未安装

**错误信息**：`ERROR: access method "columnar" does not exist`

**解决**：
```sql
-- 检查扩展
\dx

-- 安装 Citus columnar 扩展
CREATE EXTENSION IF NOT EXISTS citus_columnar;
```

### 问题 4：列顺序不匹配错误

**错误信息**：`column "xxx" is of type boolean but expression is of type integer`

**原因**：源表和归档表的列顺序不一致。

**解决**：Migration 053/305 已解决此问题，使用显式列名列表而非 `SELECT *`。确保已应用最新迁移。

## 最佳实践

1. **定期监控**：每周检查一次可归档分区数量
2. **批量归档**：一次归档多个月份比逐个归档更高效
3. **先试运行**：执行实际归档前先使用 `dry_run: true` 验证
4. **低峰期操作**：大批量归档建议在低峰期执行
5. **验证数据**：归档后抽查归档表中的数据完整性
6. **备份策略**：归档操作会删除源分区，确保有备份策略

## 扩展支持更多表

如需支持更多分区表，只需：

1. **更新配置**（`admin/data_lifecycle_partition.go`）：
```go
var partitionedTables = []partitionedTableConfig{
	// ... 现有表 ...
	{
		TableName:        "new_table",
		ArchiveTableName: "new_table_archive",
		PartitionColumn:  "created_at",
		Description:      "新表描述",
		HasArchiveFunc:   false, // 先设为 false
	},
}
```

2. **创建数据库对象**（新建迁移文件）：
```sql
-- 创建归档表
CREATE TABLE new_table_archive (LIKE new_table) PARTITION BY RANGE (created_at);

-- 创建归档函数
CREATE OR REPLACE FUNCTION archive_new_table(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
-- ... 参考 archive_request_logs 或 archive_request_wal ...
$func$;
```

3. **更新配置**：将 `HasArchiveFunc` 改为 `true`

4. **重启服务**：新表即自动集成到管理界面

## 相关文档

- [数据生命周期管理方案](./data-lifecycle-management.md)
- [Migration 053: 列感知归档函数](../db/migrations/053_archive_request_logs_column_aware.sql)
- [Migration 305: request_wal 归档支持](../db/migrations/305_partition_archive_functions.sql)
- [PartitionManager 后台服务](../bg/partition_manager.go)

## 更新日志

- **2026-06-28**: 初始版本，支持 request_logs 和 request_wal 两张表
  - 新增 `/api/admin/data-lifecycle/partitions` 端点
  - 新增 `/api/admin/data-lifecycle/partitions/archive` 端点
  - 新增 `/api/admin/data-lifecycle/partitions/archive-batch` 端点
  - 新增 Migration 305 创建 request_wal 归档函数
