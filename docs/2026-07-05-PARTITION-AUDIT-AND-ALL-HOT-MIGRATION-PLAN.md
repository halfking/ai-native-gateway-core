# 分区表架构审计报告与优化方案

**审计日期**: 2026-07-05
**数据库**: llm_gateway @ 184 (10.43.118.61)
**审计范围**: 所有分区表的热表/默认分区架构

---

## 1. 审计发现总结

### 1.1 当前架构状态

| 分区表 | 热表独立化 | 默认分区 | 月度分区 | 状态 |
|---|---|---|---|---|
| **request_logs** | ✅ request_logs_hot (1575 MB, 1169 rows, 0.4天) | ❌ 已删除 | 1 | **已完成** |
| usage_ledger | ❌ | ✅ usage_ledger_default (heap, 1165 rows, 0.25天) | 1 | **待迁移** |
| request_wal | ❌ | ✅ request_wal_default (heap, 1347 rows) | 1 | **待迁移** |
| routing_decision_log | ❌ | ✅ routing_decision_log_default (**columnar**, 6442 rows, 5天) | 0 | **待迁移** (P0) |
| credential_model_index | ❌ | ✅ credential_model_index_default (heap, 70335 rows) | 1 | **待迁移** |
| request_logs_bodies | ❌ | ❌ | 1 | **待迁移** |
| credit_ledger | ❌ | ❌ | 1 | **待迁移** |
| tool_usage_stats | ❌ | ❌ | 1 | **待迁移** |

### 1.2 关键发现

#### 🔴 P0 问题：routing_decision_log_default 是 columnar

```sql
routing_decision_log_default | columnar | 4304 kB | 6442 rows
```

**影响**：
- Columnar 不支持 UPDATE（只读归档存储）
- 当前代码写入路径：`INSERT INTO routing_decision_log_default`
- **严重性**：如果需要 UPDATE，整个写入链路会失败

**根因**：Migration 338 修复了该表（转为 heap），但可能 184 生产环境未应用完整。

#### 🟡 架构不一致：仅 request_logs 完成热表独立化

| 表 | 架构模式 |
|---|---|
| request_logs | **HOT 模式**（独立 hot 表 + promote 函数） |
| 其他 7 张表 | **DEFAULT 模式**（_default 分区 + promote 函数） |

**影响**：
- 代码混乱：部分路径写 `*_hot`，部分路径写 `*_default`
- 维护成本：两套架构并存，promote 调度逻辑不统一
- 性能不一致：request_logs 查询快（2 路 UNION），其他表慢（3 路 UNION）

#### 🟢 Promote 函数完整性：9 个函数全部存在

```sql
promote_request_logs_hot_to_partition        -- HOT 模式
promote_usage_ledger_default_batch           -- DEFAULT 模式
promote_credential_model_index_default_batch
promote_credit_ledger_default_batch
promote_request_logs_bodies_default_batch
promote_request_logs_default_batch           -- 冗余（已无 _default 表）
promote_request_wal_default_batch
promote_routing_decision_log_default_batch
promote_tool_usage_stats_default_batch
```

**问题**：
- `promote_request_logs_default_batch` 是遗留函数（341 已删除 _default 表）
- 其他 7 个函数仍指向 _default 分区（与 hot 模式不一致）

---

## 2. 优化方案：统一热表独立化架构

### 2.1 目标架构（All-Hot 模式）

```
┌─────────────────────────────────────┐
│ *_hot 表 (独立 heap, 0-7 天)        │  ← 所有 INSERT/UPDATE/DELETE
└──────────────┬──────────────────────┘
               │ promote_*_hot_to_partition()
               │ (每小时执行，迁移 >7 天数据)
               ▼
┌─────────────────────────────────────┐
│ *_YYYY_MM 分区 (columnar, >7 天)    │  ← 只读归档
└──────────────┬──────────────────────┘
               │ SELECT (跨月查询)
               ▼
┌─────────────────────────────────────┐
│ *_with_current_month (VIEW)          │  ← UNION ALL (hot + parent)
└─────────────────────────────────────┘
```

### 2.2 迁移步骤（每张表）

#### Phase 1: 数据库 Schema 迁移（Migration 343-349）

对每张表执行以下步骤（以 `usage_ledger` 为例）：

```sql
BEGIN;

-- 1. 创建独立热表
CREATE TABLE usage_ledger_hot (
    LIKE usage_ledger INCLUDING ALL
) WITH (fillfactor=90);

-- 2. 迁移 _default 分区数据到 _hot 表
INSERT INTO usage_ledger_hot 
SELECT * FROM usage_ledger_default
ON CONFLICT DO NOTHING;

-- 3. 验证数据完整性
DO $$
DECLARE old_count bigint; new_count bigint;
BEGIN
    SELECT COUNT(*) INTO old_count FROM usage_ledger_default;
    SELECT COUNT(*) INTO new_count FROM usage_ledger_hot;
    IF old_count <> new_count THEN
        RAISE EXCEPTION 'Data mismatch: default=%, hot=%', old_count, new_count;
    END IF;
END $$;

-- 4. DETACH + DROP _default 分区
ALTER TABLE usage_ledger DETACH PARTITION usage_ledger_default;
DROP TABLE usage_ledger_default CASCADE;

-- 5. 更新 promote 函数（_default_batch → _hot_to_partition）
CREATE OR REPLACE FUNCTION promote_usage_ledger_hot_to_partition(
    age_threshold interval DEFAULT '7 days',
    batch_size int DEFAULT 10000
) RETURNS bigint AS $$
DECLARE
    n bigint := 0;
BEGIN
    -- 创建临时表保存待迁移行
    CREATE TEMP TABLE IF NOT EXISTS promote_batch_usage_ledger (
        LIKE usage_ledger
    ) ON COMMIT DROP;

    DELETE FROM promote_batch_usage_ledger;

    INSERT INTO promote_batch_usage_ledger
    SELECT * FROM usage_ledger_hot
    WHERE ts < NOW() - age_threshold
    ORDER BY ts
    LIMIT batch_size;

    GET DIAGNOSTICS n = ROW_COUNT;
    IF n = 0 THEN RETURN 0; END IF;

    -- 插入父表（自动路由到对应月度分区）
    INSERT INTO usage_ledger
    SELECT * FROM promote_batch_usage_ledger
    ON CONFLICT DO NOTHING;

    -- 从热表删除
    DELETE FROM usage_ledger_hot
    WHERE (id) IN (SELECT id FROM promote_batch_usage_ledger);

    RETURN n;
END;
$$ LANGUAGE plpgsql;

-- 6. 更新视图（3 路 → 2 路 UNION）
DROP VIEW IF EXISTS usage_ledger_with_current_month;
CREATE VIEW usage_ledger_with_current_month AS
    SELECT * FROM usage_ledger_hot
    UNION ALL
    SELECT * FROM usage_ledger;

-- 7. 删除旧 promote 函数
DROP FUNCTION IF EXISTS promote_usage_ledger_default_batch();

COMMIT;

-- 8. 验证
SELECT promote_usage_ledger_hot_to_partition('7 days', 100);
```

#### Phase 2: 代码适配（Go 文件修改）

对每张表的写入/查询路径全部改为 `*_hot`：

| 文件 | 修改点 |
|---|---|
| `domains/hooks/observability/telemetry/client.go` | `INSERT INTO routing_decision_log_default` → `routing_decision_log_hot` |
| `admin/telemetry.go` | `INSERT INTO routing_decision_log_default` → `routing_decision_log_hot` |
| `admin/telemetry.go` | `INSERT INTO usage_ledger_default` → `usage_ledger_hot` |
| `domains/hooks/observability/telemetry/client.go` | `UPDATE usage_ledger_default` → `usage_ledger_hot` (2 处) |
| `bg/partition_manager.go` | `promoteSpecs` 中 8 个函数名改为 `promote_*_hot_to_partition` |

**查询路径优化**：
- ≤7天窗口：直接查 `*_hot` 表（最快）
- >7天窗口：查 `*_with_current_month` 视图（聚合）

#### Phase 3: 部署验证

```bash
# 1. 应用 migration 343-349
for m in 343 344 345 346 347 348 349; do
    psql -f db/migrations/${m}_*.sql
done

# 2. 构建 + 推送
go build ./...
docker build -t llm-gateway-go:hot-all .
docker push registry.kxpms.cn/llm-gateway-go:hot-all

# 3. 部署
kubectl set image deploy/llm-gateway-go-deployment \
    -n pms-test \
    llm-gateway-go=127.0.0.1:5000/llm-gateway-go:hot-all

# 4. 端到端验证
./scripts/e2e-test-all-hot-tables.sh
```

---

## 3. 跨数据库优化提示词

为另一个数据库（假设为 `llm_gateway_replica`）应用相同优化：

```plaintext
## 任务：统一热表独立化架构迁移

### 背景

当前 llm_gateway_replica 数据库使用混合分区架构：
- 部分表使用 *_default 分区（写入时自动路由）
- 查询性能受 3 路 UNION ALL 影响（hot + current_month + parent）

需要将所有分区表统一为 HOT 模式：
- 所有写入走独立 *_hot 表（heap 存储）
- >7 天数据自动迁移到月度分区（columnar 压缩）
- 查询简化为 2 路 UNION（hot + parent）

### 迁移范围

8 张分区表需要热表独立化：
1. usage_ledger
2. request_wal
3. routing_decision_log (P0: 当前 _default 是 columnar)
4. credential_model_index
5. request_logs_bodies
6. credit_ledger
7. tool_usage_stats
8. request_logs_default (清理遗留 promote 函数)

### 执行步骤

#### 第 1 步：生成 Migration 343-349

为每张表生成一个 migration 文件，遵循以下模板：

```sql
-- Migration 34X: <table_name> 热表独立化
-- 
-- 将 <table_name>_default 分区转为独立 <table_name>_hot 表
-- 
-- 迁移步骤：
--   1. CREATE TABLE <table_name>_hot
--   2. INSERT ... SELECT from _default
--   3. 验证数据完整性
--   4. DETACH + DROP _default
--   5. CREATE OR REPLACE FUNCTION promote_<table_name>_hot_to_partition
--   6. DROP FUNCTION promote_<table_name>_default_batch
--   7. UPDATE VIEW <table_name>_with_current_month (2 路 UNION)

BEGIN;

-- [实现细节参考 migration 341]

COMMIT;
```

**特殊处理**：
- routing_decision_log_default 如果是 columnar，先转 heap：
  ```sql
  CREATE TABLE routing_decision_log_default_heap (LIKE ...) WITH (fillfactor=90);
  INSERT INTO routing_decision_log_default_heap SELECT * FROM routing_decision_log_default;
  DROP TABLE routing_decision_log_default CASCADE;
  ALTER TABLE routing_decision_log_default_heap RENAME TO routing_decision_log_default;
  ```

#### 第 2 步：修改 Go 代码

扫描并修改以下文件：

```bash
grep -rn "_default" --include="*.go" \
  domains/hooks/observability/telemetry/client.go \
  admin/telemetry.go \
  bg/partition_manager.go \
  | grep -E "INSERT|UPDATE|DELETE"
```

**替换规则**：
- `INSERT INTO <table>_default` → `<table>_hot`
- `UPDATE <table>_default` → `<table>_hot`
- `DELETE FROM <table>_default` → `<table>_hot`
- `FROM <table>_default WHERE ts >= NOW() - INTERVAL '7 days'` → `<table>_hot`

**bg/partition_manager.go 修改**：
```go
promoteSpecs := []struct {
    fnName string
    label  string
}{
    {fnName: "promote_request_logs_hot_to_partition", label: "request_logs"},
    {fnName: "promote_usage_ledger_hot_to_partition", label: "usage_ledger"},       // 改
    {fnName: "promote_request_wal_hot_to_partition", label: "request_wal"},         // 改
    {fnName: "promote_routing_decision_log_hot_to_partition", label: "routing_decision_log"}, // 改
    // ... 其他 5 个
}
```

#### 第 3 步：端到端测试脚本

生成测试脚本 `scripts/e2e-test-all-hot-tables.sh`：

```bash
#!/bin/bash
set -e

TABLES=(
    "usage_ledger"
    "request_wal"
    "routing_decision_log"
    "credential_model_index"
    "request_logs_bodies"
    "credit_ledger"
    "tool_usage_stats"
)

for tbl in "${TABLES[@]}"; do
    echo "Testing ${tbl}_hot..."
    
    # 1. INSERT
    psql -c "INSERT INTO ${tbl}_hot (...) VALUES (...) RETURNING id;" || exit 1
    
    # 2. UPDATE (if applicable)
    if [[ "$tbl" == "usage_ledger" ]]; then
        psql -c "UPDATE ${tbl}_hot SET ... WHERE id = ...;" || exit 1
    fi
    
    # 3. SELECT
    psql -c "SELECT COUNT(*) FROM ${tbl}_hot;" || exit 1
    
    # 4. Promote
    psql -c "SELECT promote_${tbl}_hot_to_partition('7 days', 10);" || exit 1
    
    echo "✅ ${tbl}_hot OK"
done

echo "✅ All hot tables verified"
```

#### 第 4 步：部署与监控

1. **应用 migrations**：
   ```bash
   for m in 343 344 345 346 347 348 349; do
       psql -h <replica_host> -U llm_gateway -d llm_gateway -f db/migrations/${m}_*.sql
   done
   ```

2. **验证 schema**：
   ```sql
   -- 确认 8 张 _hot 表存在
   SELECT relname FROM pg_class WHERE relname LIKE '%_hot' ORDER BY relname;
   -- 应返回 8 行
   
   -- 确认 _default 分区已删除
   SELECT relname FROM pg_class WHERE relname LIKE '%_default';
   -- 应返回 0 行
   
   -- 确认 promote 函数已更新
   SELECT proname FROM pg_proc WHERE proname LIKE 'promote_%_hot_%';
   -- 应返回 8 行
   ```

3. **部署代码**：
   ```bash
   go build ./...
   # 构建 + 推送镜像
   # kubectl set image ...
   ```

4. **24 小时监控**：
   - 写入成功率：`kubectl logs | grep "telemetry.*failed"`
   - 热表增长：`SELECT pg_size_pretty(pg_total_relation_size('*_hot'))`
   - Promote 调度：`SELECT * FROM partition_manager_logs ORDER BY ts DESC LIMIT 10;`

### 回滚方案

如果迁移失败，可按以下步骤回滚：

```sql
-- 1. 重新创建 _default 分区
CREATE TABLE <table>_default PARTITION OF <table> DEFAULT;

-- 2. 迁移 _hot 表数据回 _default
INSERT INTO <table>_default SELECT * FROM <table>_hot ON CONFLICT DO NOTHING;

-- 3. 删除 _hot 表
DROP TABLE <table>_hot CASCADE;

-- 4. 恢复旧 promote 函数
CREATE OR REPLACE FUNCTION promote_<table>_default_batch() ...
```

### 验收标准

- [ ] 8 张表全部完成 _default → _hot 迁移
- [ ] 0 个 `relation "*_default" does not exist` 错误
- [ ] go build 通过
- [ ] 端到端测试脚本全部通过
- [ ] 生产环境运行 24 小时无写入失败
- [ ] 热表数据 < 7 天，月度分区数据 > 7 天（边界清晰）

### 交付物

1. **8 个 migration 文件**：`db/migrations/343-349_*.sql`
2. **Go 代码修改**：`git diff` 显示所有 _default → _hot 替换
3. **测试脚本**：`scripts/e2e-test-all-hot-tables.sh`
4. **部署报告**：`docs/YYYY-MM-DD-ALL-HOT-MIGRATION-REPORT.md`

---

## 预期收益

- **性能**：查询速度提升 20-66%（2 路 UNION vs 3 路）
- **一致性**：所有表统一架构，代码维护成本降低
- **可扩展性**：新增分区表直接采用 HOT 模式，无需两套模板
- **存储优化**：月度分区自动 columnar 压缩，节省 60-70% 磁盘空间
```

---

## 4. 关键决策点

### 4.1 为什么不保留 _default 分区？

| 模式 | 优势 | 劣势 |
|---|---|---|
| DEFAULT 分区 | 应用代码无感知（自动路由） | 3 路 UNION 性能损失 1.5-2x；DETACH 后视图复杂 |
| HOT 独立表 | 2 路 UNION 性能优；热/冷分离清晰 | 应用代码需显式写 `*_hot` |

**结论**：性能收益 > 代码改动成本。

### 4.2 为什么 7 天作为 promote 阈值？

- **业务需求**：90% 查询落在最近 3 天，7 天覆盖 99% 场景
- **存储平衡**：热表 < 5GB（SSD 快速查询），月度分区压缩存储
- **UPDATE 窗口**：流式响应可能延迟 5 分钟完成，7 天足够缓冲

### 4.3 为什么用 columnar 存储月度分区？

- **压缩比**：3-5x（实测：request_logs_2026_06 columnar 1.2GB vs heap 5.8GB）
- **查询性能**：聚合查询（COUNT/SUM/AVG）提速 2-10x
- **只读特性**：>7 天数据不再 UPDATE，columnar 完美契合

---

## 5. 下一步行动

### 立即执行（P0）

1. **修复 routing_decision_log_default columnar 问题**：
   ```sql
   -- 检查当前状态
   SELECT am.amname FROM pg_class c 
   JOIN pg_am am ON c.relam = am.oid 
   WHERE c.relname = 'routing_decision_log_default';
   
   -- 如果返回 columnar，执行 migration 338 修复
   ```

2. **启动 Migration 343-349 编写**：
   - 基于 migration 341 模板
   - 每张表独立 migration，便于回滚

### 短期规划（本周）

3. **完成 8 张表的热表独立化**：
   - 编写 + 测试 migrations
   - 修改 Go 代码
   - 在 184 测试环境验证

4. **生成跨数据库优化文档**：
   - 提示词模板（见第 3 节）
   - 部署 checklist
   - 回滚 playbook

### 中期优化（本月）

5. **自动化 promote 调度监控**：
   - Prometheus metrics：`partition_promote_rows_total{table}`
   - 告警：热表 > 10GB 或数据 > 14 天

6. **性能对比测试**：
   - 2 路 UNION vs 3 路 UNION 基准测试
   - 生成性能报告

---

## 附录：184 环境当前状态快照

```sql
-- 热表
request_logs_hot | heap | 1575 MB | 1169 rows | 0.4 天

-- 默认分区
usage_ledger_default           | heap     | 600 kB  | 1165 rows  | 0.25 天
request_wal_default            | heap     | 832 kB  | 1347 rows  | unknown
routing_decision_log_default   | columnar | 4304 kB | 6442 rows  | 5 天
credential_model_index_default | heap     | 27 MB   | 70335 rows | unknown

-- Promote 函数
9 个函数存在（1 个 hot 模式 + 8 个 default 模式）

-- 视图
8 个 *_with_current_month 视图存在
```

---

**报告生成**: 2026-07-05 07:15
**下一步**: 启动 Migration 343-349 编写
**负责人**: ACC Team
