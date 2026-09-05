# 实时请求流数据问题诊断与修复方案

## 问题总结

### 问题1：前端泳道展示数据跳变
**现象**：多维度泳道图中数据大幅跳变，不像增量更新
**根因**：
1. 查询时间窗口不连续（可能跨越分区边界）
2. SSE推送与轮询查询的数据源不一致
3. 前端本地缓存失效导致全量重建

### 问题2：请求刷新延迟 >5s
**现象**：前端请求到达网关，但网关层面延迟5秒才能看到
**根因**：
1. **INSERT 时机太晚**：当前只在 `EmitRequestLogInsert` 时调用，但实际调用时机在响应流结束后
2. **批处理延迟**：telemetry worker 使用 200ms 批处理窗口 + 最多50条才flush
3. **数据库写入延迟**：request_logs_hot 的 INSERT 本身需要时间

### 问题3：probe-direct 请求缺失消息和响应
**现象**：探测请求详情中没有 request_body / response_body / token 统计
**根因**：
1. `active_probe_emitter.go` 只调用 `EmitRequestLogInsert`，**从未调用 UPDATE**
2. ProbeResult 中有 RequestBody/ResponseBody，但未传递给 telemetry.RequestLogEntry
3. token 统计在 emitter 中是从响应解析的，但未持久化到 request_body/response_body 列

## 修复方案

### 修复1：前端泳道展示稳定性

#### 1.1 统一数据源
```sql
-- 确保查询始终从 request_logs_hot 读取（0-7天热数据）
-- 避免跨分区查询导致的时间窗口不连续
SELECT * FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '7 days'
  AND tenant_id = $1
ORDER BY ts DESC;
```

#### 1.2 SSE 推送与轮询对齐
```go
// 确保 onEmitted 推送的数据结构与 API 返回的一致
// 两者都必须包含完整的维度字段（provider_id, credential_id, model, status）
```

#### 1.3 前端增量更新策略
```typescript
// 前端改为增量更新而非全量替换
// 使用 request_id 作为唯一键，避免重复
const existingIds = new Set(currentData.map(d => d.request_id));
const newItems = sseData.filter(d => !existingIds.has(d.request_id));
setData([...newItems, ...currentData].slice(0, MAX_ITEMS));
```

### 修复2：请求刷新实时性（双写策略）

#### 2.1 请求开始时立即写入占位行

**目标**：请求进入中间件时立即 INSERT，而非等待响应完成

```go
// middleware/requestid_mw.go 或 handler 入口处
func (h *Handler) handleRequest(w http.ResponseWriter, r *http.Request) {
    requestID := middleware.GetRequestID(r.Context())
    startTime := time.Now()

    // ✅ 立即插入占位行（in_progress 状态）
    h.telemetry.EmitRequestLogInsert(&telemetry.RequestLogEntry{
        RequestID:     requestID,
        TenantID:      getTenantID(r.Context()),
        RequestStatus: strPtr(telemetry.RequestStatusInProgress),
        ClientModel:   strPtr(getClientModel(r)),
        // 其他基础字段...
        Op: telemetry.RequestLogInsert,
    })

    // 继续处理请求...
    // defer 中再 UPDATE 完整信息
    defer func() {
        h.telemetry.EmitRequestLogUpdate(&telemetry.RequestLogEntry{
            RequestID:        requestID,
            Success:          success,
            RequestStatus:    strPtr(telemetry.RequestStatusSuccess),
            LatencyMs:        intPtr(int(time.Since(startTime).Milliseconds())),
            PromptTokens:     &promptTokens,
            CompletionTokens: &completionTokens,
            // 完整的响应数据...
            Op: telemetry.RequestLogUpdate,
        })
    }()
}
```

#### 2.2 优化批处理策略

```go
// telemetry/client.go worker 函数
// 当前：200ms 或 50 条
// 优化：对于 INSERT 立即发送，UPDATE 可以批处理

func (c *Client) EmitRequestLog(entry *RequestLogEntry) {
    if entry.Op == RequestLogInsert {
        // INSERT 立即持久化（绕过队列）
        if err := c.persistRequestLog(entry); err != nil {
            slog.Warn("telemetry insert failed", "request_id", entry.RequestID, "err", err)
        }
    } else {
        // UPDATE 走批处理队列
        select {
        case c.queue <- entry:
        default:
            // 队列满时同步写入
            c.persistRequestLog(entry)
        }
    }

    // onEmitted hook 立即触发（SSE推送）
    if c.onEmitted != nil {
        c.onEmitted(entry)
    }
}
```

### 修复3：probe-direct 请求完整性

#### 3.1 在 ProbeResult 中携带完整请求/响应

```go
// bg/active_probe_executor.go Run 方法
func (e *ActiveProbeExecutor) Run(ctx context.Context, target *ProbeTarget, model string) *ProbeResult {
    startedAt := time.Now()

    // 构造探测请求
    reqBody := buildMinimalChatRequest(model)
    reqBodyStr, _ := json.Marshal(reqBody)

    req, _ := http.NewRequestWithContext(ctx, "POST", target.BaseURL, bytes.NewReader(reqBodyStr))
    // ... 设置headers

    resp, err := e.httpClient.Do(req)
    // ... 处理响应

    respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

    return &ProbeResult{
        Status:       status,
        HTTPStatus:   resp.StatusCode,
        LatencyMs:    int(time.Since(startedAt).Milliseconds()),
        StartedAt:    startedAt,
        CompletedAt:  time.Now(),
        // ✅ 填充请求/响应内容
        RequestURL:   target.BaseURL,
        RequestBody:  string(reqBodyStr),    // ← 关键
        ResponseBody: string(respBody),       // ← 关键
        RespPreview:  truncate(string(respBody), 500),
        ViaProxy:     detectProxy(),
        Target:       *target,
    }
}
```

#### 3.2 在 emitter 中传递到 telemetry

```go
// bg/active_probe_emitter.go Emit 方法
func (e *ActiveProbeEmitter) Emit(..., result *ProbeResult) {
    // 现有逻辑...

    // ✅ 将 request/response body 传递给 telemetry
    var requestBody, responseBody json.RawMessage
    if result.RequestBody != "" {
        requestBody = json.RawMessage(result.RequestBody)
    }
    if result.ResponseBody != "" {
        responseBody = json.RawMessage(result.ResponseBody)
    }

    entry := &telemetry.RequestLogEntry{
        // ... 现有字段
        RequestBody:  requestBody,   // ← 新增
        ResponseBody: responseBody,  // ← 新增
        PromptTokens:     &promptTokens,
        CompletionTokens: &completionTokens,
    }

    // ✅ 改为先 INSERT 占位，再 UPDATE 完整数据
    e.telemetry.EmitRequestLogInsert(entry)

    // 如果需要模拟"请求过程"，可以短暂延迟后再 UPDATE
    // 但对于探测请求，通常是同步的，可以直接填充完整
}
```

#### 3.3 确保 INSERT 包含完整字段

```go
// domains/hooks/observability/telemetry/client.go insertRequestLog
// 当前 INSERT 已经包含 request_body / response_body 列（见 line 775）
// 只需确保 RequestLogEntry 中这些字段有值

_, err = tx.Exec(ctx, `
    INSERT INTO request_logs_hot (
        ...,
        request_body, response_body,  -- ← 已存在
        ...
    ) VALUES (
        ...,
        $41::text::jsonb, $42::text::jsonb,  -- ← 已存在
        ...
    )
`,
    ...,
    entry.RequestBody,   // ← 确保有值
    entry.ResponseBody,  // ← 确保有值
    ...,
)
```

## 实施步骤

### Phase 1: 修复 probe-direct 缺失数据（优先级 P0）

1. ✅ 修改 `bg/active_probe_executor.go`：填充 RequestBody/ResponseBody
2. ✅ 修改 `bg/active_probe_emitter.go`：传递到 telemetry.RequestLogEntry
3. ✅ 验证：查询一条新的 probe-direct 请求，确认有 request_body/response_body

### Phase 2: 修复请求刷新延迟（优先级 P0）

1. ✅ 在 handler 入口处立即 INSERT in_progress 行
2. ✅ 在 defer 中 UPDATE 完整数据
3. ✅ 修改 EmitRequestLog：INSERT 绕过批处理队列
4. ✅ 验证：前端发起请求后 <1s 内看到 in_progress 状态

### Phase 3: 修复前端泳道跳变（优先级 P1）

1. ✅ 前端改为增量更新策略
2. ✅ 统一数据源查询（只查 request_logs_hot）
3. ✅ 验证：泳道图不再出现大幅跳变

## 测试验证

### 验证1：probe-direct 完整性
```sql
SELECT
    request_id,
    length(request_body::text) as req_len,
    length(response_body::text) as resp_len,
    prompt_tokens,
    completion_tokens
FROM request_logs_hot
WHERE request_id LIKE 'probe-direct-%'
ORDER BY ts DESC
LIMIT 10;

-- 期望：req_len > 0, resp_len > 0, tokens > 0
```

### 验证2：请求刷新实时性
```bash
# 终端1：watch 数据库
watch -n 0.5 "psql -h $DB_HOST -U $DB_USER -d llm_gateway -c \"SELECT request_id, request_status, ts FROM request_logs_hot WHERE ts > NOW() - INTERVAL '10 seconds' ORDER BY ts DESC LIMIT 5;\""

# 终端2：发起请求
curl -X POST http://localhost:8080/v1/chat/completions ...

# 期望：<1s 内看到 in_progress 行，请求结束后立即变为 success/failure
```

### 验证3：前端泳道稳定性
```
1. 打开前端实时请求流页面
2. 观察 5 分钟
3. 期望：数据平滑增长，无大幅跳变
```

## 性能影响评估

### 写入量增加
- **before**: 每次请求 1 次 INSERT（响应结束后）
- **after**: 每次请求 1 次 INSERT（立即） + 1 次 UPDATE（响应结束后）
- **影响**: 写入次数翻倍，但 INSERT 极轻（只写基础字段），UPDATE 才写完整 JSONB

### 数据库负载
- request_logs_hot 是 heap 表，支持高频 UPDATE
- 每秒 1000 QPS → 2000 次写操作（在 PG 承受范围内）
- 如果负载过高，可以考虑：
  - INSERT 写入 Redis，异步 flush 到 PG
  - 使用 pgpool 连接池
  - 增加 work_mem / shared_buffers

### 前端 SSE 流量
- onEmitted hook 会触发两次（INSERT + UPDATE）
- 前端需要按 request_id 去重合并
- 流量增加不明显（UPDATE 时才有完整数据）

## 回滚方案

如果修复后出现问题：

```go
// 回滚：恢复原有逻辑（只在响应结束后写一次）
func (h *Handler) handleRequest(w http.ResponseWriter, r *http.Request) {
    // 移除立即 INSERT 逻辑
    // 只保留 defer 中的 EmitRequestLogInsert
}
```

## 相关文档

- `docs/design/request-trace-system.md` — 链路追踪设计
- `docs/partition/partition-standards.md` — 分区表查询规范
- `bg/active_probe_executor.go` — 探测执行器
- `bg/active_probe_emitter.go` — 探测记录器
- `domains/hooks/observability/telemetry/client.go` — telemetry 客户端
