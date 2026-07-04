# 分区表操作规范修复报告

**日期**: 2026-07-04  
**修复范围**: request_logs, usage_ledger, request_wal 分区表操作  
**影响文件**: `domains/hooks/observability/telemetry/client.go`

---

## 📋 问题背景

在 llm-gateway 数据库中，我们采用了如下的分区表架构策略：

### 正确的架构模式

```
INSERT/UPDATE/DELETE → 只操作主表（自动路由到正确分区）
SELECT（聚合查询）   → 查询主表（自动包含 default + 所有分区表）
数据迁移           → 后台工具将 default 表中的历史数据迁移到月度分区表
```

### 架构原则

1. **当前数据存储**: 最近的数据（如7天内）首先写入到当前默认表（`*_default`）中
2. **历史数据迁移**: 后台工具定期将 default 表中的历史数据批量迁移到按月分区的列存储表
3. **写操作规范**: 所有 INSERT/UPDATE/DELETE 操作必须针对**主表**，不能直接操作分区表
4. **查询操作**: 查询主表时 PostgreSQL 自动聚合所有分区（包括 default 和历史分区）

### 好处

✅ 写操作只在 heap 表（default）  
✅ 历史数据在 columnar 分区表（压缩存储）  
✅ 查询自动聚合所有表的数据  
✅ 没有 columnar UPDATE 错误

---

## 🔴 发现的问题

### 问题 1: `updateRequestLog()` 函数中硬编码了 `request_logs_default` 表名

**位置**: `domains/hooks/observability/telemetry/client.go:891-986`

**错误代码**:
```sql
UPDATE request_logs
   SET ...
  FROM latest
 WHERE request_logs_default.id = latest.id
   AND request_logs_default.ts = latest.ts
```

**问题分析**:
- ❌ WHERE 子句中硬编码了 `request_logs_default` 表名
- ❌ 只会更新 default 分区中的数据
- ❌ 如果数据已迁移到历史分区表（如 `request_logs_2026_06`），UPDATE 将找不到该行
- ❌ 违反了"只操作主表，让 PostgreSQL 路由到正确分区"的原则

**影响范围**:
- 流式响应完成后的 token/cost/latency 补充更新可能失败
- 已迁移到历史分区的请求记录无法被更新

---

### 问题 2: `insertRequestLog()` 函数中 ON CONFLICT 引用了 `request_logs_default`

**位置**: `domains/hooks/observability/telemetry/client.go:625-707`

**错误代码**:
```sql
INSERT INTO request_logs (...) VALUES (...)
ON CONFLICT (request_id, ts) DO UPDATE SET
  ...
  client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs_default.client_request_id),
  ...
  attachments = COALESCE(request_logs_default.attachments, EXCLUDED.attachments)
```

**问题分析**:
- ❌ ON CONFLICT DO UPDATE 子句中引用了 `request_logs_default` 表名
- ❌ 应该使用表别名或者不指定表名前缀，让 PostgreSQL 自动处理

---

## ✅ 修复方案

### 修复 1: 使用表别名而不是硬编码分区表名

**修改后代码**:
```sql
UPDATE request_logs AS rl
   SET client_model = COALESCE($2, rl.client_model),
       outbound_model = COALESCE($3, rl.outbound_model),
       ...
  FROM latest
 WHERE rl.id = latest.id
   AND rl.ts = latest.ts
```

**改进**:
- ✅ 使用表别名 `rl` 引用主表 `request_logs`
- ✅ PostgreSQL 自动路由到正确的分区（default 或历史分区）
- ✅ 符合分区表最佳实践

---

### 修复 2: ON CONFLICT 中使用主表名而非分区表名

**修改后代码**:
```sql
INSERT INTO request_logs (...) VALUES (...)
ON CONFLICT (request_id, ts) DO UPDATE SET
  ...
  client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs.client_request_id),
  ...
  attachments = COALESCE(request_logs.attachments, EXCLUDED.attachments)
```

**改进**:
- ✅ 引用主表 `request_logs` 而不是 `request_logs_default`
- ✅ PostgreSQL 自动处理分区路由
- ✅ 与分区表架构保持一致

---

## ✅ 其他表的验证结果

### `usage_ledger` 表 ✅

**代码位置**: `domains/hooks/observability/telemetry/client.go:844-889`

```sql
UPDATE usage_ledger
   SET prompt_tokens = COALESCE($2, prompt_tokens),
       completion_tokens = COALESCE($3, completion_tokens),
       ...
 WHERE request_id = $1
```

**结论**: ✅ 正确，直接操作主表 `usage_ledger`，没有指定分区表名

---

### `request_wal` 表 ✅

**代码位置**: `domains/hooks/observability/telemetry/request_logger.go:107, 243`

```sql
INSERT INTO request_wal (request_id, tenant_id, ...)
VALUES ($1, $2, ...)

UPDATE request_wal SET
  status = COALESCE(NULLIF($2, ''), status),
  ...
WHERE request_id = $1
```

**结论**: ✅ 正确，直接操作主表 `request_wal`

---

### 其他分区表 ✅

检查了以下表的操作：
- `credit_ledger` (maas/service.go:616)
- `tool_usage_stats` (domains/toolexecution/postgres_store.go:260, registry/usage_stats.go:32)

**结论**: ✅ 全部正确，都是直接操作主表

---

## 📊 影响评估

### 修复前的潜在问题

1. **数据更新丢失**: 已迁移到历史分区的请求记录无法被更新
2. **不一致的行为**: 同一个请求在不同时间点的更新行为不一致
3. **维护困难**: 如果添加新的分区表，需要修改代码

### 修复后的改进

1. **正确路由**: PostgreSQL 自动将操作路由到正确的分区
2. **一致性**: 无论数据在哪个分区，操作行为保持一致
3. **可维护性**: 添加新分区表无需修改代码
4. **符合规范**: 完全遵循分区表架构设计原则

---

## 🧪 验证结果

### 编译验证 ✅

```bash
cd domains/hooks/observability/telemetry && go build
```

**结果**: 编译通过，无语法错误

### 代码审查 ✅

- ✅ 所有 INSERT/UPDATE/DELETE 操作都针对主表
- ✅ 没有硬编码分区表名
- ✅ 使用了正确的表别名
- ✅ 符合分区表架构原则

---

## 📝 架构最佳实践总结

### ✅ 正确的做法

```sql
-- 1. INSERT 操作主表
INSERT INTO request_logs (...) VALUES (...)

-- 2. UPDATE 操作主表
UPDATE request_logs SET ... WHERE ...

-- 3. DELETE 操作主表
DELETE FROM request_logs WHERE ...

-- 4. SELECT 查询主表（自动聚合所有分区）
SELECT * FROM request_logs WHERE ts >= '2026-06-01'

-- 5. 使用表别名
UPDATE request_logs AS rl
   SET field = value
  FROM other_table AS ot
 WHERE rl.id = ot.id
```

### ❌ 错误的做法

```sql
-- 1. 不要在应用代码中直接操作分区表
INSERT INTO request_logs_default (...) VALUES (...)  -- ❌

-- 2. 不要在 WHERE/JOIN/ON CONFLICT 中硬编码分区表名
UPDATE request_logs SET ...
WHERE request_logs_default.id = ...  -- ❌

-- 3. 不要在 ON CONFLICT 中引用分区表
ON CONFLICT (...) DO UPDATE SET
  field = COALESCE(EXCLUDED.field, request_logs_default.field)  -- ❌
```

---

## 🎯 总结

本次修复确保了 `request_logs`, `usage_ledger`, `request_wal` 三个核心分区表的所有数据操作都符合架构规范：

1. ✅ **修复了 `client.go` 中的两个关键问题**
2. ✅ **验证了其他文件中的分区表操作都是正确的**
3. ✅ **确保所有写操作都针对主表，由 PostgreSQL 自动路由**
4. ✅ **代码编译通过，无语法错误**

这些修复将：
- 🎯 确保数据更新的正确性和一致性
- 🎯 避免因数据迁移导致的更新失败
- 🎯 提高代码的可维护性
- 🎯 完全符合分区表架构设计原则
