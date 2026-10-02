# Anthropic 官方 SDK 请求格式要求

## 适用 SDK

- Python: `anthropic` (≥0.30)
- TypeScript: `@anthropic-ai/sdk`
- Go: `anthropic-sdk-go`

## 客户端配置

```python
import anthropic
client = anthropic.Anthropic(
    api_key="any-string",  # 网关接受任意 api_key
    base_url="https://gateway.example.com",  # 指向网关的 Anthropic 兼容入口
)
```

```typescript
import Anthropic from '@anthropic-ai/sdk';
const client = new Anthropic({
  apiKey: 'any-string',
  baseURL: 'https://gateway.example.com',
});
```

## 客户端发出的请求格式

### Messages 端点

```
POST {base_url}/v1/messages
x-api-key: {api_key}
anthropic-version: 2023-06-01
Content-Type: application/json
```

请求体：

```json
{
  "model": "claude-3-5-sonnet-20241022",
  "max_tokens": 1024,
  "messages": [
    {"role": "user", "content": "..."}
  ],
  "system": "..."
}
```

## SDK 期望的响应格式

### 非流式

```json
{
  "id": "msg_xxx",
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "Hello!"}
  ],
  "model": "claude-3-5-sonnet-20241022",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 20
  }
}
```

### 流式（SSE）

事件：`message_start` → `content_block_start` → `content_block_delta`（多次）→ `content_block_stop` → `message_delta` → `message_stop`

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_xxx","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}
```

## SDK 关键依赖

| 字段 | SDK 期望 | 缺失后果 |
|------|----------|----------|
| `id: "msg_xxx"` | 必须存在 | SDK 抛错 |
| `type: "message"` | 必填 | SDK 校验失败 |
| `role: "assistant"` | 必填 | SDK 校验失败 |
| `content[].type` | `text`/`tool_use`/`thinking` | SDK 拒绝未知类型 |
| `stop_reason` | `end_turn`/`max_tokens`/`stop_sequence`/`tool_use`/`refusal` | SDK 行为异常 |
| `usage.input_tokens`/`output_tokens` | 必须存在 | 计费异常 |
| 流式事件顺序 | 必须 message_start → ... → message_stop | SDK 解析失败 |
| `anthropic-version` header | 客户端发送的 header | 网关需要透传 |

## 客户端 → 网关 → 厂商 转换要求

### 场景 1：Anthropic SDK → Anthropic 模型

直通模式：网关只做认证、限流、计费，不修改 body。

### 场景 2：Anthropic SDK → OpenAI 模型

入站是 Anthropic Messages 协议，网关需要：

1. **ParseAnthropic**：将客户端请求解析为 IR（注意 `system` 顶层字段、`max_tokens` 必填）。
2. **URSM v2 路由**：选择 OpenAI 厂商。
3. **SerializeOpenAI**：将 IR 序列化为 OpenAI Chat 协议（system message 提取、max_tokens → max_tokens/max_completion_tokens、tools.parameters 不变）。
4. **ParseOpenAIResponse**：将上游响应解析回 IR。
5. **SerializeAnthropicResponse**：将 IR 序列化回 Anthropic Messages 响应（finish_reason → stop_reason、内容块重建、usage 字段映射）。

### 场景 3：Anthropic SDK → Gemini 模型

类似场景 2，额外处理：
- `role: "assistant"` → `role: "model"`
- Anthropic `tool_use_id` → Gemini 不用
- Anthropic `tool_result` (user message) → Gemini `functionResponse` part
- Gemini `finishReason` → Anthropic `stop_reason`

### 场景 4：Anthropic SDK → DeepSeek/Qwen 等 OpenAI 兼容厂商

类似场景 2，但要注意：
- 这些厂商的 OpenAI 兼容路径中，`max_tokens` 是可选的；IR 需要从 Anthropic 入站的 `max_tokens` 透传。
- 工具调用格式与 OpenAI 一致，但有些厂商不接受 `parallel_tool_calls: true`。

## 网关对客户端的承诺

1. **协议兼容**：客户端可以用 Anthropic SDK 调任何厂商模型。
2. **流式兼容**：流式事件必须按 Anthropic 顺序（`message_start` → `content_block_*` → `message_delta` → `message_stop`）。
3. **错误兼容**：错误响应必须是 Anthropic 风格的 `{"type": "error", "error": {"type", "message"}}`。
4. **thinking block 兼容**：如上游有 thinking，序列化时必须保留 `signature`。
5. **content block 顺序**：text/thinking/tool_use 的顺序必须保留。

## 不兼容场景与降级

| 上游缺失 | 网关降级策略 |
|----------|-------------|
| 上游不返回 `usage.input_tokens` | 网关估算 |
| 上游不返回 `model` 字段 | 网关填入请求时的 model |
| 上游流式事件顺序异常 | 网关重组为标准顺序 |
| 上游不返回 `stop_reason` | 网关填入 `"end_turn"` |
| 上游 `stop_reason` 不在枚举中 | 网关映射到最近的 Anthropic 等价值 |
| 上游响应有 thinking 但无 signature | 网关不输出为 Anthropic thinking（避免伪造 signature），仅在 `content` 末尾追加 |
