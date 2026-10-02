# OpenAI Chat Completions API 格式规范

## Base Protocol

- 名称: `openai-chat`
- 方言: `DialectOpenAIChat`
- 官方文档: https://platform.openai.com/docs/api-reference/chat

## HTTP 形态

```
POST /v1/chat/completions HTTP/1.1
Host: api.openai.com
Authorization: Bearer {OPENAI_API_KEY}
Content-Type: application/json

{...}
```

非流式响应为 `application/json`，流式响应为 `text/event-stream`。

## 请求 Body（顶层字段）

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `model` | string | ✅ | 模型 ID，如 `gpt-4o`、`o1-mini` |
| `messages` | array | ✅ | 对话消息列表 |
| `temperature` | number | ❌ | 0-2，默认 1 |
| `top_p` | number | ❌ | 0-1 |
| `n` | integer | ❌ | 生成候选数 |
| `stream` | boolean | ❌ | 是否流式 |
| `stop` | string/array | ❌ | 停止序列 |
| `max_tokens` | integer | ❌ | 单条回复最大 token（旧模型） |
| `max_completion_tokens` | integer | ❌ | 单条回复最大 token（新模型，如 o-series） |
| `presence_penalty` | number | ❌ | -2 到 2 |
| `frequency_penalty` | number | ❌ | -2 到 2 |
| `logit_bias` | object | ❌ | token id → bias 映射 |
| `user` | string | ❌ | 终端用户标识，用于风控 |
| `tools` | array | ❌ | 可用工具列表 |
| `tool_choice` | string/object | ❌ | `none`/`auto`/`required`/具体工具 |
| `response_format` | object | ❌ | `{"type":"json_object"}` 等 |
| `seed` | integer | ❌ | 随机种子 |
| `logprobs` | boolean | ❌ | 是否返回 log probs |
| `top_logprobs` | integer | ❌ | 返回 top N log probs |
| `stream_options` | object | ❌ | `{"include_usage": true}` 让流式末尾返回 usage |
| `reasoning_effort` | string | ❌ | `low`/`medium`/`high`（o-series） |
| `parallel_tool_calls` | boolean | ❌ | 是否允许并发 tool 调用 |
| `service_tier` | string | ❌ | `auto`/`default` |

## Messages 结构

```json
[
  {"role": "system", "content": "You are a helpful assistant."},
  {"role": "user", "content": "Hello"},
  {"role": "assistant", "content": "Hi!", "tool_calls": [...]},
  {"role": "tool", "tool_call_id": "call_xxx", "content": "tool result"}
]
```

`content` 可为 string 或 array（多模态）：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "What's in this image?"},
    {"type": "image_url", "image_url": {"url": "https://...", "detail": "auto"}}
  ]
}
```

支持的 content type：`text`、`image_url`、`input_audio`、`file`。

## Tools 结构

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "...",
    "parameters": {
      "type": "object",
      "properties": {"location": {"type": "string"}},
      "required": ["location"]
    }
  }
}
```

`tool_choice` 形式：

```json
{"type": "function", "function": {"name": "get_weather"}}
```

## Assistant Message 中的 tool_calls

```json
{
  "role": "assistant",
  "tool_calls": [
    {
      "id": "call_xxx",
      "type": "function",
      "function": {
        "name": "get_weather",
        "arguments": "{\"location\":\"SF\"}"
      }
    }
  ]
}
```

`arguments` 是字符串形式的 JSON，需要网关做 JSON parse 后存到 IR `tool_calls.arguments`。

## 响应（非流式）

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4o-2024-08-06",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help?",
        "refusal": null,
        "tool_calls": null
      },
      "finish_reason": "stop",
      "logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30,
    "prompt_tokens_details": {"cached_tokens": 0, "audio_tokens": 0},
    "completion_tokens_details": {"reasoning_tokens": 5, "audio_tokens": 0}
  },
  "system_fingerprint": "fp_xxx"
}
```

`finish_reason` 取值：
- `stop`：自然结束
- `length`：达到 max_tokens
- `tool_calls`：触发 tool 调用
- `content_filter`：被内容过滤
- `function_call`（旧版）：已弃用

## 流式响应（SSE）

每个 chunk 形如：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

...

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}

data: [DONE]
```

字段说明：
- `delta.role`：仅首块出现
- `delta.content`：流式文本片段
- `delta.tool_calls`：流式 tool 调用片段
- `finish_reason`：末块出现
- `usage`：仅在 `stream_options.include_usage=true` 时出现

## 错误响应

```json
{
  "error": {
    "message": "Incorrect API key provided",
    "type": "invalid_request_error",
    "param": null,
    "code": "invalid_api_key"
  }
}
```

`type` 取值：
- `invalid_request_error`（400）
- `authentication_error`（401）
- `permission_error`（403）
- `not_found_error`（404）
- `rate_limit_error`（429）
- `api_error`（500/503）
- `overloaded_error`（529）

## 特殊字段（IR 映射）

| OpenAI 字段 | IR 字段 | 备注 |
|-------------|---------|------|
| `messages[].content` | `Message.Content`（string 或 ContentBlock 数组） | 多模态统一为 ContentBlock |
| `messages[].tool_calls[].function.arguments` | `ToolCall.Arguments`（parsed map） | 字符串 JSON 在 IR 内解析 |
| `response_format` | `Request.ResponseFormat` | 用于 JSON 模式 |
| `reasoning_effort` | `Request.ReasoningEffort` | o-series 专用 |
| `usage.completion_tokens_details.reasoning_tokens` | `Usage.ReasoningTokens` | 推理 token 计费 |
| `usage.prompt_tokens_details.cached_tokens` | `Usage.CachedTokens` | 缓存 token 计费 |
| `top_logprobs` + `logprobs` | `Choice.Logprobs` | 已映射但需客户端支持 |

## 与其他协议的差异（关键）

- **Reasoning 内容位置**：OpenAI 顶层 `reasoning_content`（某些代理）和 assistant message 内的 `reasoning_content` 字段共存；IR 必须正确分类到 `InternalResponse.ReasoningContent`。
- **tool_calls.arguments**：字符串形式，IR 内必须解析为 map，否则后续轮次回传会失败。
- **流式 finish_reason**：仅在末块出现一次，必须保证 IR 流式合成时只在该块归并。
- **o-series reasoning_effort**：o1/o3 等模型不接受 `max_tokens`，必须改用 `max_completion_tokens`。
