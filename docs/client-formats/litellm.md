# LiteLLM 代理请求格式要求

## 概述

LiteLLM 是 Python 的统一 LLM 客户端库，把多个厂商 API 抽象为 OpenAI 兼容接口。

## 入站格式

LiteLLM 通过 OpenAI Chat Completions 协议与网关通信（`/v1/chat/completions`）。

```python
import litellm
response = litellm.completion(
    model="anthropic/claude-3-5-sonnet",
    messages=[{"role": "user", "content": "..."}],
    api_base="https://gateway.example.com/v1",
    api_key="any-string",
)
```

LiteLLM 期望返回符合 OpenAI Chat Completions 的响应。

## LiteLLM 关键依赖

| 字段 | LiteLLM 期望 | 用途 |
|------|--------------|------|
| `id` | 字符串 | 日志 |
| `object` | "chat.completion" 或 "text_completion" | 类型判断 |
| `choices[].message` | dict | 解析 assistant 内容 |
| `choices[].finish_reason` | "stop"\|"length"\|"tool_calls"\|... | 行为分支 |
| `usage` | dict | 计费 |
| `model` | 字符串 | 日志 |

## LiteLLM 的特殊请求模式

### 1. 模型名前缀

LiteLLM 使用 `<vendor>/<model>` 命名约定：

- `openai/gpt-4o`
- `anthropic/claude-3-5-sonnet-20241022`
- `gemini/gemini-1.5-pro`
- `bedrock/anthropic.claude-3-sonnet`
- `vertex_ai/gemini-1.5-pro`

网关需要解析前缀以正确路由。

### 2. tool calling

```python
tools = [{
    "type": "function",
    "function": {
        "name": "get_weather",
        "description": "...",
        "parameters": {...}
    }
}]
response = litellm.completion(
    model="openai/gpt-4o",
    messages=[...],
    tools=tools,
)
```

### 3. 流式

LiteLLM 流式默认开启 SSE：

```python
response = litellm.completion(
    model="anthropic/claude-3-5-sonnet",
    messages=[...],
    stream=True,
)
for chunk in response:
    print(chunk.choices[0].delta.content)
```

### 4. 多模态

LiteLLM 支持图片输入：

```python
response = litellm.completion(
    model="gpt-4o",
    messages=[{
        "role": "user",
        "content": [
            {"type": "text", "text": "What's in this image?"},
            {"type": "image_url", "image_url": {"url": "..."}}
        ]
    }]
)
```

### 5. 异步

LiteLLM 提供 `acompletion()` 和 `batch_completion()`。

## 网关对 LiteLLM 的特殊支持

### 1. 模型名前缀解析

```yaml
model_prefix_routing:
  openai/: openai
  anthropic/: anthropic
  gemini/: google-gemini
  bedrock/: aws-bedrock
  vertex_ai/: vertex-ai
  deepseek/: deepseek
  qwen/: qwen
  glm/: zhipu
  moonshot/: moonshot
  doubao/: volcengine
  xai/: xai
  mistral/: mistral
```

### 2. 协议转换

LiteLLM 总是发 OpenAI Chat Completions 协议。网关需要：

1. **ParseOpenAI**：解析 LiteLLM 请求。
2. **URSM v2 路由**：根据 `model` 前缀选择厂商。
3. **SerializeXxx**：按目标厂商协议序列化。
4. **ParseXxxResponse → SerializeOpenAIResponse**：反向转换。

### 3. Tool Schema 兼容性

LiteLLM 总是发 OpenAI 风格的 tools（`parameters`）。网关需要按目标厂商重命名：
- Anthropic → `input_schema`
- Gemini → `parameters`（嵌套在 functionDeclarations 内）

### 4. 错误响应

LiteLLM 期望 OpenAI 风格的错误：

```json
{"error": {"message": "...", "type": "invalid_request_error", "code": "..."}}
```

## 不兼容警告

- ❌ LiteLLM 的 fallback 机制：网关自己有 fallback，不需要 LiteLLM 这层
- ❌ LiteLLM 的 cost tracking：网关自己计费，需要忽略 LiteLLM 的 cost header
- ✅ OpenAI Chat 协议：完整支持
- ✅ 模型名前缀路由：完整支持
