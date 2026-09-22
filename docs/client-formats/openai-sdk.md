# OpenAI 官方 SDK 请求格式要求

## 适用 SDK

- Python: `openai` (≥1.0)
- Node.js / TypeScript: `openai` (≥4.0)
- Java: `openai-java` (≥1.0)
- Go: `openai-go`
- .NET: `OpenAI-DotNet`

## 客户端配置

```python
from openai import OpenAI
client = OpenAI(
    api_key="any-string",  # 网关接受任意 api_key（认证在网关层做）
    base_url="https://gateway.example.com/v1",  # 指向网关的 OpenAI 兼容入口
)
```

```typescript
import OpenAI from 'openai';
const client = new OpenAI({
  apiKey: 'any-string',
  baseURL: 'https://gateway.example.com/v1',
});
```

## 客户端发出的请求格式

### Chat Completions 端点

```
POST {base_url}/chat/completions
Authorization: Bearer {api_key}
Content-Type: application/json
```

请求体：

```json
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "..."},
    {"role": "user", "content": "..."}
  ],
  "temperature": 0.7,
  "stream": false,
  "tools": [...]
}
```

### Responses 端点（新 SDK）

```
POST {base_url}/responses
```

```json
{
  "model": "gpt-5",
  "input": "...",
  "instructions": "..."
}
```

## SDK 期望的响应格式

### 非流式

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4o-2024-08-06",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "..."},
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  }
}
```

### 流式（SSE）

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: [DONE]
```

## SDK 关键依赖

| 字段 | SDK 期望 | 缺失后果 |
|------|----------|----------|
| `id` | 必须存在且字符串 | SDK 报错 |
| `object: "chat.completion"` | 必须是这个值 | SDK 可能拒绝 |
| `created` | int unix 秒 | SDK 日志缺失 |
| `model` | 模型 ID 字符串 | SDK 计费不准 |
| `choices[].index` | int | 候选解析失败 |
| `choices[].message.role` | "assistant" | SDK 校验失败 |
| `choices[].finish_reason` | "stop"\|"length"\|"tool_calls"\|"content_filter" | SDK 行为异常 |
| `usage.prompt_tokens`/`completion_tokens`/`total_tokens` | 必须存在 | 计费异常 |
| 流式 `data: [DONE]` | 必须存在 | SDK 进入挂起状态 |

## 客户端 → 网关 → 厂商 转换要求

### 场景 1：OpenAI SDK → OpenAI 模型

入站是 OpenAI Chat 协议，网关需要：

1. **ParseOpenAI**：将客户端请求解析为 IR。
2. **URSM v2 路由**：选择 OpenAI 厂商。
3. **SerializeOpenAI**：将 IR 序列化为 OpenAI Chat 协议。
4. **ParseOpenAIResponse**：将上游响应解析回 IR。
5. **SerializeOpenAIResponse**：将 IR 序列化回客户端期望的 OpenAI Chat 响应。

### 场景 2：OpenAI SDK → Anthropic 模型

入站是 OpenAI Chat 协议，网关需要：

1. **ParseOpenAI**：将客户端请求解析为 IR。
2. **URSM v2 路由**：选择 Anthropic 厂商。
3. **SerializeAnthropic**：将 IR 序列化为 Anthropic Messages 协议（需要把 `messages` 转换，system 提到顶层，max_tokens 必填，tools.input_schema 重命名）。
4. **ParseAnthropicResponse**：将上游响应解析回 IR。
5. **SerializeOpenAIResponse**：将 IR 序列化回 OpenAI Chat 响应（finish_reason 映射、stop_reason → finish_reason）。

### 场景 3：OpenAI SDK → Gemini 模型

类似场景 2，需要额外处理：
- `role: "assistant"` → `role: "model"`
- `system message` → `systemInstruction.parts`
- `tools` → `functionDeclarations`
- `tool_choice` → `toolConfig.functionCallingConfig`
- `usage.cachedContentTokenCount` → `usage.cached_tokens`

## 网关对客户端的承诺

1. **协议兼容**：客户端可以用 OpenAI SDK 调任何厂商模型，不需要修改客户端代码。
2. **流式兼容**：流式响应必须是 SSE，结束标记必须是 `data: [DONE]`。
3. **错误兼容**：错误响应必须是 OpenAI 风格的 `{"error": {"message", "type", "code"}}`。
4. **usage 字段**：必须填全（即使上游没返回，也要给一个 best-effort 值）。

## 不兼容场景与降级

| 上游缺失 | 网关降级策略 |
|----------|-------------|
| 上游不返回 `usage` | 网关估算 `prompt_tokens`/`completion_tokens` |
| 上游不返回 `model` 字段 | 网关填入请求时的 model |
| 上游不返回 `id` | 网关生成伪 UUID |
| 上游流式结束标记不是 `data: [DONE]` | 网关在序列化时插入 |
| 上游 finish_reason 不在枚举中 | 网关映射到最近的 OpenAI 等价值 |
