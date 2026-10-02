# OpenRouter 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectOpenRouter`
- 官方文档: https://openrouter.ai/docs
- 端点: `POST /api/v1/chat/completions`
- catalog code: `openrouter`

## 与 OpenAI 的差异

### 1. `route` 参数（核心：跨厂商路由）

```json
{
  "route": "fallback",
  "models": ["anthropic/claude-3.5-sonnet", "openai/gpt-4o"]
}
```

`route`:
- `fallback`：按顺序尝试，失败后切换
- `round-robin`：轮询多个模型
- `shuffle`：随机打乱
- `auto`：OpenRouter 自动选择

`models` 列表支持 OpenRouter 模型标识符（`<vendor>/<model>` 格式）。

### 2. `provider` 参数（指定后端）

```json
{"provider": {"order": ["Anthropic", "OpenAI"], "allow_fallbacks": true}}
```

显式指定后端供应商及其优先级。

### 3. `transforms` 参数

```json
{"transforms": ["middle-out"]}
```

启用 OpenRouter 的提示词压缩 transform（middle-out）。

### 4. `plugins` 参数

```json
{
  "plugins": [
    {"id": "web"},
    {"id": "file-parser", "pdf": {"engine": "mistral-ocr"}}
  ]
}
```

OpenRouter 插件扩展（web 搜索、文件解析等）。

### 5. 响应中的 `model` 字段

```json
{
  "model": "anthropic/claude-3.5-sonnet",  // 实际使用的模型
  "provider": "Anthropic"  // 实际后端
}
```

`response.model` 与请求时不同（因为 fallback）。

### 6. 推理内容

```json
{"reasoning": "...", "reasoning_content": "..."}
```

OpenRouter 同时支持两种字段名，需在解析时兼容。

### 7. 错误码

```json
{
  "error": {
    "code": 429,
    "message": "...",
    "metadata": {"provider_name": "..."}
  }
}
```

`metadata.provider_name` 标识实际失败的厂商。

### 8. 缓存与计费

OpenRouter 不直接暴露上游缓存字段，统一在响应中提供 `usage`：

```json
{
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150,
    "cost": 0.00123,  // 美元
    "cached_tokens": 80
  }
}
```

`cost` 是 OpenRouter 特有字段（聚合多厂商成本）。

## IR 映射

| OpenRouter 字段 | IR 字段 |
|-----------------|---------|
| `route` | `Request.Route` |
| `models` | `Request.Models` |
| `provider` | `Request.Provider` |
| `transforms` | `Request.Transforms` |
| `plugins` | `Request.Plugins` |
| `usage.cost` | `Usage.Cost` |
| `reasoning` / `reasoning_content` | `Response.ReasoningContent` |
| `provider_name` (error metadata) | `Error.ProviderName` |

## 序列化注意

1. `route` 与 `models` 是 OpenRouter 私有路由参数，仅在 OpenRouter 方言时输出。
2. `cost` 字段需要单独记录到 `IR.Usage.Cost`。
3. 多厂商 fallback 路径需要在解析层识别实际响应的 `model` 字段。
