# 推理/思考内容的跨协议转换

## 概述

不同厂商的"推理/思考"内容位置和格式差异较大：

| 厂商 | 推理内容位置 | 字段 |
|------|--------------|------|
| OpenAI Chat | 顶层 `reasoning_content` | string |
| OpenAI Chat 流式 | `delta.reasoning_content` | string |
| DeepSeek | 顶层 `reasoning_content` | string |
| DeepSeek 流式 | `delta.reasoning_content` | string |
| Qwen3 | 顶层 `reasoning_content` | string |
| Qwen 流式 | `delta.reasoning_content` | string |
| GLM-Z1 | 顶层 `reasoning_content` | string |
| GLM 流式 | `delta.reasoning_content` | string |
| Kimi | 顶层 `reasoning_content` | string |
| MiniMax | 顶层 `reasoning_content` | string |
| Doubao 1.5 Thinking | 顶层 `reasoning_content` | string |
| Grok 3 mini | 顶层 `reasoning_content` | string |
| Mistral Magistral | 顶层 `reasoning_content` | string |
| OpenRouter | `reasoning` 或 `reasoning_content` | string |
| **Anthropic** | **ContentBlock `thinking`** | **object with signature** |
| **Anthropic 流式** | **ContentBlockDelta `thinking_delta` + `signature_delta`** | **string + signature** |
| Gemini 2.5+ | Part `{text: "...", thought: true}` | part with thought flag |

## IR 表示

```go
// 响应级（顶层 reasoning_content）
type InternalResponse struct {
    ReasoningContent string  // 顶层推理（OpenAI 风格）
    ...
}

// message 内（Anthropic thinking block）
type ContentBlock struct {
    Thinking *Thinking
}

type Thinking struct {
    Type      string // "thinking" | "redacted_thinking"
    Thinking  string
    Signature string  // Anthropic signature 必须保留
}

// 流式
type StreamChunk struct {
    ReasoningDelta string // OpenAI 风格
    Delta          *Message  // 包含 Thinking 增量（Anthropic 风格）
    ...
}
```

## Anthropic Thinking 的特殊约束

### signature 必须保留

Anthropic 在响应中返回的 thinking block 包含 `signature`：

```json
{
  "type": "thinking",
  "thinking": "Let me think...",
  "signature": "abc123..."
}
```

`signature` 用于 Anthropic 验证该 thinking 块未被篡改。**绝不能伪造 signature**。

### 多轮对话中的 thinking

当客户端把响应回传到下一轮请求时：

- 有 signature 的 thinking：作为 thinking block 保留
- 无 signature 的 thinking：必须降级处理

### 降级策略

当客户端请求的目标方言不支持 thinking（如 OpenAI）：

1. **有 signature 的 Anthropic thinking**：
   - Anthropic → Anthropic：保留
   - Anthropic → OpenAI：丢弃（OpenAI 不接受 thinking block）
   - Anthropic → Gemini：丢弃（Gemini 不接受 thinking block）

2. **无 signature 的 vendor reasoning**：
   - 任何 → Anthropic：**不能**伪造 signature 输出为 thinking
   - 只能作为 text 追加或丢弃
   - 任何 → OpenAI：作为 `reasoning_content` 顶层字段
   - 任何 → Gemini：作为 part with `thought: true`

⚠️ **审计结论**：跨到 Anthropic 的无签名 vendor reasoning 不可等价保留为可回传的 thinking，这是协议签名约束，不应通过伪造 signature 绕过（P1-Reasoning 已验证）。

## 转换规则

### OpenAI Chat → Anthropic

```
OpenAI:
  {
    "reasoning_content": "Let me think...",
    "content": "Final answer"
  }

Anthropic:
  {
    "content": [
      {"type": "thinking", "thinking": "Let me think..."},  // ⚠️ 无 signature
      {"type": "text", "text": "Final answer"}
    ]
  }
```

⚠️ 这里 thinking 没有 signature，Anthropic SDK 在多轮对话时会拒绝该 thinking。**建议**：丢弃该 thinking，仅保留 text。

### Anthropic → OpenAI Chat

```
Anthropic:
  {
    "content": [
      {"type": "thinking", "thinking": "Let me think...", "signature": "abc..."},
      {"type": "text", "text": "Final answer"}
    ]
  }

OpenAI:
  {
    "reasoning_content": "Let me think...",  // signature 不暴露
    "content": "Final answer"
  }
```

signature 在序列化时丢弃（OpenAI 不使用）。

### OpenAI Chat → Gemini

```
OpenAI:
  {
    "reasoning_content": "Let me think...",
    "content": "Final answer"
  }

Gemini:
  {
    "candidates": [{
      "content": {
        "role": "model",
        "parts": [
          {"text": "Let me think...", "thought": true},
          {"text": "Final answer"}
        ]
      }
    }]
  }
```

### Gemini → OpenAI Chat

```
Gemini:
  {
    "candidates": [{
      "content": {
        "role": "model",
        "parts": [
          {"text": "Let me think...", "thought": true},
          {"text": "Final answer"}
        ]
      }
    }]
  }

OpenAI:
  {
    "reasoning_content": "Let me think...",
    "content": "Final answer"
  }
```

### 流式 reasoning 转换

#### OpenAI Chat 流式 → Anthropic 流式

```
OpenAI SSE:
  data: {"choices":[{"delta":{"reasoning_content":"Let me"}}]}
  data: {"choices":[{"delta":{"reasoning_content":" think"}}]}
  data: {"choices":[{"delta":{"content":"Final"}}]}
  data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

Anthropic SSE:
  event: content_block_start
  data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

  event: content_block_delta
  data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}

  event: content_block_stop
  data: {"type":"content_block_stop","index":0}

  event: content_block_start
  data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

  event: content_block_delta
  data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Final"}}

  event: content_block_stop
  data: {"type":"content_block_stop","index":1}

  event: message_delta
  data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

  event: message_stop
  data: {"type":"message_stop"}
```

⚠️ **缺失 signature_delta**：Anthropic SDK 会因为 thinking block 无 signature 而验证失败。

**实际行为**：网关应该丢弃无 signature 的 thinking 流，仅发送 text 流。

## 内部测试覆盖

`internal/ir/` 目录下已有以下测试覆盖：

| 测试 | 覆盖 |
|------|------|
| `serialize_openai_thinking_p5_test.go` | OpenAI Chat thinking 序列化 |
| `source_reasoning_context_test.go` | reasoning 上下文 |
| `serialize_responses_extension_loss_test.go` | Responses API 扩展字段 |
| `golden_roundtrip_corpus_test.go` | round-trip 全场景 |

## 网关审计点

1. **Anthropic thinking 必须有 signature**：无 signature 不能输出为 Anthropic thinking。
2. **vendor reasoning 不能伪造 Anthropic signature**：避免 Anthropic 验证失败。
3. **流式顺序必须保持**：Anthropic thinking_delta 必须在 signature_delta 之前（即使我们丢弃）。
4. **reasoning token 必须计费**：从 `usage.completion_tokens_details.reasoning_tokens` 或 `usage.thoughtsTokenCount` 提取。
5. **reasoning 不能泄露给客户端的 system prompt**：避免推理内容被注入到下次请求。
