# Anthropic Messages API 格式规范

## Base Protocol

- 名称: `anthropic-messages`
- 方言: `DialectAnthropic`
- 官方文档: https://docs.anthropic.com/en/api/messages

## HTTP 形态

```
POST /v1/messages HTTP/1.1
Host: api.anthropic.com
x-api-key: {ANTHROPIC_API_KEY}
anthropic-version: 2023-06-01
Content-Type: application/json

{...}
```

非流式响应为 `application/json`，流式响应为 `text/event-stream`。

### 必填 Header

| Header | 说明 |
|--------|------|
| `x-api-key` | API key |
| `anthropic-version` | API 版本，当前 `2023-06-01` |
| `content-type` | `application/json` |

可选 Header：
- `anthropic-beta`：逗号分隔的 beta 特性列表（如 `prompt-caching-2024-07-31`）

## 请求 Body（顶层字段）

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `model` | string | ✅ | 如 `claude-3-5-sonnet-20241022` |
| `messages` | array | ✅ | 对话消息 |
| `max_tokens` | integer | ✅ | **必填**！最大输出 token |
| `system` | string/array | ❌ | 系统提示，可为字符串或 ContentBlock 数组 |
| `temperature` | number | ❌ | 0-1，默认 1 |
| `top_p` | number | ❌ | 0-1 |
| `top_k` | integer | ❌ | 仅采样 top K |
| `stop_sequences` | array | ❌ | 自定义停止序列 |
| `stream` | boolean | ❌ | 是否流式 |
| `tools` | array | ❌ | 工具定义 |
| `tool_choice` | object | ❌ | `{"type":"auto"}`/`{"type":"any"}`/`{"type":"tool","name":"..."}` |
| `metadata` | object | ❌ | `{"user_id":"user_xxx"}` 用于风控 |
| `thinking` | object | ❌ | `{"type":"enabled","budget_tokens":5000}` 扩展思考 |
| `context_management` | object | ❌ | 上下文管理（beta） |

## Messages 结构

```json
[
  {"role": "user", "content": "Hello"},
  {"role": "assistant", "content": [
    {"type": "text", "text": "Hi!", "citations": null},
    {"type": "thinking", "thinking": "...", "signature": "abc..."},
    {"type": "tool_use", "id": "toolu_xxx", "name": "get_weather", "input": {...}}
  ]}
]
```

注意：
1. Anthropic 的 assistant message 是 ContentBlock 数组形式，**不是** string。
2. 工具结果作为**单独的 user message** 返回：

```json
{"role": "user", "content": [
  {"type": "tool_result", "tool_use_id": "toolu_xxx", "content": "weather result", "is_error": false}
]}
```

3. ContentBlock 类型：`text`、`image`（base64 或 URL）、`document`、`audio`、`thinking`、`tool_use`、`tool_result`、`redacted_thinking`。

## Tools 结构

```json
{
  "name": "get_weather",
  "description": "...",
  "input_schema": {
    "type": "object",
    "properties": {"location": {"type": "string"}},
    "required": ["location"]
  }
}
```

注意 Anthropic 用 `input_schema` 而非 OpenAI 的 `parameters`。

## Thinking 字段

```json
{
  "thinking": {
    "type": "enabled",
    "budget_tokens": 5000
  }
}
```

`type` 取值：
- `enabled`：启用扩展思考，**必填** `budget_tokens`
- `disabled`：禁用

约束：
- `budget_tokens` 必须 < `max_tokens`
- 模型必须是支持 thinking 的（如 `claude-3-7-sonnet`）

## 响应（非流式）

```json
{
  "id": "msg_xxx",
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "Hello!", "citations": null}
  ],
  "model": "claude-3-5-sonnet-20241022",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 20,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  }
}
```

`stop_reason` 取值：
- `end_turn`：自然结束
- `max_tokens`：达到 max_tokens
- `stop_sequence`：触发自定义停止序列
- `tool_use`：触发 tool 调用
- `refusal`：安全拒绝（content filter）

## 流式响应（SSE）

事件类型：

| Event | 说明 |
|-------|------|
| `message_start` | 消息开始，携带 `message` 初始壳（usage 空） |
| `content_block_start` | 内容块开始，含 `index` |
| `ping` | 心跳（可忽略） |
| `content_block_delta` | 内容块增量 |
| `content_block_stop` | 内容块结束 |
| `message_delta` | 消息级别增量（`stop_reason`、`usage` 变化） |
| `message_stop` | 消息结束 |

`content_block_delta.delta` 类型：
- `text_delta`：`{"type":"text_delta","text":"..."}`
- `input_json_delta`：`{"type":"input_json_delta","partial_json":"..."}`
- `thinking_delta`：`{"type":"thinking_delta","thinking":"..."}`
- `signature_delta`：`{"type":"signature_delta","signature":"..."}`

## Thinking block 流式

```
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}
```

`signature` 必须保留，否则后续对话该 thinking block 无法验证。

## 错误响应

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "..."
  }
}
```

`type` 取值：
- `invalid_request_error`（400）
- `authentication_error`（401）
- `permission_error`（403）
- `not_found_error`（404）
- `rate_limit_error`（429）
- `api_error`（500/529）
- `overloaded_error`（529）

## 特殊字段（IR 映射）

| Anthropic 字段 | IR 字段 | 备注 |
|----------------|---------|------|
| `system`（顶层） | `Request.System` | 独立于 messages |
| `messages[].content[].thinking` | `ContentBlock.Thinking` | 含 `signature` 必须原样保留 |
| `messages[].content[].input` | `ToolCall.Arguments` | 已 parsed 为 map |
| `messages[].content[].tool_use_id` | `ToolResult.ToolUseID` | user message 内 |
| `stop_reason` | `Response.StopReason` | 含 `tool_use` |
| `usage.cache_creation_input_tokens` | `Usage.CacheCreationTokens` | prompt cache 写 |
| `usage.cache_read_input_tokens` | `Usage.CacheReadTokens` | prompt cache 读 |
| `max_tokens`（必填） | `Request.MaxTokens` | 必须存在，否则 400 |
| `top_k` | `Request.TopK` | Anthropic 独有 |

## 与 OpenAI 的关键差异

1. **`max_tokens` 必填**：网关需要为不知道上限的请求自动填一个安全值（默认 4096 或从模型元数据取）。
2. **System 字段独立**：OpenAI 用 system message，Anthropic 用顶层 `system` 字段。IR 把它们统一存到 `Request.System`，但序列化时必须移到顶层。
3. **Tools 的 `input_schema` 而非 `parameters`**：转换时需重命名字段。
4. **Tool result 在 user message**：OpenAI 用 `role: "tool"`，Anthropic 用 `role: "user"` + `tool_result` block。IR 内部统一，序列化时需重写。
5. **Thinking block 必须带 signature**：IR 序列化时绝不能伪造或丢弃 signature，否则会导致客户端后续验证失败。
6. **stop_reason vs finish_reason**：命名不同，IR 必须统一。
7. **多模态 content 是 block 数组**：OpenAI 也支持数组，但 Anthropic 的 image/document block 有更多类型（PDF、citation 等）。
