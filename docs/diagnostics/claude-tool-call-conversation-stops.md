# Claude 工具调用后会话中断诊断指南

## 问题描述

**症状**: Claude 模型（如 claude-sonnet-4-5, claude-opus-4-5 等）通过网关请求时，第一次工具调用可以执行，但之后会话就中断了，无法继续进行多轮对话。

**对比**: 
- ✅ **直连 Anthropic API**: 可以持续执行，多轮工具调用正常
- ❌ **通过网关**: 一次会话后中断，无法继续

**影响范围**: 
- Anthropic Messages API (anthropic-messages 协议)
- 涉及模型: claude-sonnet-4-5, claude-opus-4-5, claude-haiku-4-5, claude-3-5-sonnet 等

## 根本原因假设

基于症状和代码分析，以下是最可能的根本原因（按可能性排序）：

### 假设 1: Tools/Tool Results 格式转换问题 🔴 HIGH

**理论**: 网关在处理 Anthropic Messages API 的 tool_use 和 tool_result 时，可能存在格式转换错误，导致第二轮请求格式不正确。

**Anthropic Messages API 特点**:
- 使用 `messages` 数组
- Tool use: `content` 中包含 `type: "tool_use"` 的 block
- Tool result: 需要 `type: "tool_result"` 的 block，包含 `tool_use_id`

**可能的错误点**:
1. **Tool result ID 不匹配**: `tool_use_id` 与原始 `tool_use` 的 `id` 不一致
2. **Content block 结构错误**: tool_result 的 content 格式不符合 Anthropic 规范
3. **Role 顺序错误**: Anthropic 要求严格的 user/assistant 交替

**需要检查的代码位置**:
- `domains/streaming/anthropic_bridge.go` - OpenAI ↔ Anthropic 格式转换
- `domains/streaming/tool_call_xml.go` - XML 格式工具调用解析
- `domains/streaming/responses_bridge.go` - 响应格式转换

### 假设 2: 流式响应中 Tool Calls 被截断或损坏 🟡 MEDIUM

**理论**: 在流式传输过程中，tool_use blocks 可能被不正确地处理、截断或重组。

**症状特征**:
- 第一次响应包含 tool_use
- 客户端收到的 tool_use 格式不完整
- 第二轮请求因格式错误被网关或 Anthropic 拒绝

**需要检查**:
- Stream chunking 逻辑
- Tool call 重组逻辑
- JSON 边界处理

### 假设 3: Strip/Sanitize 逻辑误删关键字段 🟡 MEDIUM

**理论**: 网关的字段清理逻辑可能误删了 tool_result 或 tool_use 相关的关键字段。

**需要检查**:
- `StripDoubaoFieldsBody` 等 strip 函数
- Anthropic-specific 字段处理
- `tool_use_id`, `tool_choice` 等字段是否被保留

### 假设 4: 会话状态/缓存问题 🟢 LOW

**理论**: 网关的会话状态管理或缓存机制导致第二轮请求使用了错误的上下文。

**需要检查**:
- Session sticky routing
- 缓存的 tool definitions
- Conversation history 管理

## 诊断步骤

### Phase 1: 捕获实际请求/响应

#### 1.1 直连 Anthropic API（作为基准）

```bash
# 第一轮 - 请求工具调用
curl https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
  "model": "claude-sonnet-4-5-20250929",
  "max_tokens": 1024,
  "tools": [{
    "name": "get_weather",
    "description": "Get weather",
    "input_schema": {
      "type": "object",
      "properties": {
        "location": {"type": "string"}
      },
      "required": ["location"]
    }
  }],
  "messages": [{
    "role": "user",
    "content": "What is the weather in SF?"
  }]
}' > direct_round1_response.json

# 检查响应中的 tool_use
cat direct_round1_response.json | jq '.content[] | select(.type == "tool_use")'

# 第二轮 - 提供工具结果
TOOL_USE_ID=$(cat direct_round1_response.json | jq -r '.content[] | select(.type == "tool_use") | .id')

curl https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d "{
  \"model\": \"claude-sonnet-4-5-20250929\",
  \"max_tokens\": 1024,
  \"tools\": [{
    \"name\": \"get_weather\",
    \"description\": \"Get weather\",
    \"input_schema\": {
      \"type\": \"object\",
      \"properties\": {
        \"location\": {\"type\": \"string\"}
      },
      \"required\": [\"location\"]
    }
  }],
  \"messages\": [
    {
      \"role\": \"user\",
      \"content\": \"What is the weather in SF?\"
    },
    {
      \"role\": \"assistant\",
      \"content\": $(cat direct_round1_response.json | jq -c '.content')
    },
    {
      \"role\": \"user\",
      \"content\": [{
        \"type\": \"tool_result\",
        \"tool_use_id\": \"$TOOL_USE_ID\",
        \"content\": \"Sunny, 72°F\"
      }]
    }
  ]
}" > direct_round2_response.json

echo "✅ Direct API - Round 2 should succeed"
```

#### 1.2 通过网关（复现问题）

```bash
# 第一轮 - 通过网关
curl https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $GATEWAY_API_KEY" \
  -H "content-type: application/json" \
  -d '{
  "model": "claude-sonnet-4-5",
  "max_tokens": 1024,
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "description": "Get weather",
      "parameters": {
        "type": "object",
        "properties": {
          "location": {"type": "string"}
        },
        "required": ["location"]
      }
    }
  }],
  "messages": [{
    "role": "user",
    "content": "What is the weather in SF?"
  }]
}' > gateway_round1_response.json

# 检查响应中的 tool_calls
cat gateway_round1_response.json | jq '.choices[0].message.tool_calls'

# 第二轮 - 提供工具结果
TOOL_CALL_ID=$(cat gateway_round1_response.json | jq -r '.choices[0].message.tool_calls[0].id')

curl https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $GATEWAY_API_KEY" \
  -H "content-type: application/json" \
  -d "{
  \"model\": \"claude-sonnet-4-5\",
  \"max_tokens\": 1024,
  \"tools\": [{
    \"type\": \"function\",
    \"function\": {
      \"name\": \"get_weather\",
      \"description\": \"Get weather\",
      \"parameters\": {
        \"type\": \"object\",
        \"properties\": {
          \"location\": {\"type\": \"string\"}
        },
        \"required\": [\"location\"]
      }
    }
  }],
  \"messages\": [
    {
      \"role\": \"user\",
      \"content\": \"What is the weather in SF?\"
    },
    {
      \"role\": \"assistant\",
      \"content\": null,
      \"tool_calls\": $(cat gateway_round1_response.json | jq -c '.choices[0].message.tool_calls')
    },
    {
      \"role\": \"tool\",
      \"tool_call_id\": \"$TOOL_CALL_ID\",
      \"content\": \"Sunny, 72°F\"
    }
  ]
}" > gateway_round2_response.json

echo "❌ Gateway - Round 2 likely fails or returns error"
```

### Phase 2: 检查日志

#### 2.1 网关请求日志

```sql
-- 查找最近的 Claude tool calling 请求
SELECT 
    request_id,
    client_model,
    provider_id,
    credential_id,
    success,
    error_code,
    error_message,
    latency_ms,
    created_at,
    jsonb_array_length(request_body::jsonb->'messages') as message_count,
    request_body::jsonb->'messages'->-1->'role' as last_role,
    request_body::jsonb->'messages'->-1->>'content' as last_content_preview
FROM request_logs
WHERE client_model LIKE 'claude%'
AND request_body::jsonb->'tools' IS NOT NULL
AND created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 20;

-- 检查是否有 tool_result 相关的错误
SELECT 
    request_id,
    error_code,
    error_message,
    request_body::jsonb->'messages' as messages
FROM request_logs
WHERE client_model LIKE 'claude%'
AND success = false
AND (
    error_message ILIKE '%tool%' 
    OR error_message ILIKE '%invalid%'
    OR error_message ILIKE '%format%'
)
AND created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC;
```

#### 2.2 网关应用日志

```bash
ssh 154
sudo journalctl -u llm-gateway --since '1 hour ago' --no-pager \
  | grep -i 'claude\|anthropic\|tool' \
  | grep -E 'error|warn|tool_use|tool_result' -i
```

### Phase 3: 代码审查关键路径

#### 3.1 Anthropic Bridge - OpenAI ↔ Anthropic 转换

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 检查 OpenAI messages 到 Anthropic messages 的转换
grep -A 50 "convertOpenAIToAnthropic\|toAnthropicMessages" domains/streaming/anthropic_bridge.go

# 检查 tool_result 的处理
grep -B 5 -A 10 "tool_result\|tool_use_id" domains/streaming/anthropic_bridge.go

# 检查是否正确处理 role="tool"
grep -B 5 -A 10 '"tool"' domains/streaming/anthropic_bridge.go
```

#### 3.2 Tool Call 格式检查

```bash
# 检查 tool_calls 的序列化/反序列化
grep -rn "tool_calls\|ToolCalls" domains/streaming/*.go | grep -v test

# 检查 tool_use block 的构建
grep -rn "tool_use\|tool_result" domains/streaming/anthropic*.go
```

## 已知问题模式

### Pattern A: tool_use_id 不匹配

**症状**: Anthropic 返回错误 `"Invalid tool_use_id"`

**原因**: 
- 网关生成的 tool_call_id 与 Anthropic 原始的 tool_use.id 不一致
- 格式转换时 ID 被重新生成

**解决方案**: 确保 ID 透传，不要重新生成

### Pattern B: Content Block 结构错误

**症状**: Anthropic 返回 `"Invalid request format"` 或 `400 Bad Request"`

**原因**:
```json
// ❌ 错误 - tool_result 的 content 必须是 array 或 string
{
  "type": "tool_result",
  "tool_use_id": "xxx",
  "content": {"result": "data"}  // 错误：不能是 object
}

// ✅ 正确
{
  "type": "tool_result",
  "tool_use_id": "xxx",
  "content": "data"  // 或 [{"type": "text", "text": "data"}]
}
```

### Pattern C: Role 顺序错误

**症状**: Anthropic 返回 `"messages: roles must alternate between user and assistant"`

**原因**:
```json
// ❌ 错误 - 连续两个 user 消息
[
  {"role": "user", "content": "question"},
  {"role": "assistant", "content": [{"type": "tool_use", ...}]},
  {"role": "tool", "content": "result"},  // 网关可能转换为 user
  {"role": "user", "content": "next question"}  // 连续 user
]
```

**解决方案**: 
- `role="tool"` 的消息应该合并到下一个 user 消息中
- 或者转换为 user 消息的 tool_result content block

## 代码位置速查

| 文件 | 功能 | 关键函数 |
|------|------|----------|
| `domains/streaming/anthropic_bridge.go` | OpenAI ↔ Anthropic 格式转换 | `convertOpenAIToAnthropic`, `convertAnthropicToOpenAI` |
| `domains/streaming/tool_call_xml.go` | XML 格式工具调用解析 | `parseToolCallXML` |
| `domains/streaming/responses_bridge.go` | 响应格式桥接 | - |
| `domains/streaming/executors/executor_anthropic.go` | Anthropic 协议执行器 | `Execute` |
| `domains/streaming/messages.go` | Messages API 处理 | - |
| `domains/streaming/handler.go` | 主请求处理器 | `ServeHTTP` |

## 修复验证清单

修复后需要验证以下场景：

- [ ] 单轮工具调用（tool_use → tool_result → final_response）
- [ ] 多轮工具调用（连续多次 tool_use）
- [ ] 并行工具调用（一次返回多个 tool_use）
- [ ] 工具调用失败处理（tool_result 包含 error）
- [ ] 流式响应中的工具调用
- [ ] 非流式响应中的工具调用
- [ ] Tool choice 参数（auto, required, specific tool）

## 临时缓解方案

如果无法立即修复，可以：

1. **建议用户直连 Anthropic API** - 绕过网关
2. **禁用 Claude 的工具调用路由** - 仅用于纯文本对话
3. **切换到 OpenAI 格式的 Claude 中继商**（如果支持）

## 参考文档

- [Anthropic Messages API - Tool Use](https://docs.anthropic.com/en/docs/build-with-claude/tool-use)
- [OpenAI Function Calling](https://platform.openai.com/docs/guides/function-calling)
- 网关 Anthropic 桥接代码: `domains/streaming/anthropic_bridge.go`

---

**创建时间**: 2026-07-25  
**状态**: 待验证  
**优先级**: P0 - 严重影响 Claude 工具调用功能
