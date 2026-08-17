# Claude Tool Calling 问题分析报告

## 执行摘要

通过代码审查，我发现网关的 Anthropic ↔ OpenAI 格式转换逻辑**在代码层面是正确的**。Tool use ID 被正确保留和传递。但基于你的反馈，问题依然存在，这意味着问题可能在以下几个方面：

## 代码审查发现（已确认正确）

### ✅ Tool Use ID 传递链路
```
Anthropic response → OpenAI format → Client → OpenAI format → Anthropic request
   tool_use.id    →   tool_call.id  →  ...  → tool_call_id  → tool_use_id
```

**文件**: `domains/streaming/anthropic_bridge.go`

1. **Anthropic → OpenAI** (line 845-852):
```go
toolCalls = append(toolCalls, map[string]any{
    "id":   c.ID,  // ✅ 保留原始 ID
    "type": "function",
    "function": map[string]any{
        "name":      c.Name,
        "arguments": string(argsJSON),
    },
})
```

2. **OpenAI → Anthropic** (line 996-1010):
```go
if role == "tool" {
    if toolCallID, ok := msg["tool_call_id"].(string); ok {
        toolContent, _ := msg["content"].(string)
        out = map[string]any{
            "role": "user",  // ✅ 转为 user role
            "content": []any{
                map[string]any{
                    "type":        "tool_result",
                    "tool_use_id": toolCallID,  // ✅ ID 正确映射
                    "content":     toolContent,
                },
            },
        }
    }
    return out
}
```

3. **流式传输 Tool Calls** (line 636-651):
```go
func buildAnthropicBridgeToolCallChunk(index int, id, name string, args *string, hasArgs bool) *ir.StreamChunk {
    tc := ir.StreamToolCallDelta{
        Index: index,
        ID:    id,  // ✅ ID 被保留，不是重新生成
        Type:  "function",
        Name:  name,
    }
    // ...
}
```

## 潜在问题分析

既然代码逻辑正确，问题可能出在：

### 假设 A: Tool Result Content 格式问题 🔴 HIGH

**问题**: Anthropic API 对 `tool_result` 的 `content` 字段有严格要求。

**Anthropic 官方要求**:
```json
{
  "type": "tool_result",
  "tool_use_id": "xxx",
  "content": "string"  // 必须是 string 或 array of content blocks
}
```

**当前网关实现** (line 998):
```go
toolContent, _ := msg["content"].(string)  // 假设 content 是 string
```

**潜在错误场景**:
```json
// ❌ 客户端可能发送 object
{
  "role": "tool",
  "tool_call_id": "xxx",
  "content": {"result": "data"}  // object 会导致类型断言失败
}

// ❌ 客户端可能发送 array
{
  "role": "tool",
  "tool_call_id": "xxx",
  "content": [{"type": "text", "text": "data"}]  // array 会导致类型断言失败
}
```

如果类型断言失败，`toolContent` 会是空字符串，导致请求无效。

### 假设 B: 消息历史未正确传递 🟡 MEDIUM

**问题**: 第二轮请求的 `messages` 数组可能不完整。

Anthropic 要求完整的对话历史：
```json
{
  "messages": [
    {"role": "user", "content": "question"},
    {"role": "assistant", "content": [{"type": "tool_use", ...}]},
    {"role": "user", "content": [{"type": "tool_result", ...}]}
  ]
}
```

如果网关没有正确保存 assistant 的 tool_use 消息，第二轮请求会失败。

### 假设 C: 错误没有被正确捕获和返回 🟡 MEDIUM

**问题**: Anthropic API 返回的错误可能被网关吞掉或转换不当。

需要检查：
- 错误响应是否正确解析
- 4xx/5xx 状态码是否被捕获
- 错误信息是否完整返回给客户端

### 假设 D: 流式响应中 Tool Use 信息不完整 🟢 LOW

**问题**: 流式传输时，tool_use 的 input 参数可能被分片，导致客户端重组失败。

需要检查：
- Tool call 的 arguments 是否完整
- 流式传输的 chunk 边界是否正确
- 客户端是否正确重组 tool_calls

## 诊断步骤

### 第1步: 启用详细日志

修改网关日志级别，捕获完整的请求/响应：

```go
// 在 anthropic_bridge.go 的转换函数中添加详细日志
slog.Info("converting_tool_message",
    "original_role", role,
    "tool_call_id", toolCallID,
    "content_type", fmt.Sprintf("%T", msg["content"]),
    "content_preview", fmt.Sprintf("%v", msg["content"]))
```

### 第2步: 实际测试用例

创建测试脚本 `test_claude_tool_calling.sh`:

```bash
#!/bin/bash
set -e

GATEWAY_URL="https://llm.kxpms.cn/v1/chat/completions"
API_KEY="your-gateway-key"

echo "=== Round 1: Request tool call ==="
RESP1=$(curl -s "$GATEWAY_URL" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
  "model": "claude-sonnet-4-5",
  "max_tokens": 1024,
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "description": "Get the current weather in a location",
      "parameters": {
        "type": "object",
        "properties": {
          "location": {
            "type": "string",
            "description": "City name"
          }
        },
        "required": ["location"]
      }
    }
  }],
  "messages": [{
    "role": "user",
    "content": "What is the weather in San Francisco?"
  }]
}')

echo "$RESP1" | jq .
echo ""

# Extract tool call ID
TOOL_CALL_ID=$(echo "$RESP1" | jq -r '.choices[0].message.tool_calls[0].id')
echo "Tool Call ID: $TOOL_CALL_ID"

if [ "$TOOL_CALL_ID" == "null" ] || [ -z "$TOOL_CALL_ID" ]; then
  echo "❌ ERROR: No tool call returned in round 1"
  exit 1
fi

echo ""
echo "=== Round 2: Provide tool result ==="
RESP2=$(curl -s "$GATEWAY_URL" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
  \"model\": \"claude-sonnet-4-5\",
  \"max_tokens\": 1024,
  \"tools\": [{
    \"type\": \"function\",
    \"function\": {
      \"name\": \"get_weather\",
      \"description\": \"Get the current weather in a location\",
      \"parameters\": {
        \"type\": \"object\",
        \"properties\": {
          \"location\": {
            \"type\": \"string\",
            \"description\": \"City name\"
          }
        },
        \"required\": [\"location\"]
      }
    }
  }],
  \"messages\": [
    {
      \"role\": \"user\",
      \"content\": \"What is the weather in San Francisco?\"
    },
    {
      \"role\": \"assistant\",
      \"content\": null,
      \"tool_calls\": $(echo "$RESP1" | jq -c '.choices[0].message.tool_calls')
    },
    {
      \"role\": \"tool\",
      \"tool_call_id\": \"$TOOL_CALL_ID\",
      \"content\": \"Sunny, 72°F\"
    }
  ]
}")

echo "$RESP2" | jq .

# Check for errors
ERROR=$(echo "$RESP2" | jq -r '.error.message // empty')
if [ -n "$ERROR" ]; then
  echo ""
  echo "❌ ERROR in Round 2: $ERROR"
  echo ""
  echo "Full error response:"
  echo "$RESP2" | jq '.error'
  exit 1
fi

# Check for valid response
CONTENT=$(echo "$RESP2" | jq -r '.choices[0].message.content // empty')
if [ -n "$CONTENT" ]; then
  echo ""
  echo "✅ SUCCESS: Got response in round 2"
  echo "Response: $CONTENT"
else
  echo ""
  echo "❌ ERROR: No content in round 2 response"
  exit 1
fi
```

### 第3步: 检查数据库日志

```sql
-- 查找失败的第二轮请求
SELECT 
    request_id,
    client_model,
    success,
    error_code,
    error_message,
    jsonb_array_length(request_body::jsonb->'messages') as msg_count,
    request_body::jsonb->'messages'->-1->>'role' as last_role,
    request_body::jsonb->'messages'->-1->>'content' as last_content
FROM request_logs
WHERE client_model LIKE 'claude%'
AND success = false
AND request_body::jsonb->'messages' @> '[{"role": "tool"}]'::jsonb
AND created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 10;
```

## 修复建议

### 修复 A: 改进 Tool Result Content 处理

**文件**: `domains/streaming/anthropic_bridge.go:996-1010`

```go
if role == "tool" {
    if toolCallID, ok := msg["tool_call_id"].(string); ok {
        // 改进：支持多种 content 类型
        var toolContent string
        switch content := msg["content"].(type) {
        case string:
            toolContent = content
        case map[string]any:
            // 如果是 object，序列化为 JSON 字符串
            if jsonBytes, err := json.Marshal(content); err == nil {
                toolContent = string(jsonBytes)
            }
        case []any:
            // 如果是 array，可能是 Anthropic content blocks
            // 直接传递
            out = map[string]any{
                "role": "user",
                "content": []any{
                    map[string]any{
                        "type":        "tool_result",
                        "tool_use_id": toolCallID,
                        "content":     content,  // 保持 array 格式
                    },
                },
            }
            return out
        default:
            slog.Warn("unexpected_tool_content_type",
                "type", fmt.Sprintf("%T", content),
                "tool_call_id", toolCallID)
            toolContent = fmt.Sprintf("%v", content)
        }
        
        out = map[string]any{
            "role": "user",
            "content": []any{
                map[string]any{
                    "type":        "tool_result",
                    "tool_use_id": toolCallID,
                    "content":     toolContent,
                },
            },
        }
    } else {
        slog.Warn("tool_message_missing_tool_call_id",
            "message", msg)
    }
    return out
}
```

### 修复 B: 添加详细错误日志

在转换失败时记录完整的输入：

```go
slog.Error("anthropic_bridge_conversion_failed",
    "input_role", role,
    "input_message", msg,
    "error", "conversion produced empty or invalid output")
```

## 立即行动项

1. **运行测试脚本** - 使用上面的 `test_claude_tool_calling.sh` 复现问题
2. **检查日志** - 找到失败的 request_id，查看完整错误
3. **应用修复 A** - 改进 content 类型处理
4. **验证修复** - 重新运行测试确认问题解决

## 参考文档

- **Anthropic Tool Use 文档**: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
- **网关代码**: `domains/streaming/anthropic_bridge.go`
- **之前创建的诊断指南**: `docs/diagnostics/claude-tool-call-conversation-stops.md`

---

**创建时间**: 2026-07-25  
**状态**: 需要实际测试验证  
**优先级**: P0
