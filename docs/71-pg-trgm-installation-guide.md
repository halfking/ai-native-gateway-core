# 71 环境 pg_trgm 扩展安装与分区统一指南

**日期：** 2026-07-04  
**目标：** 在 71 环境安装 pg_trgm 扩展并创建 2026-09 到 2026-12 分区  
**数据库：** __PRIV_IP_2__:__PORT_5__（PostgreSQL 15.3）

---

## 执行摘要

**当前状态：**
- ✅ 184 环境：pg_trgm 已安装，2026-09~12 分区已创建并测试
- ❌ 71 环境：pg_trgm 缺失，无法创建带 trgm 索引的分区
- ✅ 本地环境：pg_trgm 已安装，2026-09~12 分区已创建并测试

**需要行动：**
1. 在 __PRIV_IP_2__ 服务器上安装 postgresql-contrib-15
2. 在数据库中创建 pg_trgm 扩展
3. 重建现有分区的 trgm 索引
4. 创建未来分区（2026-09 到 2026-12）
5. 测试写入功能

---

## 步骤 1：在数据库服务器上安装 postgresql-contrib-15

### 方法 A：直接在 __PRIV_IP_2__ 上执行（推荐）

**需要：** root 或 sudo 权限

```bash
# 登录到数据库服务器
ssh __PRIV_IP_2__  # 或通过跳板机

# 检查 PostgreSQL 版本
psql --version
# 输出应为：psql (PostgreSQL) 15.x

# 安装 postgresql-contrib-15
apt-get update
apt-get install -y postgresql-contrib-15

# 验证安装
ls -la /usr/share/postgresql/15/extension/pg_trgm.control
ls -la /usr/lib/postgresql/15/lib/pg_trgm.so

# 重启 PostgreSQL（可选，通常不需要）
systemctl restart postgresql
```

### 方法 B：通过 71 服务器作为跳板

**如果无法直接 SSH 到 __PRIV_IP_2__：**

```bash
# 在 71 服务器上
ssh -p __PORT_1__ __SSH_TARGET_2__

# 从 71 跳转到 __PRIV_IP_2__（如果配置了 SSH key）
ssh root@__PRIV_IP_2__

# 执行方法 A 的安装步骤
```

### 方法 C：联系运维团队

如果以上方法都不可行，请联系运维团队并提供以下信息：

```
服务器：__PRIV_IP_2__
服务：PostgreSQL 15.3
需求：安装 postgresql-contrib-15 包
原因：启用 pg_trgm 扩展以支持全文搜索索引
紧急程度：P0（高）- 下月 1 号分区创建会失败
```

---

## 步骤 2：在数据库中创建 pg_trgm 扩展

**在 71 服务器上执行：**

```bash
ssh -p __PORT_1__ __SSH_TARGET_2__

PGPASSWORD='__DB_PWD_1__' \
psql -h __PRIV_IP_2__ -p __PORT_5__ -U llm_gateway -d llm_gateway << 'SQL'

-- 创建扩展
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- 验证
SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';
-- 预期输出：pg_trgm | 1.6

-- 测试函数
SELECT similarity('hello', 'hello') as test;
-- 预期输出：1

SQL
```

**预期结果：**
```
CREATE EXTENSION
 extname | extversion 
---------+------------
 pg_trgm | 1.6

 test 
------
    1
```

---

## 步骤 3：重建现有分区的 trgm 索引

**目的：** 为 request_logs_2026_07 和 request_logs_2026_08 添加 trgm 索引

```bash
PGPASSWORD='__DB_PWD_1__' \
psql -h __PRIV_IP_2__ -p __PORT_5__ -U llm_gateway -d llm_gateway << 'SQL'

-- 为现有分区创建 trgm 索引
DO $$
DECLARE
    part record;
    idx_count int;
BEGIN
    FOR part IN 
        SELECT tablename 
        FROM pg_tables 
        WHERE tablename LIKE 'request_logs_2026_%'
          AND schemaname = 'public'
          AND tablename NOT LIKE '%archive%'
          AND tablename NOT LIKE '%backup%'
        ORDER BY tablename
    LOOP
        -- 检查是否已有 trgm 索引
        SELECT count(*) INTO idx_count
        FROM pg_indexes
        WHERE tablename = part.tablename
          AND indexdef LIKE '%trgm%';
        
        IF idx_count = 0 THEN
            RAISE NOTICE '为 % 创建 trgm 索引...', part.tablename;
            
            -- search_text trgm 索引
            EXECUTE format(
                'CREATE INDEX CONCURRENTLY idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
                part.tablename, part.tablename
            );
            
            -- client_model trgm 索引
            EXECUTE format(
                'CREATE INDEX CONCURRENTLY idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
                part.tablename, part.tablename
            );
            
            RAISE NOTICE '✅ % 索引创建完成', part.tablename;
        ELSE
            RAISE NOTICE '⚠️ % 已有 % 个 trgm 索引', part.tablename, idx_count;
        END IF;
    END LOOP;
END $$;

-- 验证索引
SELECT 
    tablename,
    indexname
FROM pg_indexes
WHERE tablename LIKE 'request_logs_2026_%'
  AND indexdef LIKE '%trgm%'
ORDER BY tablename, indexname;

SQL
```

**说明：**
- 使用 `CREATE INDEX CONCURRENTLY` 避免锁表（可在线执行）
- request_logs_2026_07 (columnar, 1067 MB) 索引创建预计 2-3 分钟
- request_logs_2026_08 (heap, 8 KB) 索引创建预计 < 1 秒

---

## 步骤 4：创建未来分区（2026-09 到 2026-12）

```bash
PGPASSWORD='__DB_PWD_1__' \
psql -h __PRIV_IP_2__ -p __PORT_5__ -U llm_gateway -d llm_gateway << 'SQL'

-- 创建 2026-09 到 2026-12 分区
DO $$
DECLARE
    month_date DATE;
    month_start TIMESTAMPTZ;
    month_end TIMESTAMPTZ;
    part_name TEXT;
BEGIN
    FOR i IN 9..12 LOOP
        month_date := ('2026-' || LPAD(i::text, 2, '0') || '-01')::DATE;
        month_start := month_date::TIMESTAMPTZ;
        month_end := (month_date + INTERVAL '1 month')::TIMESTAMPTZ;
        part_name := 'request_logs_' || to_char(month_date, 'YYYY_MM');
        
        IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name AND relnamespace = 'public'::regnamespace) THEN
            RAISE NOTICE '创建分区: %', part_name;
            
            -- 创建 heap 分区（未来月）
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L) USING heap',
                part_name, month_start, month_end
            );
            
            -- 创建 search_text trgm 索引
            EXECUTE format(
                'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
                part_name, part_name
            );
            
            -- 创建 client_model trgm 索引
            EXECUTE format(
                'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
                part_name, part_name
            );
            
            RAISE NOTICE '✅ 分区 % 创建完成（heap + 2 trgm 索引）', part_name;
        ELSE
            RAISE NOTICE '⚠️ 分区 % 已存在', part_name;
        END IF;
    END LOOP;
END $$;

-- 验证分区结构
SELECT 
    parent.relname as parent_table,
    child.relname as partition_name,
    am.amname as storage_type,
    pg_size_pretty(pg_total_relation_size(child.oid)) as size,
    (SELECT count(*) FROM pg_indexes WHERE tablename = child.relname AND indexdef LIKE '%trgm%') as trgm_indexes
FROM pg_inherits i
JOIN pg_class parent ON parent.oid = i.inhparent
JOIN pg_class child ON child.oid = i.inhrelid
JOIN pg_am am ON child.relam = am.oid
WHERE parent.relname = 'request_logs'
ORDER BY child.relname;

SQL
```

**预期输出：**
```
NOTICE:  创建分区: request_logs_2026_09
NOTICE:  ✅ 分区 request_logs_2026_09 创建完成（heap + 2 trgm 索引）
NOTICE:  创建分区: request_logs_2026_10
NOTICE:  ✅ 分区 request_logs_2026_10 创建完成（heap + 2 trgm 索引）
NOTICE:  创建分区: request_logs_2026_11
NOTICE:  ✅ 分区 request_logs_2026_11 创建完成（heap + 2 trgm 索引）
NOTICE:  创建分区: request_logs_2026_12
NOTICE:  ✅ 分区 request_logs_2026_12 创建完成（heap + 2 trgm 索引）

 parent_table |    partition_name    | storage_type |  size   | trgm_indexes 
--------------+----------------------+--------------+---------+--------------
 request_logs | request_logs_2026_07 | columnar     | 1067 MB |            2
 request_logs | request_logs_2026_08 | heap         | 8 KB    |            2
 request_logs | request_logs_2026_09 | heap         | 320 KB  |            2
 request_logs | request_logs_2026_10 | heap         | 320 KB  |            2
 request_logs | request_logs_2026_11 | heap         | 320 KB  |            2
 request_logs | request_logs_2026_12 | heap         | 320 KB  |            2
 request_logs | request_logs_default | heap         | 1260 MB |            0
```

---

## 步骤 5：测试写入功能

```bash
PGPASSWORD='__DB_PWD_1__' \
psql -h __PRIV_IP_2__ -p __PORT_5__ -U llm_gateway -d llm_gateway << 'SQL'

-- 测试写入到各个月份
INSERT INTO request_logs (ts, tenant_id, request_id, client_model, success)
VALUES 
    ('2026-09-15 10:00:00+00', 'test-71', 'req-71-sep', 'gpt-4', true),
    ('2026-10-15 10:00:00+00', 'test-71', 'req-71-oct', 'gpt-4', true),
    ('2026-11-15 10:00:00+00', 'test-71', 'req-71-nov', 'gpt-4', true),
    ('2026-12-15 10:00:00+00', 'test-71', 'req-71-dec', 'gpt-4', true)
RETURNING id, to_char(ts, 'YYYY-MM') as month, request_id, '✅ 写入成功' as status;

-- 测试 ILIKE 查询（使用 trgm 索引）
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, ts, client_model
FROM request_logs
WHERE client_model ILIKE '%gpt%'
  AND ts >= '2026-09-01'
  AND ts < '2026-10-01'
LIMIT 10;
-- 应该看到 "Bitmap Index Scan using idx_request_logs_2026_09_client_model_trgm"

SQL
```

**预期结果：**
```
  id   |  month  | request_id  |   status    
-------+---------+-------------+-------------
 26865 | 2026-09 | req-71-sep  | ✅ 写入成功
 26866 | 2026-10 | req-71-oct  | ✅ 写入成功
 26867 | 2026-11 | req-71-nov  | ✅ 写入成功
 26868 | 2026-12 | req-71-dec  | ✅ 写入成功
```

---

## 步骤 6：验证三环境一致性

### 最终状态对比

| 环境 | pg_trgm | 分区数量 | trgm 索引 | 2026-09~12 | 状态 |
|---|---|---|---|---|---|
| **184** | ✅ 1.6 | 7 个 | ✅ 正常 | ✅ heap | 🟢 完成 |
| **71** | ✅ 1.6 | 7 个 | ✅ 正常 | ✅ heap | 🟢 完成 |
| **本地** | ✅ 1.6 | 10 个 | ✅ 正常 | ✅ heap | 🟢 完成 |

### 验证查询

```sql
-- 在三个环境中执行
SELECT 
    'pg_trgm 扩展' as check_item,
    (SELECT extversion FROM pg_extension WHERE extname = 'pg_trgm') as value,
    CASE WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN '✅' ELSE '❌' END as status
UNION ALL
SELECT 
    '2026 分区数量',
    count(*)::text,
    CASE WHEN count(*) >= 6 THEN '✅' ELSE '⚠️' END
FROM pg_tables
WHERE tablename LIKE 'request_logs_2026_%'
  AND schemaname = 'public'
UNION ALL
SELECT 
    'trgm 索引总数',
    count(*)::text,
    CASE WHEN count(*) >= 10 THEN '✅' ELSE '⚠️' END
FROM pg_indexes
WHERE tablename LIKE 'request_logs_2026_%'
  AND indexdef LIKE '%trgm%';
```

---

## 故障排查

### 问题 1：CREATE EXTENSION 失败

**错误：**
```
ERROR: extension "pg_trgm" is not available
DETAIL: Could not open extension control file
```

**原因：** postgresql-contrib-15 未安装

**解决：** 返回步骤 1，在服务器上安装 postgresql-contrib-15

---

### 问题 2：索引创建很慢

**现象：** CREATE INDEX 执行超过 10 分钟

**原因：** columnar 表索引创建较慢（request_logs_2026_07 有 13,446 行）

**解决：**
1. 使用 `CREATE INDEX CONCURRENTLY`（已在脚本中）
2. 在业务低峰期执行
3. 监控进度：
   ```sql
   SELECT 
       pid,
       now() - query_start as duration,
       state,
       left(query, 100) as query
   FROM pg_stat_activity
   WHERE query LIKE '%CREATE INDEX%';
   ```

---

### 问题 3：应用仍报 trgm 错误

**错误：**
```
ERROR: could not access file "$libdir/pg_trgm"
```

**原因：** PostgreSQL 需要重启以加载新的库文件

**解决：**
```bash
systemctl restart postgresql
```

**注意：** 重启会短暂中断连接（~5 秒），建议在维护窗口执行

---

## 时间估算

| 步骤 | 操作 | 预计耗时 |
|---|---|---|
| 1 | 安装 postgresql-contrib-15 | 5-10 分钟 |
| 2 | 创建 pg_trgm 扩展 | < 1 秒 |
| 3 | 重建现有索引（2 个分区） | 3-5 分钟 |
| 4 | 创建未来分区（4 个月） | 1-2 分钟 |
| 5 | 测试写入 | < 1 分钟 |
| 6 | 验证一致性 | 2 分钟 |
| **总计** | | **15-20 分钟** |

---

## 回滚方案

如果安装后出现问题，可以执行以下回滚：

```sql
-- 1. 删除新创建的分区
DROP TABLE IF EXISTS request_logs_2026_09 CASCADE;
DROP TABLE IF EXISTS request_logs_2026_10 CASCADE;
DROP TABLE IF EXISTS request_logs_2026_11 CASCADE;
DROP TABLE IF EXISTS request_logs_2026_12 CASCADE;

-- 2. 删除 trgm 索引（可选）
DROP INDEX IF EXISTS idx_request_logs_2026_07_search_trgm;
DROP INDEX IF EXISTS idx_request_logs_2026_07_client_model_trgm;
DROP INDEX IF EXISTS idx_request_logs_2026_08_search_trgm;
DROP INDEX IF EXISTS idx_request_logs_2026_08_client_model_trgm;

-- 3. 删除扩展（不推荐）
-- DROP EXTENSION pg_trgm CASCADE;
```

**注意：** 通常不需要回滚，除非遇到严重问题。

---

## 后续维护

### 自动分区创建

应用代码中的 `ensure_next_month_partition()` 函数会在每月自动创建下月分区，包括 trgm 索引。

**验证自动创建：**
```sql
-- 8 月 1 号自动创建 2027-01 分区
SELECT * FROM request_logs_2027_01 LIMIT 1;
```

### 监控告警

建议添加以下监控：

1. **pg_trgm 扩展存在性**
   ```sql
   SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm';
   -- 预期：1
   ```

2. **trgm 索引覆盖率**
   ```sql
   SELECT 
       count(DISTINCT tablename) as partitions,
       count(*) as trgm_indexes
   FROM pg_indexes
   WHERE tablename LIKE 'request_logs_2026_%'
     AND indexdef LIKE '%trgm%';
   -- 预期：每个分区至少 2 个 trgm 索引
   ```

---

## 签名

**文档创建：** 2026-07-04 01:30  
**测试状态：** ✅ 184 和本地环境已验证  
**71 环境：** ⏳ 待执行（本指南）  
**优先级：** P0（高）  
**预计完成：** 本周内

---

## 附录：完整脚本

### 一键安装脚本（71 环境）

```bash
#!/bin/bash
# 71 环境 pg_trgm 完整安装脚本
# 在 71 服务器上执行

set -euo pipefail

export PGHOST=__PRIV_IP_2__
export PGPORT=__PORT_5__
export PGUSER=llm_gateway
export PGDATABASE=llm_gateway
export PGPASSWORD='__DB_PWD_1__'

echo "=== 71 环境 pg_trgm 安装与分区创建 ==="
echo ""

# 步骤 2：创建扩展
echo "步骤 2：创建 pg_trgm 扩展..."
psql -c "CREATE EXTENSION IF NOT EXISTS pg_trgm;"
psql -c "SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';"

# 步骤 3：重建索引
echo ""
echo "步骤 3：重建现有分区索引..."
psql << 'SQL'
DO $$
DECLARE part record;
BEGIN
    FOR part IN 
        SELECT tablename FROM pg_tables 
        WHERE tablename LIKE 'request_logs_2026_%'
          AND schemaname = 'public'
    LOOP
        EXECUTE format('CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)', part.tablename, part.tablename);
        EXECUTE format('CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)', part.tablename, part.tablename);
        RAISE NOTICE '✅ % 索引完成', part.tablename;
    END LOOP;
END $$;
SQL

# 步骤 4：创建未来分区
echo ""
echo "步骤 4：创建 2026-09 到 2026-12 分区..."
psql << 'SQL'
DO $$
DECLARE
    month_date DATE;
    month_start TIMESTAMPTZ;
    month_end TIMESTAMPTZ;
    part_name TEXT;
BEGIN
    FOR i IN 9..12 LOOP
        month_date := ('2026-' || LPAD(i::text, 2, '0') || '-01')::DATE;
        month_start := month_date::TIMESTAMPTZ;
        month_end := (month_date + INTERVAL '1 month')::TIMESTAMPTZ;
        part_name := 'request_logs_' || to_char(month_date, 'YYYY_MM');
        
        IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
            EXECUTE format('CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L) USING heap', part_name, month_start, month_end);
            EXECUTE format('CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)', part_name, part_name);
            EXECUTE format('CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)', part_name, part_name);
            RAISE NOTICE '✅ % 创建完成', part_name;
        END IF;
    END LOOP;
END $$;
SQL

# 步骤 5：测试写入
echo ""
echo "步骤 5：测试写入..."
psql << 'SQL'
INSERT INTO request_logs (ts, tenant_id, request_id, client_model, success)
VALUES 
    ('2026-09-15 10:00:00+00', 'test-71', 'req-71-sep', 'gpt-4', true),
    ('2026-10-15 10:00:00+00', 'test-71', 'req-71-oct', 'gpt-4', true),
    ('2026-11-15 10:00:00+00', 'test-71', 'req-71-nov', 'gpt-4', true),
    ('2026-12-15 10:00:00+00', 'test-71', 'req-71-dec', 'gpt-4', true)
RETURNING id, to_char(ts, 'YYYY-MM') as month, request_id;
SQL

echo ""
echo "=== 安装完成！ ==="
```

保存为 `/tmp/install-71-pg-trgm.sh` 并执行：
```bash
bash /tmp/install-71-pg-trgm.sh
```
