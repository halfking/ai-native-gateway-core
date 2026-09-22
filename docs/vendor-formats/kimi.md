# 月之暗面 Kimi 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectKimi`
- 官方文档: https://platform.moonshot.cn/docs
- 端点: `POST /v1/chat/completions`
- catalog code: `moonshot`、`kimi`

## 与 OpenAI 的差异

### 1. `thinking.keep` 字段

Kimi 的 thinking 模型支持 keep 参数，决定多轮对话时是否保留思考内容：

```json
{
  "thinking": {
    "type": "enabled",
    "keep": "all"  // "all" | "last" | "none"
  }
}
```

- `all`：保留所有历史的 thinking
- `last`：仅保留最近一轮的 thinking
- `none`：不保留任何 thinking（默认）

**当前实现**：IR `Thinking` 块存在但**无 keep 字段**（P2 待扩展）。

### 2. `reasoning_effort` 参数

```json
{"reasoning_effort": "low" | "medium" | "high"}
```

Kimi K2/K3 模型支持，类似于 OpenAI o-series。

### 3. `tools` 的 `type` 字段

```json
{
  "type": "function",
  "function": {...}
}
```

与 OpenAI 完全一致。

### 4. 文件/图片输入

Kimi 支持文件上传与图片输入：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "总结这个 PDF"},
    {"type": "file", "file": {"file_id": "..."}}
  ]
}
```

`file_id` 由 `/v1/files` 接口返回。

### 5. 工具结果格式

```json
{
  "role": "tool",
  "tool_call_id": "...",
  "content": "..."
}
```

与 OpenAI 一致。

### 6. 缓存 token 计费

```json
{
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150,
    "cached_tokens": 80
  }
}
```

### 7. 错误码

```json
{"error": {"code": "...", "message": "..."}}
```

常见 code：`invalid_request_error`、`authentication_error`、`rate_limit_exceeded`、`insufficient_balance`。

## IR 映射

| Kimi 字段 | IR 字段 |
|-----------|---------|
| `thinking.keep` | `Request.ThinkingKeep` (P2) |
| `reasoning_effort` | `Request.ReasoningEffort` |
| `content[].file.file_id` | `ContentBlock.File` |
| `usage.cached_tokens` | `Usage.CachedTokens` |

## 序列化注意

1. `thinking.keep` 应在 IR 中表达为字符串字段，序列化时还原。
2. 文件输入需要客户端先调用文件上传接口拿到 file_id。
3. 多模态 content 数组按 IR 通用规则处理。
