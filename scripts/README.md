# 🔧 数据库修复工具使用指南

## 快速开始

### 1️⃣ 诊断问题

```bash
./scripts/diagnose-db.sh
```

这个脚本会检查：
- ✅ 数据库连接状态
- ✅ 关键表的存在性
- ✅ 视图和函数的状态
- ✅ 已应用的迁移版本
- ✅ 数据量统计

**示例输出：**
```
=== 数据库诊断工具 ===
✓ 数据库连接成功

检查关键表存在性...
  ✓ routing_decision_log
  ✗ routing_decision_log_hot (缺失)  ← 需要修复
  ✓ request_logs
  ✓ users

=== 诊断总结 ===
✗ 缺失 1 个表: routing_decision_log_hot
建议执行: ./scripts/apply-missing-migrations.sh
```

### 2️⃣ 应用修复

如果诊断发现缺失的表，运行：

```bash
./scripts/apply-missing-migrations.sh
```

这个脚本会：
1. 检查 schema_migrations 表
2. 检查 routing_decision_log_hot 是否存在
3. 如果需要，应用 migration 346
4. 创建所需的索引和视图
5. 验证修复结果

**示例输出：**
```
=== 应用缺失的数据库迁移 ===

1. 检查 schema_migrations 表...
✓ schema_migrations 表已创建

2. 检查 routing_decision_log_hot 表...
✗ routing_decision_log_hot 表不存在，需要应用 migration 346

4. 应用 migration 346 (routing_decision_log_hot)...
NOTICE: Created routing_decision_log_hot table
NOTICE: Created indexes on routing_decision_log_hot
NOTICE: Migration 346 verification PASSED
✓ Migration 346 应用成功

=== 迁移完成 ===
```

## 环境准备

### 前置条件

1. **PostgreSQL 客户端**
   ```bash
   # macOS
   brew install postgresql
   
   # Ubuntu/Debian
   sudo apt-get install postgresql-client
   
   # CentOS/RHEL
   sudo yum install postgresql
   ```

2. **数据库连接**
   
   方式一：环境变量
   ```bash
   export DATABASE_URL="postgres://user:password@host:5432/dbname"
   ```
   
   方式二：.env 文件
   ```bash
   echo "DATABASE_URL=postgres://user:password@host:5432/dbname" >> .env
   ```

### 权限要求

执行迁移需要以下数据库权限：
- `CREATE TABLE` - 创建新表
- `CREATE INDEX` - 创建索引
- `CREATE VIEW` - 创建视图
- `CREATE FUNCTION` - 创建函数
- `INSERT/SELECT/DELETE` - 数据迁移

## 故障排查

### 问题1：DATABASE_URL 未设置

**错误信息：**
```
错误: DATABASE_URL 未设置
```

**解决方案：**
```bash
# 检查 .env 文件
cat .env | grep DATABASE_URL

# 或直接导出
export DATABASE_URL="postgres://..."
```

### 问题2：psql 命令未找到

**错误信息：**
```
错误: psql 命令未找到
```

**解决方案：** 安装 PostgreSQL 客户端（见上方"前置条件"）

### 问题3：连接被拒绝

**错误信息：**
```
psql: error: connection refused
```

**可能原因：**
- 数据库未启动
- 防火墙阻止连接
- 主机名/端口错误
- 认证信息错误

**解决方案：**
```bash
# 测试连接
psql "$DATABASE_URL" -c "SELECT 1;"

# 检查数据库是否运行
pg_isready -h localhost -p 5432
```

### 问题4：权限不足

**错误信息：**
```
ERROR: permission denied for schema public
```

**解决方案：** 使用有足够权限的数据库用户（通常是数据库所有者）

### 问题5：父表不存在

**错误信息：**
```
ERROR: relation "routing_decision_log" does not exist
```

**解决方案：** 需要先应用 migration 333
```bash
psql "$DATABASE_URL" -f sql/migrations/startup/333_partition_routing_decision_log.sql
```

## 验证修复

### 1. 检查表是否创建

```bash
psql "$DATABASE_URL" -c "\d routing_decision_log_hot"
```

应该看到表结构定义。

### 2. 测试 API

```bash
# 测试带 persist_probe 的 API
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.kxpms.cn/api/routing/resolve?model=claude-opus-4-8&persist_probe=1"
```

应该返回 200 OK（不再是 500）。

### 3. 检查数据写入

```bash
psql "$DATABASE_URL" -c "
SELECT count(*), max(ts) 
FROM routing_decision_log_hot 
WHERE resolution_path = 'resolve_probe';
"
```

如果调用了 API，应该能看到新增的记录。

## 高级用法

### 仅检查特定表

```bash
psql "$DATABASE_URL" -c "
SELECT EXISTS (
    SELECT 1 FROM pg_tables 
    WHERE tablename = 'routing_decision_log_hot'
);
"
```

### 查看迁移历史

```bash
psql "$DATABASE_URL" -c "
SELECT version, description, applied_at 
FROM schema_migrations 
ORDER BY version;
"
```

### 手动 promote 数据

将超过7天的热表数据迁移到月度分区：

```bash
psql "$DATABASE_URL" -c "
SELECT promote_routing_decision_log_hot_to_partition('7 days', 5000);
"
```

### 查看数据分布

```bash
psql "$DATABASE_URL" -c "
SELECT 
    'hot table' as location,
    count(*) as rows,
    pg_size_pretty(pg_total_relation_size('routing_decision_log_hot')) as size
FROM routing_decision_log_hot
UNION ALL
SELECT 
    'partitions' as location,
    count(*) as rows,
    pg_size_pretty(pg_total_relation_size('routing_decision_log')) as size
FROM routing_decision_log;
"
```

## 回滚方案

如果需要回滚迁移：

```bash
# 1. 备份数据（可选）
pg_dump "$DATABASE_URL" -t routing_decision_log_hot > backup.sql

# 2. 删除热表
psql "$DATABASE_URL" -c "DROP TABLE IF EXISTS routing_decision_log_hot CASCADE;"

# 3. 删除视图
psql "$DATABASE_URL" -c "DROP VIEW IF EXISTS routing_decision_log_with_current_month;"

# 4. 删除函数
psql "$DATABASE_URL" -c "DROP FUNCTION IF EXISTS promote_routing_decision_log_hot_to_partition;"

# 5. 更新迁移记录
psql "$DATABASE_URL" -c "DELETE FROM schema_migrations WHERE version = '346';"
```

## 相关文档

- 📄 [Bug修复报告](../docs/bugfix-2026-07-09.md) - 完整的修复说明
- 📄 [数据库迁移指南](../docs/database-migration-fix.md) - 详细的迁移文档
- 📄 [快速摘要](../BUGFIX_SUMMARY.md) - 修复概览

## 支持

如果遇到问题：

1. 先运行诊断工具：`./scripts/diagnose-db.sh`
2. 查看日志输出找到具体错误
3. 参考"故障排查"部分
4. 查看详细文档

---

**创建时间：** 2026-07-09  
**维护者：** LLM Gateway Team
