# 火山引擎 Ark / 豆包方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectArk`
- 官方文档: https://www.volcengine.com/docs/82379
- 端点: `POST /api/v3/chat/completions`
- catalog code: `volcengine`、`ark`、`doubao`

## 与 OpenAI 的差异

### 1. 推理内容 `reasoning_content`

豆包 1.5 Thinking 模型支持：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "...",
      "reasoning_content": "思考过程..."
    }
  }]
}
```

### 2. function_call 格式

与 OpenAI 完全一致。

### 3. 工具类型扩展

火山方舟支持 `web_search`、`function`、`image_generation` 等多种 tool 类型：

```json
{
  "tools": [
    {"type": "web_search"},
    {
      "type": "function",
      "function": {...}
    }
  ]
}
```

### 4. `thinking` 参数

```json
{"thinking": {"type": "enabled"}}
```

豆包 1.5 Thinking 启用扩展思考。

### 5. 多模态

支持图片、视频、音频输入：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "描述"},
    {"type": "image_url", "image_url": {"url": "..."}}
  ]
}
```

### 6. 缓存

```json
{
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "cached_tokens": 80
  }
}
```

### 7. 错误码

```json
{
  "error": {
    "code": "InvalidParameter",
    "message": "...",
    "type": "BadRequest"  // HTTP 状态码文字
  }
}
```

## IR 映射

| Ark 字段 | IR 字段 |
|----------|---------|
| `reasoning_content` | `Response.ReasoningContent` |
| `thinking.type` | `Request.Thinking.Type` |
| `tools[].type: "web_search"` | `Tool.Type` |
| `usage.cached_tokens` | `Usage.CachedTokens` |

## 序列化注意

1. `web_search` 工具类型是 Ark 独有，需要在 IR 中支持。
2. 多模态 content 数组按 IR 通用规则处理。
3. `reasoning_content` 在多轮对话中可能需要保留。
