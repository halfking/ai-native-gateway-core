# 分区表热表架构迁移 - 快速参考

## 📋 概览

本次迁移统一了所有分区表的架构，将 `_default` 分区替换为独立的 `_hot` 表，提升了性能和可维护性。

**涉及的表**:
- `tool_usage_stats`
- `credit_ledger`
- `request_logs_bodies`

**迁移文件**:
- `348_tool_usage_stats_hot_independence.sql`
- `349_credit_ledger_hot_independence.sql`
- `350_request_logs_bodies_hot_independence.sql`

## 🚀 快速执行

### 测试环境
```bash
cd __LOCAL_PATH_1__
./scripts/apply_hot_table_migrations.sh test
```

### 生产环境
```bash
cd __LOCAL_PATH_1__
./scripts/apply_hot_table_migrations.sh prod
```

## 📖 详细文档

- **审计报告**: `docs/partition-table-audit-2026-07-05.md`
- **修复总结**: `docs/partition-table-fix-summary.md`
- **测试套件**: `db/tests/partition_hot_table_tests.sql`

## ✅ 验证清单

执行迁移后，验证以下内容：

### 1. 表结构验证
```sql
-- 检查hot表是否存在
\dt *_hot

-- 预期输出应包含：
-- tool_usage_stats_hot
-- credit_ledger_hot
-- request_logs_bodies_hot
```

### 2. VIEW验证
```sql
-- 检查view是否存在
\dv *_with_current_month

-- 预期输出应包含：
-- tool_usage_stats_with_current_month
-- credit_ledger_with_current_month
-- request_logs_bodies_with_current_month
```

### 3. 函数验证
```sql
-- 检查promote函数
\df promote_*_hot_to_partition

-- 预期输出应包含：
-- promote_tool_usage_stats_hot_to_partition
-- promote_credit_ledger_hot_to_partition
-- promote_request_logs_bodies_hot_to_partition
```

### 4. 数据验证
```sql
-- 检查数据是否正确迁移
SELECT 
  'tool_usage_stats' as table_name,
  (SELECT count(*) FROM tool_usage_stats_hot) as hot_count,
  (SELECT count(*) FROM tool_usage_stats_with_current_month) as view_count
UNION ALL
SELECT 'credit_ledger',
  (SELECT count(*) FROM credit_ledger_hot),
  (SELECT count(*) FROM credit_ledger_with_current_month)
UNION ALL
SELECT 'request_logs_bodies',
  (SELECT count(*) FROM request_logs_bodies_hot),
  (SELECT count(*) FROM request_logs_bodies_with_current_month);
```

### 5. 运行完整测试
```bash
psql -h $DB_HOST -U postgres -d llm_gateway \
  -f db/tests/partition_hot_table_tests.sql
```

## 🔧 故障排查

### 问题: 迁移失败
**症状**: 执行脚本时报错退出

**解决步骤**:
1. 检查数据库连接是否正常
2. 确认当前用户有足够权限
3. 查看错误日志定位具体问题
4. 如果是数据冲突，检查是否有重复数据

### 问题: 性能未改善
**症状**: 迁移后查询速度没有提升

**排查步骤**:
1. 确认代码是否正确使用hot表（见下文）
2. 检查索引是否创建成功
```sql
SELECT tablename, indexname 
FROM pg_indexes 
WHERE tablename LIKE '%_hot'
ORDER BY tablename, indexname;
```
3. 运行ANALYZE更新统计信息
```sql
ANALYZE tool_usage_stats_hot;
ANALYZE credit_ledger_hot;
ANALYZE request_logs_bodies_hot;
```

### 问题: 数据丢失
**症状**: hot表数据量与原表不一致

**检查步骤**:
1. 查看迁移日志中的数据校验部分
2. 检查是否有冲突数据被跳过
```sql
-- 检查是否有重复的唯一键
SELECT tool_id, tenant_id, usage_date, count(*) 
FROM tool_usage_stats_hot 
GROUP BY tool_id, tenant_id, usage_date 
HAVING count(*) > 1;
```
3. 如果数据确实丢失，从备份恢复

## 📝 代码更新指南

### 写操作（INSERT/UPDATE/DELETE）

**❌ 错误写法（旧）**:
```go
_, err := db.Exec(ctx, `
    INSERT INTO tool_usage_stats_default (...)
    VALUES (...)
`)
```

**✅ 正确写法（新）**:
```go
_, err := db.Exec(ctx, `
    INSERT INTO tool_usage_stats_hot (...)
    VALUES (...)
`)
```

### 查询操作

**场景1: 查询热数据（0-7天）**
```go
// ✅ 直接查hot表
_, err := db.Query(ctx, `
    SELECT * FROM request_logs_hot 
    WHERE ts >= NOW() - INTERVAL '7 days'
`)
```

**场景2: 跨月聚合查询**
```go
// ✅ 使用view
_, err := db.Query(ctx, `
    SELECT * FROM request_logs_with_current_month 
    WHERE ts >= NOW() - INTERVAL '30 days'
`)
```

**场景3: 历史数据查询**
```go
// ✅ 查询父表（自动路由到分区）
_, err := db.Query(ctx, `
    SELECT * FROM request_logs 
    WHERE ts BETWEEN '2026-01-01' AND '2026-06-01'
`)
```

## 🔄 回滚方案

如果需要回滚迁移：

### 方法1: 使用脚本（推荐）
```bash
./scripts/rollback_hot_table_migrations.sh prod
```

### 方法2: 手动回滚
```sql
-- 示例：回滚 tool_usage_stats
BEGIN;

-- 1. 重建default分区
CREATE TABLE tool_usage_stats_default 
  PARTITION OF tool_usage_stats DEFAULT;

-- 2. 迁移数据回default
INSERT INTO tool_usage_stats_default
SELECT * FROM tool_usage_stats_hot
ON CONFLICT DO NOTHING;

-- 3. 恢复旧VIEW
DROP VIEW tool_usage_stats_with_current_month;
CREATE VIEW tool_usage_stats_with_current_month AS
SELECT * FROM tool_usage_stats
UNION ALL SELECT * FROM tool_usage_stats_2026_07
UNION ALL SELECT * FROM tool_usage_stats_default;

-- 4. 删除hot表
DROP TABLE tool_usage_stats_hot CASCADE;
DROP FUNCTION promote_tool_usage_stats_hot_to_partition;

COMMIT;
```

## 📊 监控指标

### 日常检查
```sql
-- hot表数据量（应该保持在7天左右）
SELECT 
  'tool_usage_stats_hot' as table,
  count(*) as rows,
  min(usage_date) as oldest,
  max(usage_date) as newest,
  pg_size_pretty(pg_total_relation_size('tool_usage_stats_hot')) as size
FROM tool_usage_stats_hot;
```

### 性能对比
```sql
-- 执行计划分析
EXPLAIN ANALYZE
SELECT * FROM tool_usage_stats_with_current_month
WHERE usage_date >= CURRENT_DATE - 7;
```

## 🆘 联系支持

如果遇到无法解决的问题：

1. **紧急问题**: 联系 on-call DBA
2. **一般问题**: 在 Slack #llm-gateway-ops 频道咨询
3. **Bug报告**: 在 GitHub 创建 issue，附上日志

## 📚 相关资源

- [PostgreSQL Partition Documentation](https://www.postgresql.org/docs/current/ddl-partitioning.html)
- [Columnar Storage Extension](https://github.com/citusdata/cstore_fdw)
- [内部Wiki: 分区表最佳实践](https://wiki.internal/partition-best-practices)

---

**最后更新**: 2026-07-05  
**维护者**: LLM Gateway OPS Team
