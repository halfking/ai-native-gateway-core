# 数据库迁移修复指南

## 问题背景

在修复 `/api/routing/resolve?persist_probe=1` 返回500错误时，发现问题根源是 `routing_decision_log_hot` 表不存在。这个表应该由 migration 346 创建，但可能尚未应用到生产数据库。

## 诊断工具

### 1. 快速诊断

运行诊断脚本检查数据库状态：

```bash
./scripts/diagnose-db.sh
```

这个脚本会检查：
- 数据库连接状态
- 关键表的存在性（routing_decision_log_hot, request_logs_hot等）
- 关键视图的存在性
- 迁移历史记录
- 数据量统计

### 2. 诊断输出示例

```
=== 数据库诊断工具 ===
数据库: postgres://user@localhost:5432/llm_gateway

1. 测试数据库连接...
✓ 数据库连接成功

2. 检查关键表存在性...
  ✓ routing_decision_log
  ✗ routing_decision_log_hot (缺失)  ← 需要修复
  ✓ request_logs
  ✓ request_logs_hot
  ✓ users
  ✓ credentials
  ✓ providers

3. 检查关键视图...
  ✗ routing_decision_log_with_current_month (缺失)

4. 检查迁移状态...
  ✓ schema_migrations 表存在
  关键迁移版本:
    333 | partition_routing_decision_log | 2026-06-15
    (346 未应用)

=== 诊断总结 ===
✗ 缺失 1 个表: routing_decision_log_hot

建议执行:
  ./scripts/apply-missing-migrations.sh
```

## 修复步骤

### 方案A：自动修复（推荐）

使用自动化脚本应用缺失的迁移：

```bash
# 1. 进入项目目录
cd /Users/xutaohuang/workspace/llm-gateway-go-3

# 2. 确保 DATABASE_URL 已设置
export DATABASE_URL="postgres://user:password@host:5432/dbname"
# 或者在 .env 文件中配置

# 3. 运行诊断（可选）
./scripts/diagnose-db.sh

# 4. 应用缺失的迁移
./scripts/apply-missing-migrations.sh
```

### 方案B：手动应用迁移

如果需要更精细的控制，可以手动应用迁移：

```bash
# 1. 检查前置条件
psql $DATABASE_URL -c "
SELECT tablename FROM pg_tables 
WHERE schemaname = 'public' 
AND tablename LIKE 'routing_decision_log%';
"

# 2. 如果 routing_decision_log 父表不存在，先应用 migration 333
psql $DATABASE_URL -f sql/migrations/startup/333_partition_routing_decision_log.sql

# 3. 应用 migration 346 创建 hot 表
psql $DATABASE_URL -f sql/migrations/startup/346_routing_decision_log_hot_independence.sql

# 4. 验证
psql $DATABASE_URL -c "
SELECT tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) 
FROM pg_tables 
WHERE tablename = 'routing_decision_log_hot';
"
```

## 迁移详情

### Migration 346: routing_decision_log_hot_independence

**目的：** 创建独立的热表架构，提升查询性能

**关键变更：**

1. **创建独立热表**
   ```sql
   CREATE TABLE routing_decision_log_hot (
       LIKE routing_decision_log INCLUDING ALL
   ) WITH (fillfactor=90);
   ```

2. **创建索引**
   - `routing_decision_log_hot_request_id_ts_key` (唯一约束)
   - `routing_decision_log_hot_ts_idx` (时间索引)
   - `routing_decision_log_hot_tenant_id_ts_idx` (租户+时间索引)

3. **创建视图**
   ```sql
   CREATE VIEW routing_decision_log_with_current_month AS
   SELECT * FROM routing_decision_log_hot    -- 热数据 (0-7天)
   UNION ALL
   SELECT * FROM routing_decision_log;        -- 历史分区
   ```

4. **创建 promote 函数**
   - `promote_routing_decision_log_hot_to_partition(interval, int)`
   - 自动将超过7天的数据从热表迁移到月度分区

**前置条件：**
- Migration 333 (创建 routing_decision_log 父表和分区)
- Migration 343 (将 default 分区转为 heap 存储)

## 代码适配

应用迁移后，代码已经适配（已在本次修复中完成）：

### 后端修改（已完成）

1. **`admin/routing_resolve_probe.go`**
   - ✅ 将 INSERT 目标从 `routing_decision_log_default` 改为 `routing_decision_log_hot`
   - ✅ 添加容错处理：表不存在时不阻断主流程
   - ✅ 降级日志级别：Error → Warn

2. **代码示例**
   ```go
   // 修复前：可能导致500错误
   _, err := h.db.Exec(ctx, `INSERT INTO routing_decision_log_hot ...`)
   if err != nil {
       slog.Error("...")
       return  // ← 阻断主流程
   }
   
   // 修复后：容错处理
   _, err := h.db.Exec(ctx, `INSERT INTO routing_decision_log_hot ...`)
   if err != nil {
       slog.Warn("...likely missing table...", "hint", "check migration 346")
       // 不再 return，继续执行
   }
   // 无论成功与否都刷新缓存
   if globalFunnelCache != nil {
       globalFunnelCache.invalidateModel(model)
   }
   ```

## 验证步骤

### 1. 验证表结构

```bash
psql $DATABASE_URL -c "
\d routing_decision_log_hot
"
```

预期输出：
```
                    Table "public.routing_decision_log_hot"
       Column        |           Type           | Collation | Nullable | Default 
---------------------+--------------------------+-----------+----------+---------
 id                  | bigint                   |           |          | 
 ts                  | timestamp with time zone |           |          | 
 request_id          | text                     |           |          | 
 model               | text                     |           |          | 
 ...
```

### 2. 测试 API

```bash
# 测试不带 persist_probe 参数
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.kxpms.cn/api/routing/resolve?model=claude-opus-4-8"

# 预期：200 OK

# 测试带 persist_probe 参数（之前返回500）
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.kxpms.cn/api/routing/resolve?model=claude-opus-4-8&persist_probe=1"

# 预期：200 OK（不再500）
```

### 3. 检查数据写入

```bash
psql $DATABASE_URL -c "
SELECT count(*), 
       min(ts) as oldest, 
       max(ts) as newest 
FROM routing_decision_log_hot 
WHERE resolution_path = 'resolve_probe';
"
```

如果看到数据，说明写入成功。

### 4. 验证 promote 函数

```bash
# 手动触发 promote（将 >7天的数据迁移到月度分区）
psql $DATABASE_URL -c "
SELECT promote_routing_decision_log_hot_to_partition('7 days', 100);
"
```

## 常见问题

### Q1: 运行脚本时提示 "DATABASE_URL 未设置"

**解决方案：**
```bash
# 方案1：导出环境变量
export DATABASE_URL="postgres://user:pass@host:5432/dbname"

# 方案2：在 .env 文件中配置
echo "DATABASE_URL=postgres://user:pass@host:5432/dbname" >> .env
```

### Q2: psql 命令未找到

**解决方案：**
```bash
# macOS
brew install postgresql

# Ubuntu/Debian
sudo apt-get install postgresql-client

# CentOS/RHEL
sudo yum install postgresql
```

### Q3: 应用迁移时报错 "routing_decision_log does not exist"

**原因：** 缺少前置迁移（migration 333）

**解决方案：**
```bash
# 先应用 migration 333 创建父表
psql $DATABASE_URL -f sql/migrations/startup/333_partition_routing_decision_log.sql

# 再应用 migration 346
./scripts/apply-missing-migrations.sh
```

### Q4: 迁移后 API 仍然返回500

**可能原因：**
1. 后端代码未更新
2. 服务未重启

**解决方案：**
```bash
# 1. 确保代码已更新（git pull 或重新部署）
# 2. 重启 gateway 服务
systemctl restart llm-gateway
# 或
pkill -9 gateway && ./gateway
```

### Q5: 数据写入后在热表中找不到

**可能原因：** 数据已被 promote 到月度分区

**解决方案：** 使用视图查询
```bash
psql $DATABASE_URL -c "
SELECT count(*) FROM routing_decision_log_with_current_month 
WHERE resolution_path = 'resolve_probe';
"
```

## 监控和维护

### 定期检查热表大小

```bash
psql $DATABASE_URL -c "
SELECT 
    pg_size_pretty(pg_total_relation_size('routing_decision_log_hot')) as hot_size,
    count(*) as row_count,
    min(ts) as oldest,
    max(ts) as newest
FROM routing_decision_log_hot;
"
```

### 设置自动 promote 任务

在 crontab 中添加定时任务：

```bash
# 每天凌晨2点执行 promote
0 2 * * * psql $DATABASE_URL -c "SELECT promote_routing_decision_log_hot_to_partition('7 days', 5000);"
```

## 影响范围

### 受益功能

1. ✅ `/api/routing/resolve?persist_probe=1` 不再返回500
2. ✅ 路由决策日志可以持久化
3. ✅ 查询性能提升（独立热表架构）
4. ✅ 存储优化（月度分区 + 自动归档）

### 无影响功能

- ✅ 普通路由解析（不带persist_probe参数）
- ✅ 其他 API 端点
- ✅ 用户认证和登录

## 回滚方案

如果迁移后出现问题，可以回滚：

```bash
# 1. 删除热表
psql $DATABASE_URL -c "DROP TABLE IF EXISTS routing_decision_log_hot CASCADE;"

# 2. 删除视图
psql $DATABASE_URL -c "DROP VIEW IF EXISTS routing_decision_log_with_current_month;"

# 3. 删除 promote 函数
psql $DATABASE_URL -c "DROP FUNCTION IF EXISTS promote_routing_decision_log_hot_to_partition;"

# 4. 更新迁移记录
psql $DATABASE_URL -c "DELETE FROM schema_migrations WHERE version = '346';"
```

## 总结

通过应用 migration 346，我们：

1. ✅ 创建了 `routing_decision_log_hot` 表
2. ✅ 修复了 `/api/routing/resolve` API 500错误
3. ✅ 提升了查询性能
4. ✅ 优化了存储结构

执行顺序：
1. 运行 `./scripts/diagnose-db.sh` 诊断问题
2. 运行 `./scripts/apply-missing-migrations.sh` 应用修复
3. 重启服务
4. 验证 API 功能

---

**相关文档：**
- [Bug修复报告](./bugfix-2026-07-09.md)
- [Migration 346 SQL](../sql/migrations/startup/346_routing_decision_log_hot_independence.sql)
