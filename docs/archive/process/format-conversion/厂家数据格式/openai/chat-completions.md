# OpenAI Chat Completions API

## 获取说明

**注意**: OpenAI 文档受 Cloudflare 保护，无法通过自动化工具抓取。
本文档基于 OpenAI 公开的 API 规范整理，需要手动访问以下链接获取最新版本：

- **官方文档**: https://platform.openai.com/docs/api-reference/chat/create
- **建议**: 使用浏览器手动访问并导出完整规范

---

## Request: POST /v1/chat/completions

### 核心参数

#### messages (required)
- **Type**: `array`
- **Description**: 对话消息列表
- **Structure**:
  ```json
  {
    "role": "system" | "user" | "assistant" | "tool",
    "content": "string" | array,
    "name": "string (optional)",
    "tool_calls": array (optional),
    "tool_call_id": "string (optional for tool role)"
  }
  ```

#### Multimodal Content
```json
{
  "role": "user",
  "content": [
    {
      "type": "text",
      "text": "What's in this image?"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "https://...",
        "detail": "high" | "low" | "auto"
      }
    }
  ]
}
```

#### model (required)
- **Type**: `string`
- **Description**: 模型 ID (e.g., `gpt-4`, `gpt-3.5-turbo`)

#### temperature (optional)
- **Type**: `number`
- **Default**: `1`
- **Range**: `0-2`

#### max_tokens (optional)
- **Type**: `integer`
- **Description**: 最大生成 token 数

#### stream (optional)
- **Type**: `boolean`
- **Default**: `false`
- **Description**: 是否启用流式响应

#### tools (optional)
- **Type**: `array`
- **Description**: 可用工具列表
- **Structure**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "string",
      "description": "string",
      "parameters": {
        "type": "object",
        "properties": {...},
        "required": [...]
      }
    }
  }
  ```

#### tool_choice (optional)
- **Type**: `string | object`
- **Values**: `"none"`, `"auto"`, `"required"`, or specific tool
- **Example**:
  ```json
  {
    "type": "function",
    "function": {"name": "get_weather"}
  }
  ```

#### response_format (optional)
- **Type**: `object`
- **Structure**:
  ```json
  {
    "type": "text" | "json_object" | "json_schema",
    "json_schema": {...} (optional)
  }
  ```

#### top_p (optional)
- **Type**: `number`
- **Range**: `0-1`

#### n (optional)
- **Type**: `integer`
- **Description**: 生成多少个响应

#### stop (optional)
- **Type**: `string | array`
- **Description**: 停止序列

#### presence_penalty (optional)
- **Type**: `number`
- **Range**: `-2.0 to 2.0`

#### frequency_penalty (optional)
- **Type**: `number`
- **Range**: `-2.0 to 2.0`

#### logit_bias (optional)
- **Type**: `map`

#### user (optional)
- **Type**: `string`
- **Description**: 用户唯一标识符

---

## Response

### Non-Streaming Response

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 1234567890,
  "model": "gpt-4",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "string | null",
        "tool_calls": [
          {
            "id": "call_...",
            "type": "function",
            "function": {
              "name": "string",
              "arguments": "string (JSON)"
            }
          }
        ]
      },
      "finish_reason": "stop" | "length" | "tool_calls" | "content_filter" | "function_call",
      "logprobs": null | {...}
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  },
  "system_fingerprint": "string"
}
```

### Streaming Response (SSE)

```
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

### Tool Calls Response

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_abc123",
            "type": "function",
            "function": {
              "name": "get_weather",
              "arguments": "{\"location\":\"San Francisco\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ]
}
```

---

## 关键特性

1. **多模态支持**: `content` 可以是字符串或包含文本/图片的数组
2. **工具调用**: 通过 `tools` + `tool_choice` 控制函数调用
3. **流式响应**: `stream=true` 时返回 SSE 格式，每个 chunk 包含 `delta`
4. **响应格式控制**: `response_format` 可强制 JSON 输出
5. **Finish Reason**: `stop`, `length`, `tool_calls`, `content_filter`, `function_call`

---

## TODO

- [ ] 手动访问 OpenAI 官方文档获取最新完整规范
- [ ] 补充 logprobs 详细结构
- [ ] 补充 JSON Schema mode 示例
- [ ] 验证 Structured Outputs 最新特性
