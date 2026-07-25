# Claude Opus 5 工具调用中断问题 - 紧急诊断指南

## 问题描述

**模型**: claude-opus-5 (或相关变体如 claude-opus-4-5, claude-opus-4-8)  
**症状**: 
- ✅ 直连 Anthropic API: 工具调用可以持续执行，多轮对话正常
- ❌ 通过网关: 第一次工具调用后会话中断，无法继续

**影响**: 严重 - 阻止所有通过网关的 Claude Opus 工具调用场景

## 🚨 立即执行步骤（服务器恢复后）

### Step 1: 找到最近失败的请求

```bash
ssh llm-252
psql -U postgres -d llm_gateway
```

```sql
-- 查找最近2小时内的 opus 请求
SELECT 
    request_id,
    client_model,
    provider_id,
    success,
    error_code,
    error_message,
    latency_ms,
    created_at,
    jsonb_array_length(request_body::jsonb->'tools') as tools_count,
    jsonb_array_length(request_body::jsonb->'messages') as message_count,
    request_body::jsonb->'messages'->-1->>'role' as last_message_role
FROM request_logs
WHERE (
    client_model LIKE '%opus%' 
    OR client_model LIKE 'claude-opus%'
)
AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 20;
```

### Step 2: 检查第二轮请求（包含 tool role）

```sql
-- 专门查找包含 tool result 的请求
SELECT 
    request_id,
    client_model,
    success,
    error_code,
    error_message,
    latency_ms,
    created_at,
    -- 检查消息结构
    jsonb_array_length(request_body::jsonb->'messages') as msg_count,
    -- 提取最后一条消息
    request_body::jsonb->'messages'->-1->>'role' as last_role,
    request_body::jsonb->'messages'->-1->>'content' as last_content_preview,
    request_body::jsonb->'messages'->-1->>'tool_call_id' as tool_call_id
FROM request_logs
WHERE client_model LIKE '%opus%'
AND request_body::text LIKE '%"role":"tool"%'
AND created_at > NOW() - INTERVAL '4 hours'
ORDER BY created_at DESC
LIMIT 10;
```

### Step 3: 获取完整的失败请求详情

```sql
-- 选择一个 request_id 进行深入分析
\set req_id '替换为实际的request_id'

SELECT 
    request_id,
    client_model,
    provider_id,
    credential_id,
    success,
    error_code,
    error_message,
    latency_ms,
    jsonb_pretty(request_body::jsonb->'messages') as messages,
    jsonb_pretty(request_body::jsonb->'tools') as tools
FROM request_logs
WHERE request_id = :'req_id';

-- 如果有响应体，检查
SELECT 
    request_id,
    response_body
FROM stream_capture_audit
WHERE request_id = :'req_id';
```

### Step 4: 检查网关应用日志

```bash
ssh 154
sudo journalctl -u llm-gateway --since '2 hours ago' --no-pager > /tmp/gateway.log

# 搜索特定 request_id
grep 'REQUEST_ID' /tmp/gateway.log

# 搜索 Anthropic bridge 相关错误
grep -i 'anthropic' /tmp/gateway.log | grep -iE 'error|warn|fail'

# 搜索 tool 相关日志
grep -i 'tool' /tmp/gateway.log | grep -iE 'error|warn|converting|invalid'

# 搜索 content 类型错误
grep -i 'content.*type\|unexpected.*content' /tmp/gateway.log
```

## 🔍 关键诊断点

### 诊断点 1: Tool Result Content 类型

**代码位置**: `domains/streaming/anthropic_bridge.go:998`

**问题**: 当前代码假设 tool result 的 content 是 string：
```go
toolContent, _ := msg["content"].(string)
```

**检查方法**:
```sql
-- 查看实际发送的 tool result content 类型
SELECT 
    request_id,
    request_body::jsonb->'messages' -> -1 -> 'content' as tool_result_content,
    pg_typeof(request_body::jsonb->'messages' -> -1 -> 'content') as content_type
FROM request_logs
WHERE request_body::text LIKE '%"role":"tool"%'
AND created_at > NOW() - INTERVAL '4 hours'
LIMIT 5;
```

**预期问题**:
- 如果 content 是 JSON object: `{"result": "data"}` → 类型断言失败 → 空字符串
- 如果 content 是 array: `[{"type": "text", "text": "data"}]` → 类型断言失败 → 空字符串
- 空字符串发送给 Anthropic → 返回错误

### 诊断点 2: Tool Use ID 不匹配

**检查方法**:
```sql
-- 查找连续的请求对（第一轮工具调用 + 第二轮工具结果）
WITH tool_call_requests AS (
    SELECT 
        request_id,
        created_at,
        response_body::jsonb->'choices'->0->'message'->'tool_calls'->0->>'id' as tool_call_id
    FROM stream_capture_audit
    WHERE response_body::text LIKE '%tool_calls%'
    AND created_at > NOW() - INTERVAL '2 hours'
),
tool_result_requests AS (
    SELECT 
        request_id,
        created_at,
        request_body::jsonb->'messages'->-1->>'tool_call_id' as tool_call_id_sent
    FROM request_logs
    WHERE request_body::text LIKE '%"role":"tool"%'
    AND created_at > NOW() - INTERVAL '2 hours'
)
SELECT 
    tcr.request_id as first_request,
    tcr.tool_call_id,
    trr.request_id as second_request,
    trr.tool_call_id_sent,
    (tcr.tool_call_id = trr.tool_call_id_sent) as ids_match
FROM tool_call_requests tcr
LEFT JOIN tool_result_requests trr 
    ON trr.created_at > tcr.created_at 
    AND trr.created_at < tcr.created_at + INTERVAL '5 minutes'
ORDER BY tcr.created_at DESC
LIMIT 10;
```

### 诊断点 3: Anthropic API 错误响应

**检查方法**:
```sql
-- 查找所有 Anthropic 相关的错误
SELECT 
    request_id,
    error_code,
    error_message,
    request_body::jsonb->'messages'->-1 as last_message
FROM request_logs
WHERE client_model LIKE '%opus%'
AND success = false
AND created_at > NOW() - INTERVAL '4 hours'
ORDER BY created_at DESC;
```

**常见 Anthropic 错误**:
- `invalid_request_error`: 请求格式错误
- `Invalid tool_use_id`: ID 不匹配
- `messages: roles must alternate`: role 顺序错误
- `content must be a string or array`: content 类型错误

## 🔧 临时修复建议

### 修复 A: 改进 Content 类型处理（推荐）

**文件**: `domains/streaming/anthropic_bridge.go`

在 line 996-1010 处修改：

```go
if role == "tool" {
    if toolCallID, ok := msg["tool_call_id"].(string); ok {
        // 改进：支持多种 content 类型
        var contentForAnthropic any
        
        switch content := msg["content"].(type) {
        case string:
            // 最常见情况：直接使用字符串
            contentForAnthropic = content
            
        case map[string]any:
            // 如果是 object，序列化为 JSON 字符串
            if jsonBytes, err := json.Marshal(content); err == nil {
                contentForAnthropic = string(jsonBytes)
                slog.Info("converted_tool_result_object_to_string",
                    "tool_call_id", toolCallID,
                    "original_type", "object")
            } else {
                slog.Warn("failed_to_marshal_tool_result",
                    "tool_call_id", toolCallID,
                    "error", err)
                contentForAnthropic = fmt.Sprintf("%v", content)
            }
            
        case []any:
            // 如果是 array，可能已经是 Anthropic content blocks 格式
            // 检查是否是有效的 content blocks
            contentForAnthropic = content
            slog.Info("tool_result_content_is_array",
                "tool_call_id", toolCallID,
                "array_length", len(content))
                
        case nil:
            slog.Warn("tool_result_content_is_nil",
                "tool_call_id", toolCallID)
            contentForAnthropic = ""
            
        default:
            slog.Warn("unexpected_tool_result_content_type",
                "tool_call_id", toolCallID,
                "type", fmt.Sprintf("%T", content))
            contentForAnthropic = fmt.Sprintf("%v", content)
        }
        
        out = map[string]any{
            "role": "user",
            "content": []any{
                map[string]any{
                    "type":        "tool_result",
                    "tool_use_id": toolCallID,
                    "content":     contentForAnthropic,
                },
            },
        }
    } else {
        slog.Error("tool_message_missing_tool_call_id",
            "message_keys", func() []string {
                keys := make([]string, 0, len(msg))
                for k := range msg {
                    keys = append(keys, k)
                }
                return keys
            }())
    }
    return out
}
```

### 修复 B: 添加详细日志（调试用）

在同一文件中，在转换开始时添加：

```go
func convertBridgeChatMessagesToAnthropic(messages []any) ([]any, error) {
    // 添加这段调试日志
    slog.Debug("converting_messages_to_anthropic",
        "message_count", len(messages))
    
    out := make([]any, 0, len(messages))
    for i, m := range messages {
        msgMap, ok := m.(map[string]any)
        if !ok {
            continue
        }
        
        // 添加这段调试日志
        role, _ := msgMap["role"].(string)
        slog.Debug("converting_message",
            "index", i,
            "role", role,
            "has_tool_call_id", msgMap["tool_call_id"] != nil,
            "has_tool_calls", msgMap["tool_calls"] != nil,
            "content_type", fmt.Sprintf("%T", msgMap["content"]))
        
        // ... 继续原有逻辑
    }
}
```

## 📊 预期结果

修复后，你应该看到：

1. **日志中出现**:
```
level=INFO msg=converted_tool_result_object_to_string tool_call_id=toolu_xxx original_type=object
```

2. **SQL 查询显示**:
```
success = true
error_code = NULL
```

3. **第二轮请求成功返回内容**

## 🚀 快速验证脚本

保存为 `test_claude_opus_fix.sh`:

```bash
#!/bin/bash
GATEWAY="https://llm.kxpms.cn/v1/chat/completions"
API_KEY="your-api-key"

echo "Round 1: Tool call request"
R1=$(curl -s "$GATEWAY" -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" -d '{
  "model": "claude-opus-4-5",
  "max_tokens": 1024,
  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}}],
  "messages": [{"role": "user", "content": "Weather in SF?"}]
}')

echo "$R1" | jq .
TOOL_ID=$(echo "$R1" | jq -r '.choices[0].message.tool_calls[0].id')
echo "Tool ID: $TOOL_ID"

echo ""
echo "Round 2: Tool result"
R2=$(curl -s "$GATEWAY" -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" -d "{
  \"model\": \"claude-opus-4-5\",
  \"max_tokens\": 1024,
  \"tools\": [{\"type\": \"function\", \"function\": {\"name\": \"get_weather\", \"parameters\": {\"type\": \"object\", \"properties\": {\"location\": {\"type\": \"string\"}}, \"required\": [\"location\"]}}}],
  \"messages\": [
    {\"role\": \"user\", \"content\": \"Weather in SF?\"},
    {\"role\": \"assistant\", \"content\": null, \"tool_calls\": $(echo "$R1" | jq -c '.choices[0].message.tool_calls')},
    {\"role\": \"tool\", \"tool_call_id\": \"$TOOL_ID\", \"content\": \"Sunny, 72F\"}
  ]
}")

echo "$R2" | jq .
ERROR=$(echo "$R2" | jq -r '.error.message // empty')
[ -n "$ERROR" ] && echo "❌ ERROR: $ERROR" || echo "✅ SUCCESS"
```

## 📁 相关文件

- `domains/streaming/anthropic_bridge.go` - 主要转换逻辑
- `domains/streaming/executors/executor_anthropic.go` - Anthropic 执行器
- `scripts/check_claude_opus_logs.sh` - 日志检查脚本

---

**创建时间**: 2026-07-25  
**优先级**: P0 - 紧急  
**状态**: 等待服务器访问以验证根因
