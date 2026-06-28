# 分区归档运维手册

## 日常运维

### 1. 每周检查

**检查可归档分区数量**：
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions | \
  jq '.[] | {table: .table_name, archivable: .archivable_count, archived: .archived_count, total: .total_partitions}'
```

**健康指标**：
- `archivable_count` < 3：正常
- `archivable_count` 3-5：需要关注
- `archivable_count` > 5：需要手动干预

### 2. 手动归档流程

#### 步骤 1：查看可归档分区
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions | \
  jq '.[] | select(.archivable_count > 0) | {
    table: .table_name, 
    archivable_partitions: [.partitions[] | select(.can_archive==true) | .partition_name]
  }'
```

#### 步骤 2：试运行归档（重要！）
```bash
# 替换为实际的月份
MONTH="2026-03"
TABLE="request_logs"

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"archive_month\":\"$MONTH\",\"dry_run\":true}" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive | jq .
```

**检查输出**：
- `status: "dry_run"` - 可以执行
- `rows_migrated` - 将要迁移的行数
- `status: "skipped"` - 分区不存在或已归档

#### 步骤 3：执行归档
确认无误后，执行实际归档：
```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"archive_month\":\"$MONTH\",\"dry_run\":false}" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive | jq .
```

**预期结果**：
```json
{
  "status": "success",
  "table_name": "request_logs",
  "archive_month": "2026-03",
  "rows_migrated": 1234567,
  "partition_dropped": true,
  "message": "Successfully migrated 1234567 rows to columnar storage"
}
```

#### 步骤 4：验证归档结果
```bash
# 检查归档表中的数据
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT 
    schemaname, tablename, 
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size
FROM pg_tables 
WHERE tablename LIKE '%archive%' 
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
"

# 检查源分区是否已删除
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT tablename 
FROM pg_tables 
WHERE tablename = 'request_logs_${MONTH//-/_}';
"
# 应该返回 0 行
```

### 3. 批量归档（多个月份）

当有多个月份需要归档时：
```bash
# 定义要归档的月份列表
MONTHS='["2026-02","2026-03","2026-04"]'
TABLE="request_logs"

# 试运行
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"months\":$MONTHS,\"dry_run\":true}" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive-batch | jq .

# 确认后执行
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"months\":$MONTHS,\"dry_run\":false}" \
  https://llmgo.kxpms.cn/api/admin/data-lifecycle/partitions/archive-batch | jq .
```

### 4. 空间回收

归档后需要执行 VACUUM 来真正释放磁盘空间：

```bash
# 在低峰期执行（会锁表）
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
VACUUM FULL ANALYZE request_logs;
VACUUM FULL ANALYZE request_wal;
"

# 或者使用常规 VACUUM（不锁表，但释放较少）
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
VACUUM ANALYZE request_logs;
VACUUM ANALYZE request_wal;
"
```

**建议时间**：
- 常规 VACUUM：任何时候
- VACUUM FULL：凌晨 2-4 点（低峰期）

## 故障处理

### 场景 1：归档失败 - 函数不存在

**错误信息**：`archive function not available`

**解决方案**：
```bash
# 检查函数是否存在
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "\df archive_request_*"

# 如果不存在，应用迁移
psql -h $DB_HOST -U $DB_USER -d llm_gateway -f db/migrations/305_partition_archive_functions.sql
```

### 场景 2：归档超时

**错误信息**：请求超时或 504 Gateway Timeout

**原因**：分区数据量太大

**解决方案**：
```bash
# 1. 检查分区大小
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT 
    tablename,
    pg_size_pretty(pg_total_relation_size('public.'||tablename)) as size,
    (SELECT reltuples::bigint FROM pg_class WHERE relname = tablename) as estimated_rows
FROM pg_tables
WHERE tablename LIKE 'request_logs_2026_%'
ORDER BY tablename;
"

# 2. 如果分区过大（> 10GB），考虑在数据库中直接执行
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT * FROM archive_request_logs('2026-03-01'::date);
"

# 3. 监控执行进度
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT pid, state, query_start, now() - query_start as duration, query 
FROM pg_stat_activity 
WHERE query LIKE '%archive_%' OR query LIKE '%INSERT INTO%archive%';
"
```

### 场景 3：空间未释放

**问题**：归档后磁盘空间没有减少

**原因**：需要执行 VACUUM

**解决方案**：
```bash
# 检查死元组
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT 
    schemaname, tablename,
    n_dead_tup,
    n_live_tup,
    round(n_dead_tup * 100.0 / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_pct
FROM pg_stat_user_tables
WHERE schemaname = 'public' AND tablename IN ('request_logs', 'request_wal')
ORDER BY n_dead_tup DESC;
"

# 执行 VACUUM（低峰期）
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
VACUUM FULL ANALYZE request_logs;
"

# 检查表大小变化
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT 
    tablename,
    pg_size_pretty(pg_total_relation_size('public.'||tablename)) as total_size
FROM pg_tables
WHERE tablename IN ('request_logs', 'request_wal', 'request_logs_archive', 'request_wal_archive')
ORDER BY tablename;
"
```

### 场景 4：权限问题

**错误信息**：403 Forbidden

**原因**：
1. Token 不是 super_admin
2. Token 已过期

**解决方案**：
```bash
# 1. 验证 token 角色
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgo.kxpms.cn/api/admin/whoami | jq .

# 应该看到 role: "super_admin"

# 2. 如果不是，使用 super_admin token
export ADMIN_TOKEN="your_super_admin_token"
```

### 场景 5：columnar 扩展问题

**错误信息**：`access method "columnar" does not exist`

**解决方案**：
```bash
# 检查扩展
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
SELECT * FROM pg_extension WHERE extname LIKE '%columnar%';
"

# 如果不存在，安装 Citus columnar
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
CREATE EXTENSION IF NOT EXISTS citus_columnar;
"
```

## 监控脚本

保存为 `monitor-partition-archive.sh`：

```bash
#!/bin/bash
# Partition archive monitoring script

ADMIN_TOKEN="${ADMIN_TOKEN}"
API_BASE_URL="${API_BASE_URL:-https://llmgo.kxpms.cn}"

echo "=========================================="
echo "Partition Archive Status - $(date)"
echo "=========================================="

# Get partition status
RESPONSE=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$API_BASE_URL/api/admin/data-lifecycle/partitions")

# Parse and display
echo "$RESPONSE" | jq -r '.[] | 
"
Table: \(.table_name)
  Total Partitions: \(.total_partitions)
  Archived: \(.archived_count)
  Archivable: \(.archivable_count)
  Total Size: \(.total_size_human)
  ---"'

# Alert if archivable count is high
ARCHIVABLE=$(echo "$RESPONSE" | jq '[.[].archivable_count] | add')

if [ "$ARCHIVABLE" -gt 5 ]; then
    echo ""
    echo "⚠️  WARNING: $ARCHIVABLE partitions are archivable"
    echo "Consider running manual archive operation"
elif [ "$ARCHIVABLE" -gt 3 ]; then
    echo ""
    echo "ℹ️  INFO: $ARCHIVABLE partitions are archivable"
fi

echo "=========================================="
```

使用方法：
```bash
chmod +x monitor-partition-archive.sh
export ADMIN_TOKEN=xxx
./monitor-partition-archive.sh
```

## 性能优化建议

### 1. 归档时间选择
- **最佳**：凌晨 2-4 点（低峰期）
- **可接受**：晚上 8 点后
- **避免**：白天业务高峰期

### 2. 批量归档策略
- 一次归档 3-5 个月份
- 避免一次归档过多（> 10 个月）
- 每次归档后检查系统负载

### 3. VACUUM 策略
- 每次归档后执行常规 VACUUM
- 每月执行一次 VACUUM FULL（低峰期）
- 使用 pg_repack 代替 VACUUM FULL（不锁表）

## 数据恢复

如果需要恢复已归档的数据：

```sql
-- 查看归档分区中的数据
SELECT COUNT(*) FROM request_logs_archive_2026_03;

-- 如果需要将数据移回主表（不推荐）
-- 1. 先创建目标分区
SELECT ensure_request_logs_partition('2026-03-01'::timestamp);

-- 2. 插入数据
INSERT INTO request_logs 
SELECT * FROM request_logs_archive_2026_03;

-- 3. 删除归档分区
DROP TABLE request_logs_archive_2026_03;
```

**注意**：归档数据使用列存储，查询性能与普通表不同。

## 联系支持

如遇到无法解决的问题：
1. 收集错误日志：`tail -1000 /var/log/llm-gateway.log > error.log`
2. 收集 API 响应：保存 curl 命令的完整输出
3. 收集数据库状态：执行上述检查 SQL 并保存结果
4. 联系开发团队，提供以上信息

## 参考文档

- 功能文档：`docs/data-lifecycle-partition-archive.md`
- 部署清单：`docs/DEPLOYMENT_CHECKLIST_PARTITION_ARCHIVE.md`
- 快速入门：`DATA_LIFECYCLE_PARTITION_README.md`
