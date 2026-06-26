# 🚨 CRITICAL BUG ANALYSIS: request_logs 写入失败

**Date:** 2026-06-27  
**Severity:** P0 - 生产环境数据丢失  
**Status:** ROOT CAUSE IDENTIFIED

---

## 🔴 问题描述

**现象:** 新的请求没有记录到 request_logs 表中

**影响范围:** 所有新请求的审计数据丢失

---

## 🔍 根本原因

### 问题链条

1. **Commit d16131ad 引入的修复**（我们的原始方案）:
   - 修改 `telemetry/client.go`: `ON CONFLICT (request_id) DO NOTHING`
   - 添加 `db/db.go::ensureRequestLogsUniqueIndex()`: 创建 `UNIQUE INDEX ON request_logs (request_id)`

2. **PostgreSQL 分区表约束**:
   - `request_logs` 是 **PARTITIONED TABLE** (按 `ts` 字段 RANGE 分区)
   - PostgreSQL 要求：**分区表的所有唯一索引必须包含分区键**
   - 我们的索引: `UNIQUE (request_id)` ❌ 缺少分区键 `ts`
   - PostgreSQL 错误: `SQLSTATE 0A000` (feature not supported)

3. **Startup 失败**:
   ```
   CREATE UNIQUE INDEX ON request_logs (request_id) fails with SQLSTATE 0A000
   → db.Open() 返回错误
   → postgres pool disabled at startup
   → admin/api routes 返回 404
   → routing executor 503
   ```

4. **ON CONFLICT 失败**:
   ```sql
   ON CONFLICT (request_id) DO NOTHING
   ```
   这个子句需要一个匹配的 UNIQUE 约束。因为索引创建失败，ON CONFLICT 找不到约束，导致：
   - **INSERT 失败** (没有匹配的约束)
   - 或者 INSERT 无法正确处理冲突
   - **结果: 没有数据写入 request_logs**

5. **Revert 措施** (commit 86eaab47 之后):
   - `ensureRequestLogsUniqueIndex()` 被注释掉
   - 注释说明: "partitioned table requires partitioning key in unique index"
   - 但 `ON CONFLICT (request_id) DO NOTHING` **仍然存在** 在 telemetry/client.go 中

### 当前状态

```
❌ db/db.go: ensureRequestLogsUniqueIndex() 被禁用
❌ 数据库: 没有 UNIQUE INDEX ON request_logs (request_id)
❌ telemetry/client.go: ON CONFLICT (request_id) DO NOTHING 引用了不存在的约束
❌ 结果: INSERT 语句失败或被拒绝，数据丢失
```

---

## 📊 PostgreSQL 分区表约束

### 为什么不能创建 UNIQUE (request_id)？

PostgreSQL 文档明确规定：
> A primary key or unique constraint on a partitioned table must include all the partition key columns.

```sql
-- ❌ 失败 (缺少分区键 ts)
CREATE UNIQUE INDEX idx_request_logs_request_id_unique 
    ON request_logs (request_id);

-- ✅ 可行 (包含分区键 ts)
CREATE UNIQUE INDEX idx_request_logs_request_id_ts_unique 
    ON request_logs (request_id, ts);
```

### 为什么会失败？

分区表的物理结构是：
```
request_logs (parent, logical)
  ├── request_logs_202601 (partition 1)
  ├── request_logs_202602 (partition 2)
  └── request_logs_202603 (partition 3)
```

UNIQUE (request_id) 需要跨分区检查唯一性，但 PostgreSQL 的分区表实现不支持这种跨分区的全局唯一约束（除非包含分区键）。

---

## 🔧 为什么 ON CONFLICT (request_id) 会导致写入失败？

```sql
INSERT INTO request_logs (...) VALUES (...)
ON CONFLICT (request_id) DO NOTHING
```

PostgreSQL 的 ON CONFLICT 子句要求：
1. **必须有匹配的 UNIQUE 或 PRIMARY KEY 约束**
2. 如果没有匹配约束 → `ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification`

### 当前状态检查

由于 `ensureRequestLogsUniqueIndex()` 被禁用：
- ❌ 数据库中**没有** `idx_request_logs_request_id_unique` 索引
- ✅ 可能还有旧的 `idx_request_logs_request_id_ts_unique (request_id, ts)` 索引

### INSERT 行为

```sql
-- 当前代码
ON CONFLICT (request_id) DO NOTHING

-- 数据库状态
-- idx_request_logs_request_id_unique 不存在
-- idx_request_logs_request_id_ts_unique (request_id, ts) 可能存在

-- 结果
ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification
→ INSERT 失败
→ 没有数据写入
```

---

## 💡 修复方案

### 方案 1: 恢复旧约束 (快速修复)

**原理:** 回退到 (request_id, ts) 约束，这个在分区表上可行

```go
// telemetry/client.go
ON CONFLICT (request_id, ts) DO UPDATE SET
    outbound_model = EXCLUDED.outbound_model,
    success = EXCLUDED.success,
    // ... 其他字段
```

**优点:**
- ✅ 立即恢复写入功能
- ✅ 与分区表兼容
- ✅ 回退到已知可工作的状态

**缺点:**
- ❌ 重新引入原始 bug (ts=now() 允许多行)
- ❌ 需要应用层保证不重复 INSERT

### 方案 2: 移除 ON CONFLICT (推荐立即执行)

**原理:** 既然没有唯一约束可用，就不要使用 ON CONFLICT

```go
// telemetry/client.go
INSERT INTO request_logs (...) VALUES (...)
-- 移除 ON CONFLICT 子句
```

**优点:**
- ✅ 立即恢复写入功能
- ✅ 简单直接
- ✅ 不依赖约束

**缺点:**
- ⚠️ 如果应用层逻辑有重复 INSERT，会创建多行（但至少数据不丢失）

### 方案 3: 包含 ts 的 UNIQUE 约束 + 应用层去重

**原理:** 使用 UNIQUE (request_id, ts)，但在应用层确保同一个 request_id 只 INSERT 一次

```sql
-- 数据库层
CREATE UNIQUE INDEX idx_request_logs_request_id_ts_unique 
    ON request_logs (request_id, ts);

-- 应用层
// 使用 sync.Map 或其他机制记录已 INSERT 的 request_id
// 确保每个 request_id 只调用一次 insertRequestLog()
```

**优点:**
- ✅ 与分区表兼容
- ✅ 有约束保护

**缺点:**
- ⚠️ 需要应用层状态管理
- ⚠️ 复杂度高

### 方案 4: 改变 ts 生成策略

**原理:** 让 ts 在同一 request_id 的重试中保持一致

```go
// 在 request context 中生成并固定 ts
ts := time.Now()
// 所有重试都使用这个 ts
INSERT INTO request_logs (request_id, ts, ...) VALUES ($1, $2, ...)
```

**优点:**
- ✅ ON CONFLICT (request_id, ts) 可以正确去重
- ✅ 与分区表兼容

**缺点:**
- ⚠️ ts 不再反映实际 INSERT 时间
- ⚠️ 需要在整个调用链中传递 ts

---

## 🚀 推荐立即行动

### P0 紧急修复 (恢复写入)

**选择方案 2: 移除 ON CONFLICT**

```go
// telemetry/client.go insertRequestLog()
// 1. 移除 ON CONFLICT (request_id) DO NOTHING
// 2. 让 INSERT 直接执行

// 修改前
ON CONFLICT (request_id) DO NOTHING

// 修改后
// (删除这一行)
```

**理由:**
- 最快恢复数据写入
- 风险最低
- 可以后续再优化去重逻辑

### P1 后续优化

1. **恢复旧的 UNIQUE 约束**
   ```sql
   CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_ts_unique 
       ON request_logs (request_id, ts);
   ```

2. **修改 ON CONFLICT 使用 (request_id, ts)**
   ```go
   ON CONFLICT (request_id, ts) DO UPDATE SET ...
   ```

3. **应用层确保不重复调用 insertRequestLog()**
   - 使用标志位或 sync.Map 记录已 INSERT 的 request_id
   - 或者改用 upsert 模式（先 UPDATE，失败再 INSERT）

---

## 📝 教训总结

1. **分区表的唯一约束有严格限制** - 必须包含分区键
2. **ON CONFLICT 需要匹配的约束** - 否则 INSERT 失败
3. **测试需要覆盖生产环境的表结构** - 开发环境可能不是分区表
4. **Schema 变更需要验证表类型** - 分区表 vs 普通表行为不同

---

## ⚠️ 当前紧急状态

**数据丢失风险:** 所有新请求的审计日志都没有记录

**需要立即行动:** 修复 telemetry/client.go 的 INSERT 语句
