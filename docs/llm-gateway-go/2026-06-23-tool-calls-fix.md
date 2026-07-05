# Tool Calls Data Loss Fix - 2026-06-23

## 问题描述

在IR层重构后（commits `05bee9f9`, `6e065a3e`），streaming响应中的tool_calls数据丢失。

### 症状
- 请求ID `a99f680163ec7bed8504a729810fc1cd` 显示工具调用交互成功
- 但Admin UI `/api/logs` 端点返回的`tool_calls`字段为空
- 非流式响应的tool_calls正常（存储在`response_body.choices[0].message.tool_calls`）
- 流式响应的tool_calls完全丢失

### 根因分析

1. **数据库层面**：`request_logs`表缺少`tool_calls` JSONB列
2. **审计层面**：`audit.StreamCapture`只将tool_calls写入`textContent`（纯文本），没有结构化存储
3. **持久化层面**：`telemetry.RequestLogEntry`没有`ToolCalls`字段
4. **SQL层面**：INSERT/UPDATE语句没有包含`tool_calls`列

## 修复方案

### 1. 数据库迁移 (Migration 042)

**文件**: `deploy/sql/migrations/042_tool_calls_column.sql`

```sql
ALTER TABLE request_logs 
  ADD COLUMN IF NOT EXISTS tool_calls JSONB;

CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls
  ON request_logs USING GIN (tool_calls)
  WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;

CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
  ON request_logs (provider_id, ts DESC)
  WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;
```

**Schema**: OpenAI Chat Completions格式
```json
[
  {
    "id": "call_abc123",
    "type": "function",
    "function": {
      "name": "get_weather",
      "arguments": "{\"location\":\"San Francisco\"}"
    }
  }
]
```

### 2. 审计层修改

**文件**: `audit/audit.go`, `audit/stream.go`

#### 2.1 StreamCapture结构添加字段
```go
type StreamCapture struct {
    // ... existing fields ...
    
    // ToolCalls accumulates structured tool calls from the stream.
    // Each entry has shape: {id, type, function: {name, arguments}}.
    ToolCalls []map[string]any
}
```

#### 2.2 ObserveChunk方法增强
```go
func (sc *StreamCapture) ObserveChunk(chunk *ir.StreamChunk) {
    // ... existing code ...
    
    // Capture tool calls (both textContent for preview AND structured ToolCalls)
    for _, tc := range chunk.Delta.ToolCalls {
        // Legacy text preview (backward compatibility)
        sc.appendText("\n[Tool Call: " + tc.Name + "]\n")
        sc.appendText(tc.Arguments)
        
        // NEW: Structured tool_calls accumulation
        sc.mergeToolCall(tc)
    }
}
```

#### 2.3 mergeToolCall方法
处理OpenAI流式协议的增量更新：
- 第一个chunk：`{index: 0, id: "call_abc", type: "function", function: {name: "foo", arguments: ""}}`
- 后续delta：`{index: 0, function: {arguments: "{\"bar\""}}`
- 累积arguments：`{\"bar\":123}`

#### 2.4 SummaryAsMap方法
```go
func (sc *StreamCapture) SummaryAsMap() map[string]any {
    // ... existing code ...
    
    // 2026-06-23: structured tool_calls from streaming
    if len(sc.ToolCalls) > 0 {
        m["tool_calls"] = sc.ToolCalls
    }
    return m
}
```

### 3. Telemetry层修改

**文件**: `telemetry/client.go`

#### 3.1 RequestLogEntry添加字段
```go
type RequestLogEntry struct {
    // ... existing fields ...
    
    // 2026-06-23: structured tool_calls array
    ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
}
```

#### 3.2 INSERT语句修改
```sql
INSERT INTO request_logs (
    -- ... existing columns ...
    upstream_finish_reason,
    tool_calls  -- NEW
) VALUES (
    -- ... existing params ...
    $65,  -- upstream_finish_reason
    CAST($66 AS jsonb)  -- tool_calls
)
```

#### 3.3 UPDATE语句修改
```sql
UPDATE request_logs rl
   SET -- ... existing columns ...
       upstream_finish_reason = COALESCE($62, rl.upstream_finish_reason),
       tool_calls = COALESCE(CAST($63 AS jsonb), rl.tool_calls)  -- NEW
```

### 4. Relay层修改

**文件**: `relay/handler.go`

```go
func (h *ChatHandler) emitTelemetry(...) {
    // ... existing code ...
    
    if capture != nil {
        m := capture.SummaryAsMap()
        
        // ... existing quality_flags extraction ...
        
        // 2026-06-23: structured tool_calls from streaming
        if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
            if b, err := json.Marshal(v); err == nil {
                reqLog.ToolCalls = b
            }
        }
    }
}
```

### 5. 数据库启动时迁移

**文件**: `db/db.go`

```go
func (d *DB) ensureRequestLogSchema(ctx context.Context) error {
    _, err := d.pool.Exec(ctx, `
        -- ... existing ALTER TABLE ...
        
        -- 2026-06-23: structured tool_calls
        ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS tool_calls JSONB;
        CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls
            ON request_logs USING GIN (tool_calls)
            WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;
        CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
            ON request_logs (provider_id, ts DESC)
            WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;
    `)
    return err
}
```

## 测试覆盖

### 单元测试 (audit/stream_tool_calls_test.go)

1. **TestObserveChunk_ToolCalls_SingleChunk**: 单个完整tool call
2. **TestObserveChunk_ToolCalls_IncrementalArguments**: 增量arguments累积
3. **TestObserveChunk_ToolCalls_MultipleTools**: 多个tool calls
4. **TestObserveChunk_ToolCalls_EmptyResponse**: 无tool calls的响应
5. **TestObserveChunk_ToolCalls_JSONMarshaling**: JSON序列化测试

### 集成测试 (relay/anthropic_to_openai_stream_tool_calls_test.go)

**TestStreamAnthropicSSEToOpenAI_ToolCalls_Complete**:
- 模拟真实Anthropic SSE响应（包含tool_use）
- 验证转换为OpenAI格式的tool_calls
- 验证audit.StreamCapture正确捕获
- 验证SummaryAsMap返回结构化数据

## 验证方法

### 1. 单元测试
```bash
go test ./audit -run TestObserveChunk_ToolCalls -v
# PASS: 5/5 tests
```

### 2. 集成测试
```bash
go test ./relay -run TestStreamAnthropicSSEToOpenAI_ToolCalls_Complete -v
# PASS: tool_calls correctly captured and formatted
```

### 3. 数据库验证
```sql
-- 部署后查询
SELECT 
    request_id,
    client_model,
    jsonb_pretty(tool_calls) as tool_calls,
    upstream_finish_reason
FROM request_logs
WHERE tool_calls IS NOT NULL
    AND jsonb_array_length(tool_calls) > 0
ORDER BY ts DESC
LIMIT 10;
```

### 4. 复现原始问题
```bash
# 使用原始trace_id重新发起请求
curl -X POST https://__DOMAIN_2__/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [...],
    "tools": [...],
    "stream": true
  }'

# 查询request_logs验证tool_calls字段有数据
```

## 部署步骤

### 1. 部署到184 (k3s)
```bash
cd __LOCAL_PATH_1__

# 运行测试
go test ./audit ./relay -v

# 构建镜像
./scripts/deploy-llm-gateway-go-184.sh

# 验证迁移
kubectl exec -it deploy/kx-llm-gateway-go -n pms-test -- sh
psql -U stockuser -d llm_gateway -c "\d request_logs" | grep tool_calls
```

### 2. 部署到71 (host docker)
```bash
./scripts/deploy-llm-gateway-go-71.sh
```

### 3. 验证修复
```bash
# 发起包含tool calls的streaming请求
# 查询数据库确认tool_calls字段有数据
```

## 影响范围

### 受益的场景
1. **Streaming响应**：所有包含tool_calls的streaming请求现在都会正确持久化
2. **Admin UI**：`/api/logs`端点现在返回完整的tool_calls数据
3. **审计分析**：可以通过SQL查询分析tool call使用情况
4. **Provider质量**：可以统计哪些provider的tool call质量更好

### 兼容性
- ✅ **向后兼容**：旧数据的`tool_calls`列为NULL，不影响查询
- ✅ **非流式响应**：不受影响（仍存储在`response_body`中）
- ✅ **现有索引**：不冲突，新增GIN索引用于tool_calls查询

## 相关Commits

- Migration 042: `deploy/sql/migrations/042_tool_calls_column.sql`
- Audit layer: `audit/audit.go`, `audit/stream.go`
- Telemetry layer: `telemetry/client.go`
- Relay layer: `relay/handler.go`
- DB bootstrap: `db/db.go`
- Tests: `audit/stream_tool_calls_test.go`, `relay/anthropic_to_openai_stream_tool_calls_test.go`

## 后续优化

1. **非流式响应提取**：当前非流式响应的tool_calls仍在`response_body`中，可以考虑也提取到`tool_calls`列
2. **Admin UI增强**：在UI中显示tool_calls的详细信息
3. **Analytics Dashboard**：添加tool call使用率、成功率等指标
4. **Provider比较**：对比不同provider的tool call质量

## 参考资料

- OpenAI Chat Completions API: https://platform.openai.com/docs/api-reference/chat/object
- Anthropic Messages API: https://docs.anthropic.com/claude/reference/messages_post
- IR Layer Design: `internal/ir/types.go`, `internal/ir/stream.go`
