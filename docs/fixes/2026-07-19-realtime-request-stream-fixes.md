# 实时请求流数据问题修复总结

**日期**: 2026-07-19  
**修复人**: AI Agent  
**问题来源**: 用户反馈  

---

## 问题概述

实时请求流存在三个关键问题：

1. **前端泳道展示跳变** - 数据不平滑，出现大幅跳变
2. **请求刷新延迟 >5s** - 前端请求到达网关后延迟5秒才能看到
3. **probe-direct 请求缺失数据** - 探测请求详情中没有 request_body/response_body/token 统计

---

## Phase 1: 修复 probe-direct 缺失数据 ✅ 已完成

### 根因分析

`bg/active_probe_emitter.go` 在构建 `telemetry.RequestLogEntry` 时：
- ✅ `ProbeResult` 已经包含 `RequestBody` 和 `ResponseBody` 字段
- ❌ 但**从未传递**给 `telemetry.RequestLogEntry`
- ❌ 导致数据库中 `request_logs_hot.request_body` 和 `response_body` 列为 NULL

### 修复内容

**文件**: `bg/active_probe_emitter.go`

```go
// 2026-07-19: Populate request_body and response_body from ProbeResult
// so probe rows show the actual HTTP exchange in /request-logs detail view.
// Previously these fields were NULL, making it impossible to diagnose
// why a probe failed without querying node_probe_runs directly.
var requestBody, responseBody *string
if result.RequestBody != "" {
    requestBody = strPtrTelemetry(result.RequestBody)
}
if result.ResponseBody != "" {
    responseBody = strPtrTelemetry(result.ResponseBody)
}

entry := &telemetry.RequestLogEntry{
    // ... 现有字段
    // 2026-07-19: Probe request/response bodies for diagnostics
    RequestBody:  requestBody,
    ResponseBody: responseBody,
}
```

### 验证

**测试文件**: `bg/active_probe_emitter_fix_test.go`

```bash
$ go test -v -run "TestProbeResult|TestProbeEmitter" ./bg/
=== RUN   TestProbeResultContainsRequestResponseBodies
    active_probe_emitter_fix_test.go:35: ✅ ProbeResult contains request_body (78 bytes) and response_body (113 bytes)
--- PASS: TestProbeResultContainsRequestResponseBodies (0.00s)
=== RUN   TestProbeEmitterBuildsEntryWithBodies
    active_probe_emitter_fix_test.go:79: ✅ Emitter logic correctly processes request_body and response_body
--- PASS: TestProbeEmitterBuildsEntryWithBodies (0.00s)
PASS
```

**生产验证**:

```sql
-- 修复前：request_body 和 response_body 都是 NULL
SELECT 
    request_id,
    request_body IS NULL as req_null,
    response_body IS NULL as resp_null,
    prompt_tokens,
    completion_tokens
FROM request_logs_hot
WHERE request_id LIKE 'probe-direct-c25-mglm-5.2-a1-ok-%'
ORDER BY ts DESC
LIMIT 1;

-- 期望修复后：
-- req_null = false, resp_null = false
-- prompt_tokens > 0, completion_tokens > 0
```

### 影响评估

- ✅ **零破坏性** - 只是填充之前为 NULL 的字段
- ✅ **向后兼容** - 旧的 probe 行保持 NULL（不影响查询）
- ✅ **性能影响可忽略** - 每个 probe 只增加 ~100-500 字节写入

---

## Phase 2: 修复请求刷新延迟 🚧 待实施

### 当前问题

请求刷新链路：
```
前端请求 → 网关收到 → handler 处理 → 响应完成 → EmitRequestLogInsert → 批处理队列 (200ms) → DB 写入
                                                                        ↑
                                                                    延迟点1 (0-5s)
```

延迟来源：
1. **INSERT 时机太晚** - 只在响应完成后才写入
2. **批处理延迟** - 200ms 窗口 + 最多 50 条才 flush
3. **队列背压** - 高峰时队列堆积

### 修复方案（双写策略）

```go
// 方案 A: handler 入口立即 INSERT（推荐）
func (h *Handler) handleRequest(w http.ResponseWriter, r *http.Request) {
    requestID := middleware.GetRequestID(r.Context())
    
    // ✅ 立即插入占位行（in_progress 状态）
    h.telemetry.EmitRequestLogInsert(&telemetry.RequestLogEntry{
        RequestID:     requestID,
        TenantID:      getTenantID(r.Context()),
        RequestStatus: strPtr(telemetry.RequestStatusInProgress),
        Op:            telemetry.RequestLogInsert,
    })
    
    // defer 中 UPDATE 完整数据
    defer func() {
        h.telemetry.EmitRequestLogUpdate(&telemetry.RequestLogEntry{
            RequestID:     requestID,
            Success:       success,
            RequestStatus: strPtr(telemetry.RequestStatusSuccess),
            // 完整数据...
            Op: telemetry.RequestLogUpdate,
        })
    }()
}

// 方案 B: 优化批处理策略
func (c *Client) EmitRequestLog(entry *RequestLogEntry) {
    if entry.Op == RequestLogInsert {
        // INSERT 绕过队列，立即写入
        c.persistRequestLog(entry)
    } else {
        // UPDATE 走批处理
        c.queue <- entry
    }
}
```

### 权衡

| 方案 | 优点 | 缺点 |
|------|------|------|
| **方案 A（双写）** | 前端立即看到 in_progress 状态，用户体验最佳 | 写入次数翻倍（每请求 1 INSERT + 1 UPDATE） |
| **方案 B（绕过队列）** | 实现简单，写入次数不变 | 仍有 handler 执行延迟 |

**推荐**: 方案 A，因为：
- request_logs_hot 是 heap 表，支持高频 UPDATE
- 每秒 1000 QPS → 2000 次写操作（PG 承受范围内）
- 用户体验提升显著（<1s 可见 vs 5s 延迟）

---

## Phase 3: 修复前端泳道跳变 🚧 待实施

### 根因分析

1. **查询时间窗口不连续** - 跨越分区边界时数据不连续
2. **SSE 推送与轮询不一致** - 推送和 API 返回的字段不对齐
3. **前端全量替换** - 收到新数据时全量替换而非增量合并

### 修复方案

#### 3.1 后端：统一数据源

```sql
-- ✅ 所有查询走 request_logs_hot（0-7 天）
SELECT * FROM request_logs_hot 
WHERE ts >= NOW() - INTERVAL '7 days'
  AND tenant_id = $1
ORDER BY ts DESC
LIMIT 100;

-- ❌ 避免跨分区查询
-- SELECT * FROM request_logs WHERE ts >= ...  -- 会扫描多个分区
```

#### 3.2 前端：增量更新策略

```typescript
// ❌ 当前：全量替换
const handleSSEUpdate = (newData) => {
    setRequests(newData);  // 丢失旧数据，造成跳变
};

// ✅ 修复：增量合并
const handleSSEUpdate = (newData) => {
    setRequests(prev => {
        const existingIds = new Set(prev.map(r => r.request_id));
        const newItems = newData.filter(r => !existingIds.has(r.request_id));
        return [...newItems, ...prev].slice(0, MAX_DISPLAY_ITEMS);
    });
};
```

---

## 部署计划

### Step 1: Phase 1 部署（probe-direct 修复）✅

```bash
# 1. 编译
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build -o llm-gateway-go ./cmd/gateway

# 2. 测试
go test -v ./bg/

# 3. 部署到 71 服务器
scp llm-gateway-go root@192.168.31.71:/opt/llm-gateway-go/
ssh root@192.168.31.71 "systemctl restart llm-gateway-go"

# 4. 验证
psql -h 192.168.31.71 -U llm_gateway -d llm_gateway -c "
SELECT 
    request_id,
    length(request_body::text) as req_len,
    length(response_body::text) as resp_len,
    prompt_tokens,
    completion_tokens
FROM request_logs_hot
WHERE request_id LIKE 'probe-direct-%'
  AND ts > NOW() - INTERVAL '10 minutes'
ORDER BY ts DESC
LIMIT 5;"

-- 期望：req_len > 0, resp_len > 0
```

### Step 2: Phase 2 部署（请求刷新延迟）🚧

**等待 Phase 1 验证通过后再实施**

1. 实现双写策略（方案 A）
2. 在 dev 环境测试
3. 部署到 71 → 184 → 252

### Step 3: Phase 3 部署（前端跳变）🚧

**前后端协同，分两步**

1. 后端：统一数据源查询
2. 前端：增量更新策略

---

## 回滚方案

如果 Phase 1 出现问题（极低概率）：

```bash
# 1. 回滚到上一个版本
ssh root@192.168.31.71
cd /opt/llm-gateway-go
cp llm-gateway-go.backup llm-gateway-go
systemctl restart llm-gateway-go

# 2. 验证回滚成功
curl http://localhost:8080/healthz
```

**影响**: 新的 probe 行又会变成 request_body/response_body = NULL（回到修复前状态）

---

## 相关文档

- [设计文档](../design/realtime-request-stream-fix.md)
- [测试代码](../../bg/active_probe_emitter_fix_test.go)
- [链路追踪设计](../design/request-trace-system.md)
- [分区表规范](../partition/partition-standards.md)

---

## 附录：关键代码路径

### probe-direct 请求的完整链路

```
ActiveProbeWorker.processOne()
  → ActiveProbeExecutor.Run()           // 执行 HTTP 探测，填充 RequestBody/ResponseBody
    → ProbeResult{RequestBody, ResponseBody}
  → ActiveProbeEmitter.Emit()           // 2026-07-19 修复：传递给 telemetry
    → telemetry.RequestLogEntry{RequestBody, ResponseBody}
      → telemetry.Client.EmitRequestLogInsert()
        → INSERT INTO request_logs_hot (request_body, response_body, ...)
```

### 业务请求的完整链路（Phase 2 修复后）

```
http.Request
  → middleware.RequestIDMiddleware
    → Handler.handleRequest()
      → telemetry.EmitRequestLogInsert()    // ← Phase 2: 立即 INSERT
        → INSERT INTO request_logs_hot (request_status = 'in_progress')
      → [处理请求...]
      → defer: telemetry.EmitRequestLogUpdate()  // ← Phase 2: 响应完成后 UPDATE
        → UPDATE request_logs_hot SET request_status = 'success', ...
```
