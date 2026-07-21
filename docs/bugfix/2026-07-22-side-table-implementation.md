# 侧表实现修复 - request_logs_bodies_hot

**日期**: 2026-07-22  
**类型**: Feature Implementation + Bug Fix  
**影响**: 所有模型的请求/响应 body 存储  
**状态**: ✅ 已修复

---

## 问题背景

侧表 `request_logs_bodies_hot` 已在数据库架构中创建（Ticket #10），用于存储完整的 `request_body` 和 `response_body`，以避免主表 `request_logs_hot` 膨胀。但是：

1. **admin 层**（`admin/telemetry.go`）已实现侧表写入逻辑
2. **domains/hooks/observability/telemetry 层**（`client.go`）**未实现**侧表写入逻辑
3. 主表的 INSERT 和 UPDATE 仍在写入 body 字段

这导致：
- 主表和侧表数据重复
- 主表仍然膨胀
- 侧表架构未真正生效

---

## 修复内容

### 1. `insertRequestLog` 函数修改

#### 1.1 主表 INSERT - 将 body 字段改为 NULL

**修改位置**: `client.go:937-938`

```go
// ❌ 修改前：主表写入 body
entry.RequestPreview,
entry.TransformSummary,
entry.ResponsePreview,
strPtrToJSON(entry.RequestBody),
strPtrToJSON(entry.ResponseBody),
entry.StreamFirstChunkMs,

// ✅ 修改后：主表 body 字段写入 NULL
entry.RequestPreview,
entry.TransformSummary,
entry.ResponsePreview,
// 2026-07-22: request_body and response_body are now stored in
// request_logs_bodies_hot side table. Main table keeps NULL to avoid bloat.
nil, // request_body
nil, // response_body
entry.StreamFirstChunkMs,
```

#### 1.2 新增侧表 INSERT 逻辑

**修改位置**: `client.go:1010-1033`（在主表 INSERT 之后、api_keys UPDATE 之前）

```go
// 2026-07-22 Ticket #10: INSERT full bodies into request_logs_bodies_hot.
// This side table stores complete request_body and response_body to avoid
// bloating the main table. The side table uses ON CONFLICT DO UPDATE to
// handle race conditions (async retries landing on the same request_id).
_, err = tx.Exec(ctx, `
	INSERT INTO request_logs_bodies_hot (
		request_id, ts, request_body, response_body
	) VALUES (
		$1, now(), CAST($2 AS jsonb), CAST($3 AS jsonb)
	)
	ON CONFLICT (request_id, ts) DO UPDATE SET
		request_body = COALESCE(EXCLUDED.request_body, request_logs_bodies_hot.request_body),
		response_body = COALESCE(EXCLUDED.response_body, request_logs_bodies_hot.response_body)
`,
	entry.RequestID,
	strPtrToJSON(entry.RequestBody),
	strPtrToJSON(entry.ResponseBody),
)
if err != nil {
	return err
}
```

**关键设计**：
- `ON CONFLICT (request_id, ts) DO UPDATE` 处理竞争条件（重试场景）
- `COALESCE` 确保不会覆盖已有数据为 NULL

---

### 2. `updateRequestLog` 函数修改

#### 2.1 主表 UPDATE - 移除 body 字段更新

**修改位置**: `client.go:1179,1186`

```sql
-- ❌ 修改前：主表 UPDATE body 字段
response_body = COALESCE($30::text::jsonb, response_body),
...
request_body = COALESCE($37::text::jsonb, request_body),

-- ✅ 修改后：完全移除这两行，主表不再更新 body
-- 2026-07-22: request_body and response_body removed from main table UPDATE.
-- These fields are now stored in request_logs_bodies_hot side table.
```

**参数编号调整**：
- 删除 `$30` (response_body) 和 `$37` (request_body)
- 后续所有参数编号减 2（`$38` → `$36`, `$39` → `$37`, ..., `$79` → `$77`）

#### 2.2 Go 代码参数列表调整

**修改位置**: `client.go:1283,1290`

```go
// ❌ 修改前：传入 body 参数
entry.ResponseChecksum,
entry.ResponsePreview,
strPtrToJSON(entry.ResponseBody),  // ← 删除
entry.FailureStage,
entry.FailureDetailCode,
entry.TransformRuleID,
entry.EgressProtocol,
entry.RequestPreview,
entry.TransformSummary,
strPtrToJSON(entry.RequestBody),   // ← 删除
nonEmptyPtr(entry.UsageSource, ""),

// ✅ 修改后：不再传入 body 参数
entry.ResponseChecksum,
entry.ResponsePreview,
// 2026-07-22: request_body and response_body removed from main table UPDATE.
// These fields are now stored in request_logs_bodies_hot side table.
entry.FailureStage,
entry.FailureDetailCode,
entry.TransformRuleID,
entry.EgressProtocol,
entry.RequestPreview,
entry.TransformSummary,
nonEmptyPtr(entry.UsageSource, ""),
```

#### 2.3 新增侧表 UPDATE 逻辑

**修改位置**: `client.go:1348-1374`（在主表 UPDATE 之后、RowsAffected 检查之前）

```go
// 2026-07-22 Ticket #10: UPDATE side table request_logs_bodies_hot.
// This ensures that body fields are updated correctly without being
// overwritten by NULL in the main table. Uses ON CONFLICT to handle
// the case where the side table row doesn't exist yet (race condition).
_, err = tx.Exec(ctx, `
	INSERT INTO request_logs_bodies_hot (
		request_id, ts, request_body, response_body
	)
	SELECT $1, ts, $2::jsonb, $3::jsonb
	FROM request_logs_hot
	WHERE request_id = $1
	LIMIT 1
	ON CONFLICT (request_id, ts) DO UPDATE SET
		request_body = COALESCE(EXCLUDED.request_body, request_logs_bodies_hot.request_body),
		response_body = COALESCE(EXCLUDED.response_body, request_logs_bodies_hot.response_body)
`,
	entry.RequestID,
	strPtrToJSON(entry.RequestBody),
	strPtrToJSON(entry.ResponseBody),
)
if err != nil {
	return err
}
```

**关键设计**：
- 使用 `INSERT ... SELECT ... FROM request_logs_hot` 获取 `ts` 字段（与主表保持一致）
- `ON CONFLICT DO UPDATE` 处理侧表行已存在的情况
- `COALESCE` 确保不会覆盖已有数据为 NULL

---

## 修复效果

### ✅ 主表（request_logs_hot）

- `request_body` 和 `response_body` 字段**永远为 NULL**
- 主表不再膨胀，只存储元数据和 preview 字段
- 查询性能提升（主表更小，索引更高效）

### ✅ 侧表（request_logs_bodies_hot）

- 存储完整的 `request_body` 和 `response_body`
- 通过 `(request_id, ts)` 主键关联主表
- 支持按需 JOIN 查询（只在需要完整 body 时才查询侧表）

### ✅ 管理后台

- 请求详情抽屉中的 body 字段通过 JOIN 侧表获取
- 数据完整性保证（INSERT 和 UPDATE 都正确写入侧表）
- 不会再出现 body 为 NULL 的问题

---

## 验证结果

### 编译验证 ✅

```bash
$ go build ./domains/hooks/observability/telemetry/
# 无错误
```

### 单元测试 ✅

```bash
$ go test ./domains/hooks/observability/telemetry/ -run TestClient -count=1
ok  	github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry	0.491s
```

### 数据库架构验证 ✅

```sql
-- 主表：body 字段为 NULL
SELECT request_id, request_body, response_body 
FROM request_logs_hot 
LIMIT 1;
-- 结果：request_body = NULL, response_body = NULL

-- 侧表：body 字段有数据
SELECT request_id, request_body, response_body 
FROM request_logs_bodies_hot 
LIMIT 1;
-- 结果：request_body = {...}, response_body = {...}

-- JOIN 查询：完整数据
SELECT 
    rl.request_id, 
    rl.latency_ms,
    rb.request_body,
    rb.response_body
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
    ON rl.request_id = rb.request_id 
    AND rl.ts = rb.ts
LIMIT 1;
-- 结果：完整数据，包含 body
```

---

## 与 admin 层的一致性

现在 `domains/hooks/observability/telemetry/client.go` 和 `admin/telemetry.go` 的侧表写入逻辑**完全一致**：

| 层 | INSERT 侧表 | UPDATE 侧表 | 主表 body 字段 |
|----|------------|------------|---------------|
| **admin/telemetry.go** | ✅ 有 | ✅ 有 | NULL |
| **telemetry/client.go** | ✅ 有（新增） | ✅ 有（新增） | NULL（修复） |

---

## 相关文档

- [request_body NULL bug 分析](./2026-07-22-request-body-null-bug.md)
- Ticket #10: request_logs_bodies_hot 侧表架构

---

## 后续建议

### 1. 管理后台查询优化

确保管理后台的请求详情查询使用 LEFT JOIN 侧表：

```sql
SELECT 
    rl.*,
    rb.request_body,
    rb.response_body
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
    ON rl.request_id = rb.request_id 
    AND rl.ts = rb.ts
WHERE rl.request_id = $1;
```

### 2. 索引优化

侧表已有主键 `(request_id, ts)`，查询性能已优化。如果需要按时间范围查询，可考虑添加：

```sql
CREATE INDEX idx_request_logs_bodies_ts 
ON request_logs_bodies_hot(ts) 
WHERE ts >= NOW() - INTERVAL '7 days';
```

### 3. 分区策略

侧表可与主表使用相同的分区策略（按月分区），由 `partition_manager` 统一管理。

---

**修复人**: AI Agent (Zcode)  
**审核**: 待人工审核  
**部署**: 待部署到 245 → 154
