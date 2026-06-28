# 数据生命周期管理 - 分区表列存储归档功能实现总结

## 实现概述

本次更新为 https://llmgo.kxpms.cn/admin/data-lifecycle 数据生命周期管理界面添加了对多个分区表（request_logs、request_wal 等）的列存储归档管理功能。

## 新增文件

### 1. 后端代码

#### `admin/data_lifecycle_partition.go` (新增)
核心功能文件，包含：
- **分区表配置管理**：`partitionedTableConfig` 结构和 `partitionedTables` 配置列表
- **分区信息查询**：`handleDataLifecyclePartitions()` - GET 端点，返回所有分区表状态
- **单分区归档**：`handleDataLifecycleArchivePartition()` - POST 端点，归档指定月份
- **批量归档**：`handleDataLifecycleArchiveBatch()` - POST 端点，批量归档多个月份
- **辅助函数**：分区边界解析、列存储状态检查等

**关键特性**：
- 支持 dry-run 模式，安全预览归档操作
- 自动检测分区是否可归档（>= 2 个月前）
- 显式列名映射，避免列顺序不匹配问题

#### `admin/data_lifecycle_partition_test.go` (新增)
单元测试文件，覆盖：
- 分区边界解析逻辑
- 表配置验证
- 归档请求验证
- 辅助函数测试

**测试结果**：所有测试通过 ✓

### 2. 数据库迁移

#### `db/migrations/305_partition_archive_functions.sql` (新增)
创建 request_wal 表的归档支持：
- `request_wal_archive` 表（分区父表）
- `archive_request_wal()` 函数（列感知归档）
- `ensure_request_wal_partition()` 函数（自动创建分区）

#### `db/migrations/305_partition_archive_functions.down.sql` (新增)
回滚迁移的逆向脚本。

### 3. 文档

#### `docs/data-lifecycle-partition-archive.md` (新增)
完整的功能文档，包含：
- API 端点说明和示例
- 使用场景和最佳实践
- 故障排查指南
- 监控告警建议

## 修改文件

### `admin/handler.go`
**修改位置**：`RegisterRoutes()` 方法（第 351-353 行）

**添加的路由**：
```go
// Partition management endpoints (2026-06-28)
mux.HandleFunc("/api/admin/data-lifecycle/partitions", admin(h.handleDataLifecyclePartitions))
mux.HandleFunc("/api/admin/data-lifecycle/partitions/archive", h.superAdmin(h.handleDataLifecycleArchivePartition))
mux.HandleFunc("/api/admin/data-lifecycle/partitions/archive-batch", h.superAdmin(h.handleDataLifecycleArchiveBatch))
```

**权限设计**：
- 查询端点：`admin()` - 需要 platform_ops 或 super_admin
- 归档端点：`superAdmin()` - 仅 super_admin（高风险操作）

## API 端点

### 1. GET /api/admin/data-lifecycle/partitions
**功能**：查询所有分区表及其分区的详细状态

**返回字段**：
- `table_name`: 表名
- `total_partitions`: 总分区数
- `archived_count`: 已归档分区数
- `archivable_count`: 可归档分区数（>= 2 个月前）
- `partitions[]`: 分区详情列表
  - `partition_name`: 分区名称
  - `start_date/end_date`: 分区时间范围
  - `row_count`: 行数
  - `size_bytes/size_human`: 大小
  - `is_columnar`: 是否使用列存储
  - `can_archive`: 是否可归档

### 2. POST /api/admin/data-lifecycle/partitions/archive
**功能**：归档指定表的指定月份分区（需 super_admin）

**请求参数**：
- `table_name`: 表名（request_logs 或 request_wal）
- `archive_month`: 归档月份（YYYY-MM 格式）
- `dry_run`: 是否试运行（推荐先试运行）

**返回字段**：
- `status`: success/skipped/error/dry_run
- `rows_migrated`: 迁移的行数
- `partition_dropped`: 是否删除了源分区
- `message`: 操作消息

### 3. POST /api/admin/data-lifecycle/partitions/archive-batch
**功能**：批量归档多个月份（需 super_admin）

**请求参数**：
- `table_name`: 表名
- `months[]`: 月份数组（YYYY-MM 格式）
- `dry_run`: 是否试运行

**返回字段**：
- `total_requested`: 请求总数
- `results[]`: 每个月份的归档结果

## 支持的表

| 表名 | 归档表 | 分区字段 | 归档函数 | 状态 |
|------|--------|----------|----------|------|
| `request_logs` | `request_logs_archive` | `ts` | `archive_request_logs()` | ✓ 已支持（Migration 053） |
| `request_wal` | `request_wal_archive` | `created_at` | `archive_request_wal()` | ✓ 新增（Migration 305） |

**扩展性**：通过更新 `partitionedTables` 配置和创建对应的归档函数，可轻松添加更多表。

## 技术亮点

### 1. 列感知归档
使用 `information_schema.columns` 动态构建列名列表，避免源表和归档表列顺序不一致导致的数据类型错误。

```sql
SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
FROM information_schema.columns a
JOIN information_schema.columns r ON a.column_name = r.column_name
WHERE a.table_name = 'request_logs_archive' AND r.table_name = 'request_logs_2026_04'
```

### 2. Columnar 存储
利用 Citus columnar 扩展，归档数据使用列式存储，压缩比可达 15-40x：

```sql
CREATE TABLE request_logs_archive_2026_04 
PARTITION OF request_logs_archive 
FOR VALUES FROM ('2026-04-01') TO ('2026-05-01') 
USING columnar;
```

### 3. 安全的试运行模式
支持 `dry_run` 参数，在不实际执行归档的情况下预览影响：
- 检查分区是否存在
- 统计待迁移行数
- 返回预期结果

### 4. 分区边界自动解析
从 PostgreSQL partition bounds 字符串中提取日期范围：
```go
parsePartitionBounds("FOR VALUES FROM ('2026-04-01') TO ('2026-05-01')")
// 返回: startDate=2026-04-01, endDate=2026-05-01
```

### 5. 自动归档逻辑
判断分区是否可归档的条件：
```go
canArchive := !isArchived && endDate.Before(twoMonthsAgo) && hasArchiveFunc
```

## 与现有系统集成

### PartitionManager 后台服务
本功能与现有的 `bg.PartitionManager` 完美集成：
- PartitionManager 每天自动运行，归档 2 个月前的分区
- 管理界面提供手动触发能力（应急场景）
- 两者调用相同的数据库函数（`archive_request_logs()` / `archive_request_wal()`）

### 数据生命周期管理体系
本功能是现有数据生命周期管理的扩展：
- 现有功能：统计、清理预览（`data_lifecycle.go`）
- 新增功能：分区级别的归档管理（`data_lifecycle_partition.go`）
- 共享权限和认证机制

## 部署步骤

### 1. 应用数据库迁移
```bash
# 迁移会在服务启动时自动执行
# 或手动执行：
psql -h $DB_HOST -U $DB_USER -d llm_gateway \
  -f db/migrations/305_partition_archive_functions.sql
```

### 2. 验证迁移
```sql
-- 检查函数是否创建
\df archive_request_*

-- 应该看到：
-- archive_request_logs(date)
-- archive_request_wal(date)
```

### 3. 重启服务
```bash
# 重启 llm-gateway-go 服务
systemctl restart llm-gateway-go
```

### 4. 验证 API
```bash
# 查询分区状态
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions
```

### 5. 试运行归档
```bash
# 试运行归档（推荐先执行）
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive
```

## 测试情况

### 单元测试
```bash
go test ./admin -run "TestPartition" -v
```

**测试覆盖**：
- ✓ 分区边界解析
- ✓ 表配置验证
- ✓ 归档请求验证
- ✓ 辅助函数逻辑

**结果**：所有测试通过

### 编译验证
```bash
go build -o /tmp/llm-gateway-test ./cmd/gateway
```

**结果**：编译成功，无错误

## 预期效果

### 存储优化
- **压缩比**：15-40x（根据数据特征）
- **示例**：4GB 的分区归档后可能只占用 100-200MB

### 性能影响
- **查询性能**：归档表为 columnar 存储，适合分析查询，但不适合单行查询
- **写入性能**：归档操作会锁表，建议在低峰期执行
- **归档耗时**：100 万行约需 1-2 分钟

### 空间回收
- 归档后需执行 `VACUUM FULL` 才能真正回收磁盘空间
- PartitionManager 会自动删除源分区，但不会自动 VACUUM

## 监控建议

### 关键指标
1. **可归档分区数**（`archivable_count`）
   - 告警阈值：> 3 持续 24h
   - 原因：可能是自动归档失败

2. **归档率**（`archived_count / total_partitions`）
   - 健康值：> 60%
   - 原因：大部分历史数据应已归档

3. **归档表大小增长**
   - 监控 `request_logs_archive` 和 `request_wal_archive` 的总大小
   - 预期：每月增加但增速应低于主表（因为有压缩）

### 日志监控
```bash
# 监控自动归档日志
tail -f /var/log/llm-gateway.log | grep "partition_manager"

# 查看归档成功记录
grep "archive succeeded" /var/log/llm-gateway.log
```

## 后续优化建议

### 短期（1-2 周）
1. 在 Grafana 中添加分区归档监控面板
2. 配置 Prometheus 告警规则
3. 编写运维 runbook

### 中期（1-2 月）
1. 支持更多分区表（如有需要）
2. 添加归档历史记录表（audit log）
3. 前端管理界面（Vue.js）

### 长期（3-6 月）
1. 支持自定义归档策略（不同表不同保留期）
2. 归档数据的查询代理（统一查询主表+归档表）
3. 冷存储集成（S3/OSS）

## 相关文档

- [功能文档](../docs/data-lifecycle-partition-archive.md)
- [原始需求](../docs/data-lifecycle-management.md)
- [Migration 053](../db/migrations/053_archive_request_logs_column_aware.sql)
- [Migration 305](../db/migrations/305_partition_archive_functions.sql)

## 变更统计

- **新增文件**：4 个
  - 1 个核心功能文件
  - 1 个测试文件
  - 2 个迁移文件（up/down）
  
- **修改文件**：1 个
  - `admin/handler.go`（添加路由）
  
- **新增文档**：1 个
  - `docs/data-lifecycle-partition-archive.md`

- **新增 API 端点**：3 个
  - GET `/api/admin/data-lifecycle/partitions`
  - POST `/api/admin/data-lifecycle/partitions/archive`
  - POST `/api/admin/data-lifecycle/partitions/archive-batch`

- **代码行数**：约 600 行（含注释和测试）

## 作者与日期

- **实现日期**：2026-06-28
- **功能状态**：✓ 已完成并测试
- **部署状态**：待部署到生产环境
