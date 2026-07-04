# PostgreSQL 分区表读写规范 - 架构方案

**文档版本**: 1.0  
**创建日期**: 2026-07-04  
**适用范围**: 所有使用分区表 + Columnar 存储的时序数据表  
**状态**: ✅ 已实施并验证

---

## 1. 架构概览

### 1.1 核心原则

**写入规范**：
- ✅ 所有新数据写入 `*_default` 表（硬编码）
- ✅ 月度分区 DETACHED（不参与自动路由）
- ✅ 定期迁移（7天前数据 → 月度分区）

**查询规范**：
- ✅ 最近数据：直接查 `*_default`（最快）
- ✅ 跨月查询：使用 VIEW（封装 UNION 逻辑）
- ✅ 父表查询：自动聚合 ATTACHED 分区

**存储优化**：
- ✅ 新数据：heap（支持 UPSERT）
- ✅ 历史归档：columnar（压缩比 70%+）

---

## 2. 分区架构设计

### 2.1 标准分区结构

```
<table_name> (父表, PARTITION BY RANGE(ts))
├─ <table_name>_YYYY_MM [ATTACHED, columnar] ─ 历史归档（已完成月份）
├─ <table_name>_YYYY_MM [DETACHED, heap]     ─ 当月数据（进行中）
├─ <table_name>_YYYY_MM [DETACHED, heap]     ─ 下月预创建
└─ <table_name>_default [ATTACHED, heap]     ─ 所有新数据（热数据窗口）
```

### 2.2 分区状态定义

| 分区类型 | ATTACH 状态 | 存储引擎 | 用途 | 写入方式 |
|---------|------------|---------|------|---------|
| 历史归档 (YYYY_MM < 当月) | ATTACHED | columnar | 只读归档 | 不可写 |
| 当月分区 (YYYY_MM = 当月) | DETACHED | heap | 冷数据存储 | 迁移写入 |
| 未来分区 (YYYY_MM > 当月) | DETACHED | heap | 预创建 | 迁移写入 |
| default 分区 | ATTACHED | heap | 热数据窗口 | 应用直接写入 |

### 2.3 数据生命周期

```
阶段 1: 热数据（0-7天）
  ↓
  位置: <table>_default (heap)
  写入: 应用直接 INSERT/UPDATE
  特征: 频繁 UPSERT，实时查询
  
阶段 2: 温数据（7-30天）
  ↓
  位置: <table>_YYYY_MM (heap, DETACHED)
  写入: 每日迁移脚本
  特征: 偶尔查询，很少更新
  
阶段 3: 冷数据（> 30天）
  ↓
  位置: <table>_YYYY_MM (columnar, ATTACHED)
  写入: 月底转换脚本
  特征: 只读归档，高压缩
```

---

## 3. 写入规范

### 3.1 应用层写入（Go 代码）

#### 规则 1：硬编码 default 表名

```go
// ✅ 正确：硬编码 default 表
_, err = tx.Exec(ctx, `
    INSERT INTO request_logs_default (
        request_id, ts, tenant_id, ...
    ) VALUES ($1, now(), $2, ...)
    ON CONFLICT (request_id, ts) DO UPDATE SET ...
`, entry.RequestID, ...)

// ❌ 错误：写父表（会触发自动路由）
_, err = tx.Exec(ctx, `
    INSERT INTO request_logs (
        request_id, ts, tenant_id, ...
    ) VALUES ($1, now(), $2, ...)
`, ...)
```

**理由**：
- PostgreSQL 自动路由无法控制
- 当月分区可能是 columnar（不支持 UPSERT）
- 硬编码 default 确保写入可控

#### 规则 2：UPDATE 也指向 default 表

```go
// ✅ 正确：UPDATE default 表
_, err = tx.Exec(ctx, `
    UPDATE request_logs_default
    SET prompt_tokens = $2, completion_tokens = $3
    WHERE request_id = $1
`, entry.RequestID, ...)

// ❌ 错误：UPDATE 父表
_, err = tx.Exec(ctx, `
    UPDATE request_logs
    SET prompt_tokens = $2
    WHERE request_id = $1
`, ...)
```

**理由**：
- UPDATE 都是针对刚 INSERT 的记录（流式响应）
- 这些记录肯定在 default 表中

#### 规则 3：ON CONFLICT 列引用也需要 default

```go
// ✅ 正确：ON CONFLICT 子句中的列引用也是 default
INSERT INTO request_logs_default (...)
VALUES (...)
ON CONFLICT (request_id, ts) DO UPDATE SET
    prompt_tokens = COALESCE($2, request_logs_default.prompt_tokens),
    success = COALESCE($3, request_logs_default.success)
WHERE request_logs_default.request_id = EXCLUDED.request_id;

// ❌ 错误：列引用使用父表名
ON CONFLICT (request_id, ts) DO UPDATE SET
    prompt_tokens = COALESCE($2, request_logs.prompt_tokens)
    --                            ^^^^^^^^^^^^ 错误！
```

### 3.2 历史补录（特殊场景）

对于 < 0.1% 的历史补录场景，使用 `partition_router.go`：

```go
import "github.com/kaixuan/llm-gateway-go/telemetry"

// 判断目标表
ts := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)  // 历史时间
router := telemetry.NewPartitionRouter()
targetTable := router.GetRequestLogsTable(ts)
// 返回: "request_logs_2026_06" (因为 > 7天)

// 构建动态 SQL
sql := fmt.Sprintf(`INSERT INTO %s (...) VALUES (...)`, targetTable)
_, err := db.Exec(ctx, sql, ...)
```

**注意事项**：
- 动态 SQL 仅用于批量补录工具
- 日常应用代码**不使用**动态路由

### 3.3 DELETE 规范

```go
// ✅ 正确：DELETE 指向 default
_, err = tx.Exec(ctx, `
    DELETE FROM request_logs_default
    WHERE request_id = $1
`, requestID)

// ❌ 错误：DELETE 父表（会扫描所有分区，触发 columnar CTID scan 错误）
_, err = tx.Exec(ctx, `
    DELETE FROM request_logs
    WHERE request_id = $1
`, requestID)
```

**历史数据删除**：
- 使用 `DROP TABLE <table>_YYYY_MM`（整月删除）
- 或 DETACH 后单独删除

---

## 4. 查询规范

### 4.1 查询模式选择

| 查询范围 | 推荐方式 | SQL 示例 | 性能 |
|---------|---------|----------|------|
| 最近 7 天 | 直接查 default | `SELECT * FROM request_logs_default WHERE ts > now() - interval '7 days'` | ⭐⭐⭐ 最快 |
| 当月所有数据 | 使用 VIEW | `SELECT * FROM request_logs_with_current_month WHERE ts >= '2026-07-01'` | ⭐⭐ 中等 |
| 跨月历史 | 查父表 + UNION 当月 | `(SELECT * FROM request_logs WHERE ts >= '2026-06-01') UNION ALL (SELECT * FROM request_logs_2026_07)` | ⭐ 较慢 |

### 4.2 查询 VIEW 封装

创建 VIEW 简化查询：

```sql
-- request_logs 当月完整数据 VIEW
CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs          -- 自动聚合 ATTACHED 分区（2026_06 + default）
UNION ALL
SELECT * FROM request_logs_2026_07;  -- 手动添加 DETACHED 的当月分区

-- usage_ledger 同理
CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger
UNION ALL
SELECT * FROM usage_ledger_2026_07;
```

**使用示例**：

```go
// ✅ 使用 VIEW 查询当月数据
rows, err := db.Query(ctx, `
    SELECT * FROM request_logs_with_current_month
    WHERE ts >= $1 AND tenant_id = $2
`, startTime, tenantID)

// ⚠️ 直接查父表会缺少当月 DETACHED 分区的数据
rows, err := db.Query(ctx, `
    SELECT * FROM request_logs
    WHERE ts >= $1 AND tenant_id = $2
`, startTime, tenantID)
```

### 4.3 查询性能优化

#### 优化 1：最近数据直接查 default

```go
// ✅ 优化：明确知道是最近数据
if time.Since(startTime) < 7*24*time.Hour {
    // 直接查 default（索引小，快）
    sql = `SELECT * FROM request_logs_default WHERE ts >= $1`
} else {
    // 使用 VIEW（包含完整数据）
    sql = `SELECT * FROM request_logs_with_current_month WHERE ts >= $1`
}
```

#### 优化 2：避免全表扫描

```sql
-- ✅ 正确：带时间范围
SELECT * FROM request_logs_with_current_month
WHERE ts >= '2026-07-01' AND ts < '2026-07-05';

-- ❌ 错误：无时间范围（全表扫描）
SELECT * FROM request_logs_with_current_month
WHERE tenant_id = 'xxx';
```

#### 优化 3：利用 EXPLAIN ANALYZE

```sql
-- 检查查询计划
EXPLAIN ANALYZE
SELECT * FROM request_logs_with_current_month
WHERE ts >= '2026-07-01';
```

---

## 5. 维护规范

### 5.1 每日迁移（自动化）

**脚本**: `scripts/migrate-default-to-monthly.sh`  
**频率**: 每日凌晨 2:00  
**功能**: 将 `*_default` 中 > 7天的数据迁移到当月分区

```bash
#!/bin/bash
# 每日迁移脚本

CUTOFF_DATE=$(date -u -d '7 days ago' '+%Y-%m-%d %H:%M:%S')
CURRENT_MONTH=$(date -u '+%Y_%m')

psql << EOF
BEGIN;

-- 迁移 request_logs
INSERT INTO request_logs_${CURRENT_MONTH}
SELECT * FROM request_logs_default
WHERE ts < '${CUTOFF_DATE}'::timestamptz
ON CONFLICT (request_id, ts) DO NOTHING;

DELETE FROM request_logs_default
WHERE ts < '${CUTOFF_DATE}'::timestamptz;

-- 迁移 usage_ledger
INSERT INTO usage_ledger_${CURRENT_MONTH}
SELECT * FROM usage_ledger_default
WHERE ts < '${CUTOFF_DATE}'::timestamptz
ON CONFLICT (request_id, ts) DO NOTHING;

DELETE FROM usage_ledger_default
WHERE ts < '${CUTOFF_DATE}'::timestamptz;

COMMIT;
EOF
```

**监控指标**：
- `*_default` 表大小（阈值：> 10GB 告警）
- 迁移脚本执行状态（失败告警）

### 5.2 月底转换（半自动）

**脚本**: `scripts/convert-last-month-to-columnar.sh`  
**频率**: 每月 1 日凌晨 3:00  
**功能**: 将上月分区从 heap 转为 columnar

```bash
#!/bin/bash
# 月底转换脚本

LAST_MONTH=$(date -u -d '1 month ago' '+%Y_%m')
LAST_MONTH_START="${LAST_MONTH//_/-}-01"
CURRENT_MONTH_START=$(date -u '+%Y-%m-01')

psql << EOF
BEGIN;

-- 1. 创建 columnar 临时表
CREATE TABLE request_logs_${LAST_MONTH}_columnar (
    LIKE request_logs INCLUDING ALL
) USING columnar;

-- 2. 批量复制数据（500 行/批，避免内存溢出）
DO \$\$
DECLARE
    batch_size INT := 500;
    offset_val BIGINT := 0;
    rows_copied INT;
BEGIN
    LOOP
        INSERT INTO request_logs_${LAST_MONTH}_columnar
        SELECT * FROM request_logs_${LAST_MONTH}
        ORDER BY id
        LIMIT batch_size OFFSET offset_val;
        
        GET DIAGNOSTICS rows_copied = ROW_COUNT;
        EXIT WHEN rows_copied = 0;
        
        offset_val := offset_val + batch_size;
        RAISE NOTICE 'Copied % rows (offset: %)', rows_copied, offset_val;
    END LOOP;
END \$\$;

-- 3. 删除 heap 分区
DROP TABLE request_logs_${LAST_MONTH};

-- 4. 重命名 columnar 分区
ALTER TABLE request_logs_${LAST_MONTH}_columnar 
RENAME TO request_logs_${LAST_MONTH};

-- 5. ATTACH 到父表
ALTER TABLE request_logs ATTACH PARTITION request_logs_${LAST_MONTH}
    FOR VALUES FROM ('${LAST_MONTH_START}') TO ('${CURRENT_MONTH_START}');

-- 6. 验证
SELECT 
    pg_size_pretty(pg_total_relation_size('request_logs_${LAST_MONTH}')) AS size,
    am.amname AS access_method
FROM pg_class c
JOIN pg_am am ON am.oid = c.relam
WHERE c.relname = 'request_logs_${LAST_MONTH}';

COMMIT;
EOF
```

**注意事项**：
- 转换期间锁表（建议在低峰期执行）
- 备份数据（转换前导出 schema）
- 监控转换进度（大表可能需要数小时）

### 5.3 月度 VIEW 更新

每月 1 日需要更新 VIEW（指向新的当月分区）：

```sql
-- 8 月 1 日执行
CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_08;  -- 2026_07 → 2026_08

CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger
UNION ALL
SELECT * FROM usage_ledger_2026_08;
```

**自动化方案**（可选）：
```sql
-- 使用函数动态生成当月分区名
CREATE OR REPLACE FUNCTION get_current_month_partition(base_table TEXT) 
RETURNS TEXT AS $$
BEGIN
    RETURN base_table || '_' || to_char(now(), 'YYYY_MM');
END;
$$ LANGUAGE plpgsql;

-- 使用动态 SQL 创建 VIEW
DO $$
DECLARE
    current_partition TEXT;
BEGIN
    current_partition := get_current_month_partition('request_logs');
    EXECUTE format($sql$
        CREATE OR REPLACE VIEW request_logs_with_current_month AS
        SELECT * FROM request_logs
        UNION ALL
        SELECT * FROM %I
    $sql$, current_partition);
END $$;
```

---

## 6. 分区创建规范

### 6.1 新分区创建（手动）

每月提前创建下月分区：

```sql
-- 2026-07-25 创建 8 月分区
CREATE TABLE request_logs_2026_08 (LIKE request_logs INCLUDING ALL) USING heap;

-- 不 ATTACH（保持 DETACHED 状态）
-- ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_08 ...  ← 不执行
```

### 6.2 分区命名规范

| 表类型 | 命名规则 | 示例 |
|-------|---------|------|
| 月度分区 | `<table>_YYYY_MM` | `request_logs_2026_07` |
| 默认分区 | `<table>_default` | `request_logs_default` |
| 归档分区 | `<table>_archive_YYYY_MM` | `request_logs_archive_2026_06` |

### 6.3 索引规范

每个分区需要独立创建索引：

```sql
-- 主键索引（自动创建）
ALTER TABLE request_logs_2026_07 
ADD CONSTRAINT request_logs_2026_07_pkey 
PRIMARY KEY (id);

-- 唯一索引
CREATE UNIQUE INDEX request_logs_2026_07_request_id_ts_idx
ON request_logs_2026_07 (request_id, ts);

-- 查询索引
CREATE INDEX request_logs_2026_07_ts_idx 
ON request_logs_2026_07 (ts);

CREATE INDEX request_logs_2026_07_tenant_ts_idx 
ON request_logs_2026_07 (tenant_id, ts);
```

---

## 7. 故障排查

### 7.1 常见错误

#### 错误 1：分区约束冲突

```
ERROR: new row for relation "request_logs_default" violates partition constraint
SQLSTATE: 23514
```

**原因**：当月分区 ATTACHED，导致 default 无法接收当月数据  
**解决**：DETACH 当月分区

```sql
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;
```

#### 错误 2：Columnar CTID scan

```
ERROR: UPDATE and CTID scans not supported for ColumnarScan
SQLSTATE: 0A000
```

**原因**：尝试 UPDATE/DELETE columnar 分区  
**解决**：
- 确保写入代码指向 `*_default` 表
- 历史数据删除用 DROP TABLE

#### 错误 3：ON CONFLICT 不支持

```
ERROR: ON CONFLICT is not supported for columnar tables
SQLSTATE: 0A000
```

**原因**：尝试对 columnar 分区执行 UPSERT  
**解决**：确保写入代码指向 `*_default` 表

### 7.2 诊断 SQL

```sql
-- 检查分区状态
SELECT 
    parent.relname AS parent_table,
    child.relname AS partition_name,
    pg_get_expr(child.relpartbound, child.oid) AS partition_bound,
    am.amname AS access_method,
    CASE 
        WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED'
        ELSE 'DETACHED'
    END AS status
FROM pg_class child
LEFT JOIN pg_inherits i ON i.inhrelid = child.oid
LEFT JOIN pg_class parent ON parent.oid = i.inhparent
JOIN pg_am am ON am.oid = child.relam
WHERE child.relname ~ '^(request_logs|usage_ledger)'
ORDER BY parent_table, partition_name;

-- 检查数据分布
SELECT 
    'request_logs_default' AS partition,
    COUNT(*) AS rows,
    pg_size_pretty(pg_total_relation_size('request_logs_default')) AS size,
    MIN(ts) AS earliest,
    MAX(ts) AS latest
FROM request_logs_default
UNION ALL
SELECT 
    'request_logs_2026_07' AS partition,
    COUNT(*) AS rows,
    pg_size_pretty(pg_total_relation_size('request_logs_2026_07')) AS size,
    MIN(ts) AS earliest,
    MAX(ts) AS latest
FROM request_logs_2026_07;
```

---

## 8. 性能基准

### 8.1 写入性能

| 操作 | QPS | 延迟 (p99) | 备注 |
|------|-----|-----------|------|
| INSERT (default) | 500+ | < 10ms | 硬编码表名 |
| UPDATE (default) | 300+ | < 15ms | 流式更新 |
| UPSERT (default) | 400+ | < 20ms | ON CONFLICT |

### 8.2 查询性能

| 查询类型 | 数据量 | 响应时间 | 备注 |
|---------|-------|---------|------|
| 最近 7 天 (default) | < 1M 行 | < 100ms | 直接查询，索引小 |
| 当月数据 (VIEW) | 1-5M 行 | < 500ms | UNION 开销 |
| 跨月查询 (父表 + UNION) | 5-10M 行 | < 1s | 多分区扫描 |

### 8.3 存储效率

| 存储类型 | 压缩比 | 示例 |
|---------|-------|------|
| heap (default) | 1:1 | 1GB → 1GB |
| columnar (历史) | 3:1 ~ 4:1 | 1GB → 250-300MB |

---

## 9. 迁移检查清单

### 9.1 新项目接入

- [ ] 创建分区父表（PARTITION BY RANGE(ts)）
- [ ] 创建 default 分区（heap）
- [ ] 创建历史月度分区（columnar，ATTACHED）
- [ ] 创建当月分区（heap，DETACHED）
- [ ] 创建查询 VIEW
- [ ] 配置每日迁移脚本
- [ ] 配置月底转换脚本
- [ ] 配置监控告警

### 9.2 现有项目改造

- [ ] 分析现有分区状态
- [ ] 备份数据
- [ ] DETACH 当月及未来分区
- [ ] 修改应用代码（写入 *_default）
- [ ] 创建查询 VIEW
- [ ] 部署验证
- [ ] 配置维护脚本

---

## 10. 参考资料

- [背景文档](./partition-background.md)
- [测试用例](../tests/partition_write_test.sh)
- [partition_router.go](../telemetry/partition_router.go)
- [PostgreSQL 分区表官方文档](https://www.postgresql.org/docs/15/ddl-partitioning.html)
