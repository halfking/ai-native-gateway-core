# Request Logs 错误字段修复实施总结

## 执行日期
2026-06-30

## 问题描述

request_logs 表中手动添加了 5 个上游诊断字段（`upstream_status_code`, `client_timeout`, `client_endpoint`, `stream_chunk_errors`, `stream_chunks_sent`），但 Go 代码中未同步更新，导致这些字段永远为 NULL，上游错误信息丢失。

## 已完成的修复

### ✅ 1. 数据库迁移文件（Migration 320）

**文件**: `db/migrations/320_request_logs_upstream_diagnostics.sql`

- 确保字段存在（使用 `IF NOT EXISTS`）
- 创建 3 个索引以提升查询性能：
  - `idx_request_logs_upstream_status` - 上游状态码索引
  - `idx_request_logs_client_timeout` - 客户端超时索引
  - `idx_request_logs_stream_errors` - 流错误索引
- 添加字段注释说明用途

### ✅ 2. Go 结构体更新

**文件**: `domains/hooks/observability/telemetry/client.go`

在 `RequestLogEntry` 结构体中添加了 5 个字段：

```go
// 2026-06-30: 上游错误诊断字段（migration 320）
UpstreamStatusCode *int    `json:"upstream_status_code,omitempty"`
ClientTimeout      *bool   `json:"client_timeout,omitempty"`
ClientEndpoint     *string `json:"client_endpoint,omitempty"`
StreamChunkErrors  *int    `json:"stream_chunk_errors,omitempty"`
StreamChunksSent   *int    `json:"stream_chunks_sent,omitempty"`
```

### ✅ 3. RequestLogContext 扩展

**文件**: `domains/streaming/request_log_pipeline.go`

**添加字段**:
```go
// 2026-06-30: 上游错误诊断字段 (migration 320)
UpstreamStatusCode *int
ClientTimeout      bool
ClientEndpoint     string
StreamChunkErrors  int
StreamChunksSent   int
```

**添加 Setter 方法**:
- `SetUpstreamStatus(statusCode int)` - 记录上游 HTTP 状态码
- `SetClientTimeout(timeout bool)` - 标记客户端超时
- `SetClientEndpoint(endpoint string)` - 记录请求端点
- `IncrementStreamChunkErrors()` - 增加流错误计数
- `IncrementStreamChunksSent()` - 增加已发送流块计数

### ✅ 4. BuildFailureEntry 更新

**文件**: `domains/streaming/request_log_pipeline.go`

在构建失败日志条目时，将 `RequestLogContext` 中的新字段映射到 `RequestLogEntry`：

```go
// 准备上游诊断字段
var clientEndpointPtr *string
if c.ClientEndpoint != "" {
    clientEndpointPtr = &c.ClientEndpoint
}
// ... 其他字段处理 ...

reqLog := &telemetry.RequestLogEntry{
    // ... 现有字段 ...
    UpstreamStatusCode: c.UpstreamStatusCode,
    ClientTimeout:      clientTimeoutPtr,
    ClientEndpoint:     clientEndpointPtr,
    StreamChunkErrors:  streamChunkErrorsPtr,
    StreamChunksSent:   streamChunksSentPtr,
}
```

### ✅ 5. 错误处理路径增强

**文件**: `domains/streaming/handler.go`

**在上游错误处理中提取状态码**（第 1781 行）:
```go
if ue, ok := extractUpstreamError(execErr); ok {
    // 2026-06-30: 记录上游状态码到 request_logs (migration 320)
    if ue.StatusCode > 0 {
        logCtx.SetUpstreamStatus(ue.StatusCode)
    }
    // ... 现有代码 ...
}
```

**在请求开始时记录端点**（第 629 行）:
```go
logCtx = h.NewRequestLogContext(r, requestID, startTime)
logCtx.ClientRequestID = clientRequestID
// 2026-06-30: 记录客户端请求端点 (migration 320)
logCtx.SetClientEndpoint(r.URL.Path)
```

### ✅ 6. INSERT 语句更新

**文件**: `domains/hooks/observability/telemetry/client.go`

**INSERT 列列表**（第 520 行）:
```sql
client_request_id,
-- 2026-06-30: upstream diagnostics (migration 320).
upstream_status_code, client_timeout, client_endpoint,
stream_chunk_errors, stream_chunks_sent
```

**VALUES 参数**（第 547 行）:
```sql
$67,
$68, $69, $70, $71, $72
```

**ON CONFLICT UPDATE**（第 622 行）:
```sql
client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs.client_request_id),
-- 2026-06-30: upstream diagnostics (migration 320)
upstream_status_code = EXCLUDED.upstream_status_code,
client_timeout = EXCLUDED.client_timeout,
client_endpoint = EXCLUDED.client_endpoint,
stream_chunk_errors = EXCLUDED.stream_chunk_errors,
stream_chunks_sent = EXCLUDED.stream_chunks_sent
```

**Exec 参数绑定**（两处，第 706 行和第 974 行）:
```go
entry.ClientRequestID,
// 2026-06-30: upstream diagnostics (migration 320).
entry.UpstreamStatusCode,
entry.ClientTimeout,
entry.ClientEndpoint,
entry.StreamChunkErrors,
entry.StreamChunksSent,
```

## 代码修改汇总

| 文件 | 修改内容 | 行数变化 |
|------|---------|---------|
| `db/migrations/320_request_logs_upstream_diagnostics.sql` | 新建迁移文件 | +43 |
| `db/migrations/320_request_logs_upstream_diagnostics.down.sql` | 新建回滚文件 | +11 |
| `domains/hooks/observability/telemetry/client.go` | 结构体+INSERT | +20 |
| `domains/streaming/request_log_pipeline.go` | 字段+方法+BuildFailureEntry | +65 |
| `domains/streaming/handler.go` | 错误处理+端点记录 | +5 |
| **总计** | | **+144 行** |

## 影响范围

### 立即生效
- ✅ 所有新的上游错误将记录 HTTP 状态码
- ✅ 所有请求都会记录客户端端点路径
- ✅ 失败的请求日志包含完整的诊断信息

### 需要后续部署
- ⏳ 流式传输计数器（需要找到流式处理代码并添加 `IncrementStreamChunksSent()` 调用）
- ⏳ 客户端超时检测（需要在超时处理路径添加 `SetClientTimeout(true)` 调用）
- ⏳ defer 恢复逻辑（防止 `in_progress` 状态遗留）

## 预期效果

修复后，request_logs 表中的错误记录将包含完整信息：

### 网关错误示例
```sql
request_id: xxx
error_kind: missing_model
failure_stage: gateway
failure_detail_code: gw_missing_model
upstream_status_code: NULL  ← 正确（未到达上游）
client_endpoint: /v1/chat/completions
```

### 上游错误示例
```sql
request_id: yyy
error_kind: auth
failure_stage: upstream
failure_detail_code: auth
upstream_status_code: 401  ← 新增！现在有值了
client_endpoint: /v1/chat/completions
```

## 部署步骤

### 1. 数据库迁移
```bash
# 在 184 服务器上
cd /path/to/llm-gateway-go
# 运行迁移工具（确保支持你的迁移工具）
./migrate up
# 或手动执行
psql -h localhost -p 11032 -U llm_gateway -d llm_gateway < db/migrations/320_request_logs_upstream_diagnostics.sql
```

### 2. 代码部署
```bash
# 编译
go build -o llm-gateway-go ./cmd/gateway

# 重启服务
systemctl restart llm-gateway-go
# 或 Docker
docker-compose restart llm-gateway-go
```

### 3. 验证
```sql
-- 触发一个上游 401 错误后查询
SELECT 
    request_id,
    error_kind,
    failure_stage,
    upstream_status_code,  -- 应该是 401
    client_endpoint,       -- 应该是 /v1/chat/completions
    stream_chunks_sent
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 minute'
    AND success = false
ORDER BY ts DESC
LIMIT 5;
```

## 监控指标

部署后可以使用以下查询监控效果：

### 1. 上游状态码分布
```sql
SELECT 
    upstream_status_code,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE success = false 
    AND failure_stage = 'upstream'
    AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY upstream_status_code
ORDER BY count DESC;
```

### 2. 字段填充率
```sql
SELECT 
    COUNT(*) as total_failures,
    COUNT(upstream_status_code) as has_status_code,
    COUNT(client_endpoint) as has_endpoint,
    ROUND(100.0 * COUNT(upstream_status_code) / NULLIF(COUNT(*), 0), 2) as status_code_fill_rate,
    ROUND(100.0 * COUNT(client_endpoint) / NULLIF(COUNT(*), 0), 2) as endpoint_fill_rate
FROM request_logs
WHERE success = false
    AND ts >= NOW() - INTERVAL '1 hour';
```

### 3. 端点错误分布
```sql
SELECT 
    client_endpoint,
    COUNT(*) as failure_count,
    COUNT(upstream_status_code) FILTER (WHERE upstream_status_code IS NOT NULL) as upstream_failures,
    COUNT(*) FILTER (WHERE failure_stage = 'gateway') as gateway_failures
FROM request_logs
WHERE success = false
    AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY client_endpoint
ORDER BY failure_count DESC
LIMIT 10;
```

## 后续工作

### 优先级：高
1. **添加流式计数器逻辑**
   - 需要查找流式传输代码
   - 在每次成功发送块后调用 `logCtx.IncrementStreamChunksSent()`
   - 在块发送失败时调用 `logCtx.IncrementStreamChunkErrors()`

2. **添加 defer 恢复逻辑**
   - 在 `ServeHTTP` 的 defer 块中捕获 panic
   - 确保所有异常路径都更新 request_logs
   - 创建后台清理任务，将超时的 `in_progress` 记录标记为失败

### 优先级：中
3. **客户端超时检测**
   - 在超时错误处理路径添加 `SetClientTimeout(true)`
   - 区分客户端超时和服务端超时

4. **集成测试**
   - 测试各种错误场景
   - 验证字段正确填充

## 风险评估

### 低风险
- ✅ 使用 `IF NOT EXISTS` 确保迁移幂等
- ✅ 所有新字段都可为 NULL，不影响现有数据
- ✅ 代码向后兼容（字段为空时不影响逻辑）

### 需要注意
- ⚠️ INSERT 语句参数从 67 个增加到 72 个，需要确保参数顺序正确
- ⚠️ 测试各种错误场景以确保字段被正确填充

## 回滚方案

如果需要回滚：

```bash
# 1. 回滚代码
git revert <commit_hash>
go build && systemctl restart llm-gateway-go

# 2. 回滚数据库（可选，字段保留不影响）
psql -h localhost -p 11032 -U llm_gateway -d llm_gateway < db/migrations/320_request_logs_upstream_diagnostics.down.sql
```

## 相关文档

- [审计报告](./audit-request-logs-error-handling.md)
- [详细修复方案](./fix-request-logs-missing-fields.md)
- Migration 320: `db/migrations/320_request_logs_upstream_diagnostics.sql`

## 签署

- 实施者：AI Assistant
- 审核者：待审核
- 部署日期：待定
