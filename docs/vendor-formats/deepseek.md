# DeepSeek 方言规范

## 概述

- Base protocol: `openai-chat`（OpenAI 兼容模式）
- 方言: `DialectDeepSeek`
- 官方文档: https://api-docs.deepseek.com/
- 端点: `POST /chat/completions` 或 `/v1/chat/completions`

## 与 OpenAI 的差异

### 1. 推理内容（reasoning_content）

DeepSeek-R1 等模型在响应中额外提供 `reasoning_content` 字段，存放模型思考过程：

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "最终答案",
        "reasoning_content": "思考过程..."
      },
      "finish_reason": "stop"
    }
  ]
}
```

流式响应中，`reasoning_content` 通过 `delta.reasoning_content` 增量传输。

### 2. 多轮对话中的 reasoning 历史

将 DeepSeek 响应的 reasoning_content 回传到下一轮请求时，**必须**把 reasoning_content 放到 assistant message 之前：

```json
[
  {"role": "user", "content": "9.11 和 9.9 哪个大?"},
  {"role": "assistant", "reasoning_content": "思考...", "content": ""},
  {"role": "assistant", "content": "9.9 更大"},
  {"role": "user", "content": "怎么算的?"}
]
```

注：DeepSeek 的多轮示例中把 reasoning 与最终 content 拆成两个 assistant message。

### 3. 用户标识（user_id）

```json
{"user_id": "user-xxx"}
```

用于 DeepSeek 风控。

### 4. 函数调用格式

与 OpenAI 完全一致，使用 `tools` + `tool_choice` + `tool_calls`。

### 5. 错误码

```json
{
  "error": {
    "message": "...",
    "type": "invalid_request_error",
    "code": "..."  // DeepSeek 自定义
  }
}
```

DeepSeek 不使用 OpenAI 的 `param` 字段。

### 6. 缓存 token 计费

`usage.prompt_tokens_details.cached_tokens` 与 OpenAI 一致。

`usage.prompt_cache_hit_tokens` 和 `usage.prompt_cache_miss_tokens` 是 DeepSeek 扩展：

```json
{
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "prompt_cache_hit_tokens": 80,
    "prompt_cache_miss_tokens": 20
  }
}
```

## IR 映射

| DeepSeek 字段 | IR 字段 |
|---------------|---------|
| `delta.reasoning_content` (流式) | `StreamChunk.ReasoningDelta` |
| `message.reasoning_content` (非流式) | `InternalResponse.ReasoningContent` |
| `usage.prompt_cache_hit_tokens` | `Usage.CachedTokens` |
| `usage.completion_tokens` | `Usage.CompletionTokens` |
| `user_id` | `Request.UserID` |
| `temperature` 范围 0-2 | `Request.Temperature` |

## 序列化注意

1. 流式响应合成时，`reasoning_content` delta 必须映射为 IR 的 `ReasoningContent` 字段，并在序列化到 Anthropic/OpenAI Responses 时按目标协议规则处理。
2. `user_id` 与 OpenAI 的 `user` 字段语义相同，IR 内部统一存为 `UserID`。
3. 当 `reasoning_content` 为非空且 `content` 为空时，需要在多轮对话中拆成两个 assistant message（DeepSeek 官方推荐写法）。
