# 分区表运维手册

**日期**: 2026-07-05
**版本**: 1.0

## 修订历史

| 版本 | 日期 | 变更 | 作者 |
|------|------|------|------|
| 1.0 | 2026-07-05 | 初始版本（合并自 OPERATIONS_GUIDE_PARTITION_ARCHIVE / data-lifecycle-partition-archive / data-lifecycle-management / data-lifecycle-partition-implementation-summary 四份旧文档） | Infrastructure Team |

---

## 1. 故障排查流程

### 1.1 快速诊断

当出现分区表相关问题时，按以下顺序检查：

```bash
# 1. 运行健康检查（5 分钟内完成初步诊断）
./scripts/partition/check-partition-health.sh --env 71

# 2. 查看后台调度器日志
grep "partition_manager" __SERVER_PATH_6__.log | tail -50

# 3. 验证架构对齐
./scripts/partition/verify-partition-alignment.sh --env 71
```

### 1.2 常见故障与解决方案

---

## 2. 故障场景与解决方案

### 场景 A：写入失败 - 分区约束冲突

**错误信息**：
```
ERROR: new row for relation "request_logs_default" violates partition constraint
SQLSTATE: 23514
```

**原因**：当月分区仍为 ATTACHED，DEFAULT 分区动态排除当月时间范围

**排查步骤**：
```bash
# 1. 检查当月分区状态
psql -c "
  SELECT c.relname,
         CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
  FROM pg_class c
  LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
  WHERE c.relname LIKE 'request_logs_2026_07';
"

# 2. 预期结果
# 如果显示 ATTACHED，说明 migration 337 未应用
```

**解决方案**：
```bash
# 应用 migration 337
psql < db/migrations/337_detach_current_future_partitions.sql

# 验证
./scripts/partition/check-partition-health.sh --env 71
```

**预防措施**：
- 确保 migration 337 在所有环境已应用
- 配置 Prometheus 告警：PartitionConstraintViolations

---

### 场景 B：写入失败 - Columnar 不支持 UPSERT

**错误信息**：
```
ERROR: ON CONFLICT is not supported for columnar tables
SQLSTATE: 0A000
```

**原因**：写入代码尝试对 Columnar 分区执行 UPSERT

**排查步骤**：
```bash
# 1. 检查错误发生在哪个表
# 查看应用日志中的 request_id

# 2. 确认写入目标
grep "INSERT INTO" telemetry/client.go | head -5
# 预期：INSERT INTO request_logs_default（不是父表）
```

**解决方案**：
1. 确认代码已改为写入 `*_default`
2. 如果 `*_default` 是 Columnar，需要重建为 heap：
```sql
-- 检查存储类型
SELECT c.relname, am.amname
FROM pg_class c
JOIN pg_am am ON am.oid = c.relam
WHERE c.relname = 'xxx_default';

-- 如果是 columnar，需要迁移数据重建
ALTER TABLE xxx_default SET USING heap;
```

**预防措施**：
- 代码审查确保所有写入指向 `*_default`
- 测试环境验证 UPSERT 功能

---

### 场景 C：`*_default` 表过大

**错误信息**：
```
# Prometheus 告警
ALERT: PartitionDefaultTableSizeWarning
```

**原因**：
1. promote 函数停止工作
2. 写入量激增
3. 保留窗口设置过长

**排查步骤**：
```bash
# 1. 检查表大小
./scripts/partition/check-partition-health.sh --env 71 --report-only

# 2. 检查 promote 函数执行日志
grep "promote failed\|promote batch" __SERVER_PATH_6__.log | tail -20

# 3. 检查月度分区是否存在
psql -c "SELECT count(*) FROM pg_class WHERE relname LIKE 'request_logs_2026_%';"
```

**解决方案**：
```bash
# 1. 手动触发 promote（先小批次测试）
./scripts/partition/manual-promote-default.sh \
  --table request_logs \
  --retention 7 \
  --batch 1000

# 2. 如果成功，扩大处理
./scripts/partition/manual-promote-default.sh --all

# 3. 检查月度分区是否需要创建
# 如果 promote 函数报告 "target partition does not exist"
# 需要先创建月度分区
SELECT ensure_request_logs_partition(now());
```

**预防措施**：
- 配置 PartitionDefaultTableSizeWarning 告警（5GB 警告，10GB 严重）
- 确保 promote 函数每 1 小时执行

---

### 场景 D：Promote 函数执行失败

**错误信息**：
```
ERROR: duplicate key value violates unique constraint
SQLSTATE: 23505
```

**原因**：
1. 迁移中断后重复执行
2. 目标分区已有数据

**排查步骤**：
```bash
# 1. 检查目标分区数据
psql -c "SELECT count(*) FROM request_logs_2026_07;"

# 2. 检查 _default 是否还有遗留数据
psql -c "SELECT count(*) FROM request_logs_default 
        WHERE ts < now() - interval '7 days';"
```

**解决方案**：
```sql
-- 使用 ON CONFLICT DO NOTHING 避免重复
WITH del AS (
    DELETE FROM request_logs_default
    WHERE ts < now() - interval '7 days'
    RETURNING *
),
ins AS (
    INSERT INTO request_logs
    SELECT * FROM del
    ON CONFLICT DO NOTHING  -- 幂等保证
    RETURNING 1
)
SELECT count(*) FROM ins;
```

**预防措施**：
- promote 函数已内置 `ON CONFLICT DO NOTHING`
- 监控 `partition_manager_promote_errors_total` 指标

---

### 场景 E：查询返回数据不完整

**现象**：
- 查询父表 `SELECT * FROM request_logs WHERE ts >= '2026-07-01'`
- 结果不包含 2026-07 数据

**原因**：
- 2026-07 分区已 DETACHED
- 父表查询不包含 DETACHED 分区

**排查步骤**：
```bash
# 1. 检查分区状态
psql -c "
  SELECT c.relname,
         CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
  FROM pg_class c
  LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
  WHERE c.relname LIKE 'request_logs_2026%';
"

# 2. 确认使用了 VIEW
psql -c "SELECT count(*) FROM request_logs_with_current_month 
        WHERE ts >= '2026-07-01';"
```

**解决方案**：
1. **短期**：使用 VIEW 替代父表查询
```sql
SELECT * FROM request_logs_with_current_month
WHERE ts >= '2026-07-01';
```

2. **长期**：修改应用代码使用 VIEW

**预防措施**：
- 创建 `*_with_current_month` VIEW
- 代码审查确保跨月查询使用 VIEW

---

### 场景 F：Promote 函数未执行

**现象**：
- `*_default` 表持续增长
- 日志中无 promote 相关输出

**原因**：
1. `bg/partition_manager.go` 进程未运行
2. `promoteInterval` 设置为 0

**排查步骤**：
```bash
# 1. 检查进程状态
ps aux | grep partition_manager

# 2. 检查配置
grep "promoteInterval\|PromoteInterval" bg/partition_manager.go
```

**解决方案**：
```bash
# 1. 重启服务
systemctl restart llm-gateway

# 2. 手动触发 promote
./scripts/partition/manual-promote-default.sh --all
```

**预防措施**：
- 配置 PartitionPromoteLag 告警（2 小时未执行）
- 监控进程健康状态

---

## 3. 紧急操作

### 3.1 完全重建 `*_default`

如果 `*_default` 严重损坏：

```sql
-- 1. 备份数据
CREATE TABLE request_logs_default_backup AS 
SELECT * FROM request_logs_default;

-- 2. 重建表
DROP TABLE request_logs_default;
CREATE TABLE request_logs_default PARTITION OF request_logs DEFAULT;

-- 3. 恢复数据（分批）
INSERT INTO request_logs_default
SELECT * FROM request_logs_default_backup
WHERE ts >= now() - interval '7 days';

-- 4. 验证
SELECT count(*) FROM request_logs_default;
```

### 3.2 紧急清理大量积压

```bash
# 强制迁移所有积压数据
./scripts/partition/manual-promote-default.sh \
  --all \
  --retention 1 \
  --batch 10000
```

### 3.3 临时允许写入父表（不推荐，仅紧急）

```sql
-- 临时 ATTACH 当月分区（允许父表写入）
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07
  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- 记录操作原因
-- ... 紧急操作完成 ...

-- 恢复 DETACHED
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;
```

---

## 4. 诊断命令速查

### 4.1 分区状态
```sql
-- 检查所有分区状态
SELECT c.relname,
       CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END AS status,
       pg_get_expr(c.relpartbound, c.oid) AS bounds
FROM pg_class c
LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE c.relname LIKE 'request_logs_%'
ORDER BY c.relname;
```

### 4.2 表大小
```sql
SELECT 
  schemaname || '.' || tablename AS table_name,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_stat_user_tables
WHERE tablename LIKE '%_default'
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

### 4.3 写入统计
```sql
SELECT 
  relname,
  n_tup_ins AS inserts,
  n_tup_upd AS updates,
  n_tup_del AS deletes,
  n_live_tup AS live_rows,
  n_dead_tup AS dead_rows
FROM pg_stat_user_tables
WHERE relname LIKE '%_default'
  OR relname LIKE '%_2026_%';
```

### 4.4 Promote 函数测试
```sql
SELECT promote_request_logs_default_batch('7 days'::interval, 100);
-- 返回移动的行数
-- 0 = 无更多数据
```

---

## 5. 联系人和升级路径

| 严重性 | 联系 | 升级时间 |
|--------|------|---------|
| P0 (全局不可用) | 值班 SRE | 15 分钟 |
| P1 (部分功能受损) | Team Lead | 1 小时 |
| P2 (性能下降) | Infrastructure | 4 小时 |
| P3 (预防性) | GitHub Issue | 工作日 |

---

## 6. 合并来源

本手册于 2026-07-05 合并以下旧文档：

- `docs/OPERATIONS_GUIDE_PARTITION_ARCHIVE.md`（已迁移到 `_to-be-deprecated/`）— HTTP API 归档流程、批量归档、空间回收、监控脚本、数据恢复
- `docs/data-lifecycle-partition-archive.md`（已迁移到 `_to-be-deprecated/`）— 数据生命周期管理方案、API 端点、权限要求、监控告警
- `docs/data-lifecycle-management.md`（已迁移到 `_to-be-deprecated/`）— 三温数据模型、字段裁剪策略、Parquet/JSONL/SQL 归档方案、备份方案
- `docs/data-lifecycle-partition-implementation-summary.md`（已迁移到 `_to-be-deprecated/`）— 实施总结、技术亮点、测试情况

---

## 7. HTTP API 归档操作（合并自 OPERATIONS_GUIDE_PARTITION_ARCHIVE.md）

### 7.1 查看可归档分区

```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://__DOMAIN_8__/api/admin/data-lifecycle/partitions | \
  jq '.[] | {table: .table_name, archivable: .archivable_count, archived: .archived_count, total: .total_partitions}'
```

**健康指标**：
- `archivable_count` < 3：正常
- `archivable_count` 3-5：需要关注
- `archivable_count` > 5：需要手动干预

### 7.2 手动归档流程

```bash
# 1. 试运行（推荐先执行）
MONTH="2026-03"
TABLE="request_logs"

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"archive_month\":\"$MONTH\",\"dry_run\":true}" \
  https://__DOMAIN_8__/api/admin/data-lifecycle/partitions/archive | jq .

# 2. 确认无误后执行实际归档
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"archive_month\":\"$MONTH\",\"dry_run\":false}" \
  https://__DOMAIN_8__/api/admin/data-lifecycle/partitions/archive | jq .
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

### 7.3 批量归档

```bash
MONTHS='["2026-02","2026-03","2026-04"]'
TABLE="request_logs"

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"table_name\":\"$TABLE\",\"months\":$MONTHS,\"dry_run\":false}" \
  https://__DOMAIN_8__/api/admin/data-lifecycle/partitions/archive-batch | jq .
```

**最佳实践**：
- 一次归档 3-5 个月份
- 避免一次归档过多（> 10 个月）
- 每次归档后检查系统负载

### 7.4 空间回收

```bash
# 常规 VACUUM（不锁表）
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
VACUUM ANALYZE request_logs;
VACUUM ANALYZE request_wal;
"

# 或 VACUUM FULL（锁表，建议凌晨 2-4 点低峰期）
psql -h $DB_HOST -U $DB_USER -d llm_gateway -c "
VACUUM FULL ANALYZE request_logs;
VACUUM FULL ANALYZE request_wal;
"
```

### 7.5 归档监控脚本

```bash
#!/bin/bash
# Partition archive monitoring script

ADMIN_TOKEN="${ADMIN_TOKEN}"
API_BASE_URL="${API_BASE_URL:-https://__DOMAIN_8__}"

echo "=========================================="
echo "Partition Archive Status - $(date)"
echo "=========================================="

RESPONSE=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$API_BASE_URL/api/admin/data-lifecycle/partitions")

echo "$RESPONSE" | jq -r '.[] |
"
Table: \(.table_name)
  Total Partitions: \(.total_partitions)
  Archived: \(.archived_count)
  Archivable: \(.archivable_count)
  Total Size: \(.total_size_human)
  ---"'

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

### 7.6 数据恢复

```sql
-- 查看归档分区中的数据
SELECT COUNT(*) FROM request_logs_archive_2026_03;

-- 1. 先创建目标分区
SELECT ensure_request_logs_partition('2026-03-01'::timestamp);

-- 2. 插入数据
INSERT INTO request_logs
SELECT * FROM request_logs_archive_2026_03;

-- 3. 删除归档分区
DROP TABLE request_logs_archive_2026_03;
```

注意：归档数据使用列存储，查询性能与普通表不同。

---

## 8. 数据生命周期管理（合并自 data-lifecycle-*.md）

### 8.1 三温数据模型

| 级别 | 时间范围 | 保留策略 | 存储位置 | 用途 |
|------|----------|----------|----------|------|
| 热数据 | 最近 7 天 | 在线全量保留 | PostgreSQL `*_default` | 实时查询、故障排查 |
| 温数据 | 7-30 天 | 在线保留（可选裁剪大字段） | 月度分区 (DETACHED, heap) | 历史分析、趋势对比 |
| 冷数据 | 30-90 天 | 归档到压缩存储 | 列存 columnar 分区 (ATTACHED) | 合规审计、长期分析 |
| 过期数据 | 90 天以上 | 删除或冷备份 | 可选 S3/OSS | 法务要求保留 |

### 8.2 API 端点权限

| 端点 | 方法 | 权限 | 说明 |
|------|------|------|------|
| `/api/admin/data-lifecycle/partitions` | GET | platform_ops / super_admin | 查询分区状态 |
| `/api/admin/data-lifecycle/partitions/archive` | POST | super_admin | 归档单分区 |
| `/api/admin/data-lifecycle/partitions/archive-batch` | POST | super_admin | 批量归档 |

### 8.3 关键监控指标

1. **归档率**：`archived_count / total_partitions`（健康值 > 60%）
2. **可归档分区数**：`archivable_count`（应保持较低）
3. **压缩比**：归档前后 `size_bytes` 对比
4. **归档延迟**：最老未归档分区的年龄

### 8.4 自动化管理（PartitionManager）

```go
// 已集成到 cmd/gateway/main.go
pm := bg.NewPartitionManager(dbConn.Pool(), 24*time.Hour)
pm.Start(context.Background())
defer pm.Stop()
```

后台自动执行：
1. 自动创建下月分区
2. 每月 1-3 号自动归档 2 个月前的分区

查看日志：
```bash
grep "partition_manager" __SERVER_PATH_6__.log
grep "archive succeeded" __SERVER_PATH_6__.log | tail -5
```

### 8.5 备份策略

```bash
# 热备份（每天增量）
pg_dump -h __INTERNAL_K8S_HOST__ -U __DB_USER__ -d llm_gateway \
  --table=request_logs --data-only \
  --where="ts > NOW() - INTERVAL '7 days'" \
  | gzip -9 > /backup/incremental/request_logs_$(date +%Y%m%d).sql.gz

# 全量备份（每周日）
pg_dump -h __INTERNAL_K8S_HOST__ -U __DB_USER__ -d llm_gateway \
  --table=request_logs --data-only \
  | gzip -9 > /backup/full/request_logs_full_$(date +%Y%m%d).sql.gz
```

### 8.6 故障排查补充

| 场景 | 原因 | 解决方案 |
|------|------|----------|
| 归档失败 `archive function not available` | 未应用 Migration 305 | `psql -f db/migrations/305_partition_archive_functions.sql` |
| 归档后空间未释放 | 未执行 VACUUM | `VACUUM FULL ANALYZE request_logs;` |
| `access method "columnar" does not exist` | 未安装扩展 | `CREATE EXTENSION IF NOT EXISTS citus_columnar;` |
| `column "xxx" is of type boolean but expression is of type integer` | 列顺序不一致 | 已用显式列名列表解决，确认应用 Migration 053/305 |

---

**维护团队**: Infrastructure Team
**最后更新**: 2026-07-05
