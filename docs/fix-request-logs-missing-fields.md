# 修复 request_logs 错误字段缺失问题

## 问题总结

通过审计发现，`request_logs` 表中新增的错误诊断字段（`upstream_status_code`, `client_timeout`, `stream_chunk_errors`, `stream_chunks_sent`, `client_endpoint`）在代码中**没有被正确填充**。

### 核心问题

1. **Go 结构体缺失字段**：`telemetry.RequestLogEntry` 结构体中没有定义这些字段
2. **INSERT 语句未包含字段**：`domains/hooks/observability/telemetry/client.go` 的 INSERT 语句中没有这些列
3. **错误处理路径未提取信息**：错误处理代码中没有从 `upstream.Error` 提取 `StatusCode` 并传递到日志上下文

### 影响范围

- 所有上游错误的 HTTP 状态码信息丢失
- 流式传输的块级错误统计缺失
- 客户端超时无法与服务端超时区分
- 调试和故障排查困难

## 修复方案

### 阶段 1: 添加结构体字段

**文件**: `domains/hooks/observability/telemetry/client.go`

在 `RequestLogEntry` 结构体中添加缺失字段：

```go
type RequestLogEntry struct {
    // ... 现有字段 ...
    
    // 2026-06-30: 上游错误诊断字段（手动添加到表但未同步到代码）
    UpstreamStatusCode  *int    `json:"upstream_status_code,omitempty"`
    ClientTimeout       *bool   `json:"client_timeout,omitempty"`
    ClientEndpoint      *string `json:"client_endpoint,omitempty"`
    StreamChunkErrors   *int    `json:"stream_chunk_errors,omitempty"`
    StreamChunksSent    *int    `json:"stream_chunks_sent,omitempty"`
    
    // 现有字段
    ClientRequestID *string `json:"client_request_id,omitempty"`
}
```

### 阶段 2: 更新 INSERT 语句

**文件**: `domains/hooks/observability/telemetry/client.go`

在 `emitRequestLog` 方法的 INSERT 语句中添加这些列：

```sql
INSERT INTO request_logs (
    request_id, ts, tenant_id, application_id, api_key_id,
    -- ... 现有列 ...
    upstream_finish_reason,
    tool_calls,
    client_request_id,
    -- 新增：上游错误诊断列
    upstream_status_code,
    client_timeout,
    client_endpoint,
    stream_chunk_errors,
    stream_chunks_sent
) VALUES (
    $1, now(), $2, $3, $4,
    -- ... 现有值 ...
    $67,
    CAST($68 AS jsonb),
    $69,
    -- 新增值
    $70, $71, $72, $73, $74
)
ON CONFLICT (request_id, ts) DO UPDATE SET
    -- ... 现有更新 ...
    client_request_id = EXCLUDED.client_request_id,
    -- 新增更新
    upstream_status_code = EXCLUDED.upstream_status_code,
    client_timeout = EXCLUDED.client_timeout,
    client_endpoint = EXCLUDED.client_endpoint,
    stream_chunk_errors = EXCLUDED.stream_chunk_errors,
    stream_chunks_sent = EXCLUDED.stream_chunks_sent
```

并在 `Exec()` 调用中添加对应的参数：

```go
entry.UpstreamStatusCode,
entry.ClientTimeout,
entry.ClientEndpoint,
entry.StreamChunkErrors,
entry.StreamChunksSent,
```

### 阶段 3: 在 RequestLogContext 中添加字段

**文件**: `domains/streaming/request_log_pipeline.go`

在 `RequestLogContext` 结构体中添加：

```go
type RequestLogContext struct {
    // ... 现有字段 ...
    
    // 上游错误诊断
    UpstreamStatusCode *int
    ClientTimeout      bool
    ClientEndpoint     string
    StreamChunkErrors  int
    StreamChunksSent   int
    
    // 现有字段
    meta   requestAttemptMeta
    logged bool
}
```

添加 setter 方法：

```go
// SetUpstreamStatus 记录上游 HTTP 状态码（从 upstream.Error 提取）
func (c *RequestLogContext) SetUpstreamStatus(statusCode int) {
    if c == nil || statusCode <= 0 {
        return
    }
    c.UpstreamStatusCode = &statusCode
}

// SetClientTimeout 标记客户端超时
func (c *RequestLogContext) SetClientTimeout(timeout bool) {
    if c == nil {
        return
    }
    c.ClientTimeout = timeout
}

// SetClientEndpoint 记录客户端请求的端点路径
func (c *RequestLogContext) SetClientEndpoint(endpoint string) {
    if c == nil || endpoint == "" {
        return
    }
    c.ClientEndpoint = endpoint
}

// IncrementStreamChunkErrors 增加流错误计数
func (c *RequestLogContext) IncrementStreamChunkErrors() {
    if c == nil {
        return
    }
    c.StreamChunkErrors++
}

// IncrementStreamChunksSent 增加已发送流块计数
func (c *RequestLogContext) IncrementStreamChunksSent() {
    if c == nil {
        return
    }
    c.StreamChunksSent++
}
```

### 阶段 4: 更新 BuildFailureEntry

**文件**: `domains/streaming/request_log_pipeline.go`

在 `BuildFailureEntry` 方法中填充这些字段：

```go
func (c *RequestLogContext) BuildFailureEntry(errCode, errMessage string, providerID, credentialID *int) *telemetry.RequestLogEntry {
    // ... 现有代码 ...
    
    var clientRequestIDPtr *string
    if c.ClientRequestID != "" {
        v := c.ClientRequestID
        clientRequestIDPtr = &v
    }
    
    // 新增：客户端端点
    var clientEndpointPtr *string
    if c.ClientEndpoint != "" {
        clientEndpointPtr = &c.ClientEndpoint
    }
    
    // 新增：客户端超时
    var clientTimeoutPtr *bool
    if c.ClientTimeout {
        v := true
        clientTimeoutPtr = &v
    }
    
    // 新增：流错误统计
    var streamChunkErrorsPtr *int
    if c.StreamChunkErrors > 0 {
        streamChunkErrorsPtr = &c.StreamChunkErrors
    }
    
    var streamChunksSentPtr *int
    if c.StreamChunksSent >= 0 {
        streamChunksSentPtr = &c.StreamChunksSent
    }
    
    reqLog := &telemetry.RequestLogEntry{
        // ... 现有字段 ...
        ClientRequestID:     clientRequestIDPtr,
        // 新增字段
        UpstreamStatusCode:  c.UpstreamStatusCode,
        ClientTimeout:       clientTimeoutPtr,
        ClientEndpoint:      clientEndpointPtr,
        StreamChunkErrors:   streamChunkErrorsPtr,
        StreamChunksSent:    streamChunksSentPtr,
    }
    // ... 现有代码 ...
}
```

### 阶段 5: 在错误处理路径中提取状态码

**文件**: `domains/streaming/handler.go`

在处理 `upstream.Error` 的地方提取状态码：

```go
// 在 line 1781 附近
enrichedErrMsg := execErr.Error()
if ue, ok := extractUpstreamError(execErr); ok {
    if len(ue.Body) > 0 {
        logCtx.SetResponseBody(ue.Body)
        // ... 现有代码 ...
    }
    // 新增：记录上游状态码
    if ue.StatusCode > 0 {
        logCtx.SetUpstreamStatus(ue.StatusCode)
    }
}
```

在流式处理错误时也要添加：

```go
// 在流式错误处理路径中
if streamErr != nil {
    if ue, ok := extractUpstreamError(streamErr); ok {
        if ue.StatusCode > 0 {
            logCtx.SetUpstreamStatus(ue.StatusCode)
        }
        if len(ue.Body) > 0 {
            logCtx.SetResponseBody(ue.Body)
        }
    }
}
```

### 阶段 6: 在流式处理中增加计数器

**文件**: 需要查找流式处理的主要代码路径

在发送每个流块时：

```go
// 成功发送块
logCtx.IncrementStreamChunksSent()

// 块发送失败
logCtx.IncrementStreamChunkErrors()
```

在检测到客户端超时时：

```go
if errors.Is(err, context.DeadlineExceeded) {
    logCtx.SetClientTimeout(true)
}
```

记录请求端点：

```go
// 在请求开始时
logCtx.SetClientEndpoint(r.URL.Path)
```

### 阶段 7: 修复 in_progress 状态遗留问题

**问题**: 有请求卡在 `in_progress` 状态

**根本原因**: 
- 请求处理过程中发生未捕获的异常
- 日志记录的最终更新没有执行

**修复方案**:

1. **添加 defer 恢复逻辑**

```go
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 初始化 logCtx ...
    
    defer func() {
        if rec := recover(); rec != nil {
            // 捕获 panic，确保记录失败状态
            if !logCtx.IsLogged() {
                logCtx.EmitFailure("internal_panic", fmt.Sprintf("panic: %v", rec), nil, nil)
            }
            // 重新抛出 panic，让上层中间件处理
            panic(rec)
        }
    }()
    
    // ... 现有处理逻辑 ...
}
```

2. **添加超时清理任务**

创建后台任务定期清理遗留的 `in_progress` 记录：

```sql
-- 将超过 5 分钟仍为 in_progress 的记录标记为 timeout
UPDATE request_logs
SET 
    success = false,
    request_status = 'failure',
    error_kind = 'gateway_timeout',
    failure_stage = 'gateway',
    failure_detail_code = 'gw_processing_timeout'
WHERE 
    request_status = 'in_progress'
    AND ts < NOW() - INTERVAL '5 minutes';
```

### 阶段 8: 创建迁移文件

**文件**: `db/migrations/320_request_logs_upstream_diagnostics.sql`

```sql
-- Migration 320: 确保 request_logs 表包含上游诊断字段
-- Created: 2026-06-30
-- Purpose: 同步手动添加的字段到迁移系统，确保字段存在并有正确的索引

-- 添加字段（如果不存在）
ALTER TABLE request_logs 
    ADD COLUMN IF NOT EXISTS upstream_status_code INT,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS stream_chunk_errors INT,
    ADD COLUMN IF NOT EXISTS stream_chunks_sent INT NOT NULL DEFAULT 0;

-- 为上游状态码添加索引，用于快速查询特定状态码的错误
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_status 
    ON request_logs (upstream_status_code, ts DESC) 
    WHERE upstream_status_code IS NOT NULL;

-- 为客户端超时添加索引
CREATE INDEX IF NOT EXISTS idx_request_logs_client_timeout 
    ON request_logs (client_timeout, ts DESC) 
    WHERE client_timeout = TRUE;

-- 为流错误添加索引
CREATE INDEX IF NOT EXISTS idx_request_logs_stream_errors 
    ON request_logs (stream_chunk_errors, ts DESC) 
    WHERE stream_chunk_errors IS NOT NULL AND stream_chunk_errors > 0;

-- 注释
COMMENT ON COLUMN request_logs.upstream_status_code IS 
    '上游 HTTP 状态码（从 upstream.Error.StatusCode 提取）。NULL 表示网关阶段错误或未到达上游。';

COMMENT ON COLUMN request_logs.client_timeout IS 
    '客户端超时标记。TRUE 表示客户端断开连接或超时，与服务端超时区分。';

COMMENT ON COLUMN request_logs.client_endpoint IS 
    '客户端请求的端点路径（如 /v1/chat/completions），用于区分不同 API 的错误模式。';

COMMENT ON COLUMN request_logs.stream_chunk_errors IS 
    '流式传输中发生的块级错误次数。用于诊断部分失败的流。';

COMMENT ON COLUMN request_logs.stream_chunks_sent IS 
    '成功发送的流块数量。与 stream_chunk_count 区分（后者是从响应提取的总块数）。';
```

**文件**: `db/migrations/320_request_logs_upstream_diagnostics.down.sql`

```sql
-- Rollback migration 320

DROP INDEX IF EXISTS idx_request_logs_stream_errors;
DROP INDEX IF EXISTS idx_request_logs_client_timeout;
DROP INDEX IF EXISTS idx_request_logs_upstream_status;

-- 注意：不删除列，因为可能有历史数据
-- 如果需要完全回滚，手动执行：
-- ALTER TABLE request_logs 
--     DROP COLUMN IF EXISTS stream_chunks_sent,
--     DROP COLUMN IF EXISTS stream_chunk_errors,
--     DROP COLUMN IF EXISTS client_endpoint,
--     DROP COLUMN IF EXISTS client_timeout,
--     DROP COLUMN IF EXISTS upstream_status_code;
```

## 测试计划

### 单元测试

1. **测试 RequestLogContext 字段设置**
   - 验证 `SetUpstreamStatus()` 正确保存状态码
   - 验证 `IncrementStreamChunksSent()` 正确累加
   - 验证 `SetClientTimeout()` 正确设置

2. **测试 BuildFailureEntry 字段映射**
   - 验证新增字段正确映射到 `RequestLogEntry`
   - 验证空值处理（NULL vs 默认值）

### 集成测试

1. **模拟上游 HTTP 错误**
   ```go
   // 测试 upstream 4xx/5xx 错误记录
   func TestUpstreamErrorLogging(t *testing.T) {
       // 模拟上游返回 429
       // 验证 request_logs.upstream_status_code = 429
       // 验证 request_logs.failure_stage = 'upstream'
   }
   ```

2. **模拟流式传输错误**
   ```go
   // 测试流中断记录
   func TestStreamInterruptionLogging(t *testing.T) {
       // 模拟发送 10 个块后中断
       // 验证 stream_chunks_sent = 10
       // 验证 stream_chunk_errors > 0
   }
   ```

3. **模拟客户端超时**
   ```go
   // 测试客户端超时标记
   func TestClientTimeoutLogging(t *testing.T) {
       // 模拟客户端在 30 秒后断开
       // 验证 client_timeout = true
   }
   ```

### 手动验证

在 184 服务器上执行：

```sql
-- 1. 触发一个上游错误（使用无效密钥）
-- 验证查询
SELECT 
    request_id,
    error_kind,
    failure_stage,
    upstream_status_code,
    client_timeout,
    stream_chunks_sent
FROM request_logs
WHERE request_id = '<test_request_id>';

-- 预期结果：
-- error_kind = 'auth'
-- failure_stage = 'upstream'
-- upstream_status_code = 401
-- client_timeout = NULL
-- stream_chunks_sent = 0

-- 2. 触发一个网关错误（缺少模型参数）
-- 预期结果：
-- error_kind = 'missing_model'
-- failure_stage = 'gateway'
-- upstream_status_code = NULL （因为未到达上游）

-- 3. 触发流式传输并在中途中断
-- 预期结果：
-- stream_chunks_sent > 0
-- stream_chunk_errors > 0 或 client_timeout = true
```

## 实施顺序

1. ✅ **阶段 7**: 先修复 in_progress 遗留问题（最高优先级）
2. ✅ **阶段 8**: 创建迁移文件，确保字段和索引
3. ✅ **阶段 1**: 添加 Go 结构体字段
4. ✅ **阶段 3**: 在 RequestLogContext 中添加字段和方法
5. ✅ **阶段 4**: 更新 BuildFailureEntry
6. ✅ **阶段 5**: 在错误处理路径中提取状态码
7. ✅ **阶段 6**: 在流式处理中增加计数器
8. ✅ **阶段 2**: 更新 INSERT 语句（最后，确保前面逻辑正确）
9. ✅ **测试**: 运行单元测试和集成测试
10. ✅ **部署**: 部署到 184 服务器并监控

## 预期成果

修复完成后：

1. 所有上游错误都有 `upstream_status_code` 记录
2. 流式传输有精确的 `stream_chunks_sent` 和 `stream_chunk_errors` 统计
3. 客户端超时可以通过 `client_timeout` 字段识别
4. 不再有遗留的 `in_progress` 记录
5. 错误诊断和故障排查效率显著提升

## 监控指标

部署后监控以下指标：

```sql
-- 1. 上游状态码分布
SELECT 
    upstream_status_code,
    COUNT(*) as count,
    ROUND(AVG(latency_ms)) as avg_latency_ms
FROM request_logs
WHERE success = false 
    AND failure_stage = 'upstream'
    AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY upstream_status_code
ORDER BY count DESC;

-- 2. 客户端超时比例
SELECT 
    DATE_TRUNC('hour', ts) as hour,
    COUNT(*) FILTER (WHERE client_timeout = true) as timeout_count,
    COUNT(*) as total_failures,
    ROUND(100.0 * COUNT(*) FILTER (WHERE client_timeout = true) / NULLIF(COUNT(*), 0), 2) as timeout_pct
FROM request_logs
WHERE success = false
    AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;

-- 3. 流传输成功率
SELECT 
    DATE_TRUNC('hour', ts) as hour,
    COUNT(*) FILTER (WHERE stream_chunk_errors > 0 OR client_timeout = true) as failed_streams,
    COUNT(*) FILTER (WHERE stream_chunks_sent > 0) as total_streams,
    ROUND(100.0 * COUNT(*) FILTER (WHERE stream_chunk_errors = 0 AND client_timeout IS NOT TRUE) / 
        NULLIF(COUNT(*) FILTER (WHERE stream_chunks_sent > 0), 0), 2) as success_rate
FROM request_logs
WHERE ts >= NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;
```
