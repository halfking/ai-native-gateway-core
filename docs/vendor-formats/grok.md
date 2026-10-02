# xAI Grok 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectGrok`
- 官方文档: https://docs.x.ai/docs
- 端点: `POST /v1/chat/completions`
- catalog code: `xai`、`grok`

## 与 OpenAI 的差异

### 1. 函数调用格式

与 OpenAI 完全一致。

### 2. 联网搜索

```json
{
  "search_parameters": {
    "mode": "auto",  // "auto" | "on" | "off"
    "sources": [{"type": "web"}, {"type": "x"}, {"type": "news"}],
    "return_citations": true,
    "from_date": "2025-01-01",
    "to_date": "2025-12-31"
  }
}
```

`search_parameters.mode`:
- `auto`：模型决定是否搜索
- `on`：强制搜索
- `off`：禁用搜索

`sources`:
- `web`：网页
- `x`：X（推特）
- `news`：新闻

启用搜索时，响应 `choices[].message` 中可能包含 `citations` 字段：

```json
{
  "message": {
    "role": "assistant",
    "content": "...",
    "citations": ["https://...", "https://..."]
  }
}
```

### 3. 推理内容

Grok 3 mini 等模型支持 `reasoning_content`。

### 4. 多模态

Grok Vision 支持图片输入：

```json
{
  "content": [
    {"type": "text", "text": "..."},
    {"type": "image_url", "image_url": {"url": "...", "detail": "high"}}
  ]
}
```

### 5. 用户标识

与 OpenAI `user` 字段一致。

### 6. 错误码

与 OpenAI 风格一致。

### 7. 缓存

Grok 4+ 支持 prompt caching：

```json
{
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "cached_tokens": 80
  }
}
```

## IR 映射

| Grok 字段 | IR 字段 |
|-----------|---------|
| `search_parameters` | `Request.SearchParameters` |
| `citations` | `Message.Citations` |
| `reasoning_content` | `Response.ReasoningContent` |
| `usage.cached_tokens` | `Usage.CachedTokens` |

## 序列化注意

1. `search_parameters` 是 Grok 私有字段，仅在 Grok 方言时输出。
2. `citations` 字段需要在解析时保留，序列化为 OpenAI/Anthropic 时按各自规则处理。
3. 多模态 content 与 OpenAI 兼容。
