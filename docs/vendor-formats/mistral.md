# Mistral AI 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectMistral`
- 官方文档: https://docs.mistral.ai/api/
- 端点: `POST /v1/chat/completions`
- catalog code: `mistral`

## 与 OpenAI 的差异

### 1. 推理内容

Mistral Magistral 系列支持 reasoning：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "...",
      "reasoning_content": "思考..."
    }
  }]
}
```

### 2. 函数调用

Mistral 支持 OpenAI 风格的 `tools`，但也有 Mistral 原生的 `tool_choice` 值：

```json
{"tool_choice": "any"}  // OpenAI 风格
{"tool_choice": {"type": "function", "function": {"name": "..."}}}
```

### 3. 安全模型（`safe_prompt`）

```json
{"safe_prompt": true}
```

在 Mistral 系统提示前注入一层安全约束。

### 4. 多模态

Mistral Pixtral 系列支持图片：

```json
{
  "content": [
    {"type": "text", "text": "..."},
    {"type": "image_url", "image_url": {"url": "..."}}
  ]
}
```

### 5. `prefix`（前缀续写）

```json
{"prefix": true}
```

启用 assistant 前缀续写模式：模型在 `prefix` 之后的内容基础上续写。常用于代码补全场景。

### 6. `parallel_tool_calls`

Mistral 默认为 `true`，与 OpenAI 行为略有差异。

### 7. 错误码

与 OpenAI 风格一致：

```json
{"error": {"message": "...", "type": "...", "code": "..."}}
```

### 8. 缓存

Mistral 不暴露 `cached_tokens`，计费通过 `usage.prompt_tokens`。

## IR 映射

| Mistral 字段 | IR 字段 |
|--------------|---------|
| `safe_prompt` | `Request.SafePrompt` |
| `prefix` | `Request.Prefix` |
| `reasoning_content` | `Response.ReasoningContent` |

## 序列化注意

1. `safe_prompt` 仅在 Mistral 方言时输出。
2. `prefix` 启用时，IR 需要识别该模式（assistant 前缀续写）。
3. `parallel_tool_calls` 默认行为不同，需要在 IR 中显式表达。
