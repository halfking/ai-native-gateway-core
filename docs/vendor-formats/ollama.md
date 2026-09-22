# Ollama 自托管方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式 + 私有 `/api/chat`）
- 方言: `DialectOllama`
- 官方文档: https://github.com/ollama/ollama/blob/main/docs/api.md
- 端点（原生）: `POST /api/chat`
- 端点（兼容）: `POST /v1/chat/completions`
- catalog code: `ollama`

## 兼容模式

Ollama 提供 OpenAI 兼容的 HTTP 服务（通过 `OLLAMA_ORIGINS` 配置）：

```
POST /v1/chat/completions HTTP/1.1
Host: localhost:11434
Authorization: Bearer {OLLAMA_API_KEY}  // 可选
Content-Type: application/json
```

兼容模式的字段与 OpenAI 一致，但行为略有差异（详见下节）。

## 原生模式 `/api/chat`

### 请求

```json
{
  "model": "llama3.1",
  "messages": [
    {"role": "user", "content": "Why is the sky blue?"}
  ],
  "stream": true,
  "format": "json",
  "options": {
    "temperature": 0.7,
    "top_p": 0.9,
    "top_k": 40,
    "num_predict": 1024,
    "stop": ["\n"],
    "seed": 42
  }
}
```

### 流式响应（NDJSON）

```
{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":""},"done":false}
{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":"The"},"done":false}
...
{"model":"llama3.1","created_at":"2024-...","message":{"role":"assistant","content":"...","done":true,"total_duration":...,"load_duration":...,"prompt_eval_count":10,"eval_count":20}
```

注意：**流式响应是 NDJSON 而非 SSE**，每行一个 JSON 对象。

### 非流式响应

```json
{
  "model": "llama3.1",
  "created_at": "2024-...",
  "message": {
    "role": "assistant",
    "content": "..."
  },
  "done": true,
  "total_duration": 1234567890,
  "load_duration": 100000000,
  "prompt_eval_count": 10,
  "eval_count": 20,
  "eval_duration": 500000000
}
```

## 与 OpenAI 的差异

### 1. `options` 子对象

原生模式下所有采样参数放在 `options` 内：

```json
{
  "options": {
    "temperature": 0.7,
    "top_p": 0.9,
    "top_k": 40,
    "num_predict": 1024,
    "stop": ["\n"],
    "seed": 42,
    "num_ctx": 4096,
    "num_gpu": 1,
    "mirostat": 0,
    "mirostat_eta": 0.1,
    "mirostat_tau": 5.0,
    "repeat_last_n": 64,
    "repeat_penalty": 1.1,
    "tfs_z": 1.0,
    "typical_p": 1.0
  }
}
```

兼容模式下，这些参数直接放在顶层。

### 2. `format` 字段

```json
{"format": "json"}  // 强制 JSON 输出
```

### 3. `keep_alive`

```json
{"keep_alive": "5m"}
```

模型在内存中保留的时间。

### 4. `num_predict`

OpenAI 的 `max_tokens` 在 Ollama 中是 `num_predict`。

### 5. `tools` 支持

Ollama 0.5+ 支持 OpenAI 风格的 tools：

```json
{
  "tools": [
    {
      "type": "function",
      "function": {...}
    }
  ]
}
```

需要模型支持（如 llama3.1、qwen2.5）。

### 6. 思考/推理内容

Ollama 思考模型（如 DeepSeek-R1-Distill）：

```json
{
  "message": {
    "role": "assistant",
    "content": "...",
    "thinking": "思考过程..."
  }
}
```

### 7. 错误码

```json
{"error": "model 'xxx' not found"}
```

Ollama 错误是简单字符串，不是嵌套对象。

### 8. 缓存

Ollama 不暴露 `cached_tokens`，通过 `prompt_eval_count` 体现。

## IR 映射

| Ollama 字段 | IR 字段 |
|-------------|---------|
| `options.temperature` | `Request.Temperature` |
| `options.top_p` | `Request.TopP` |
| `options.top_k` | `Request.TopK` |
| `options.num_predict` | `Request.MaxTokens` |
| `options.stop` | `Request.Stop` |
| `options.seed` | `Request.Seed` |
| `options.repeat_penalty` | `Request.RepetitionPenalty` |
| `format` | `Request.ResponseFormat` |
| `keep_alive` | `Request.KeepAlive` |
| `message.thinking` | `Response.ReasoningContent` |
| `total_duration` / `load_duration` / `eval_duration` | `Usage.Timing` |

## 序列化注意

1. **流式响应格式差异**：原生模式是 NDJSON（`\n` 分隔 JSON），与 OpenAI 的 SSE（`data:` 前缀）不同。网关解析层需要正确识别两种格式。
2. **`options` 子对象**：兼容模式下应平铺到顶层，原生模式下嵌套到 options。
3. **`num_predict` ↔ `max_tokens`**：兼容模式 OpenAI 字段是 `max_tokens`。
4. **错误码**：Ollama 错误是字符串，不是嵌套对象，需要规范化处理。
