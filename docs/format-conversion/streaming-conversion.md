# 流式响应的双向转换

## 概述

流式响应比非流式响应复杂得多，因为网关需要在 chunk 级别进行转换。本文档描述流式响应的所有转换细节。

## 关键约束

1. **不可缓存完整响应**：流式响应必须按 chunk 下发，缓存会破坏交互体验。
2. **不可丢失顺序**：流式 chunk 的顺序必须在转换中保持。
3. **错误必须及时中断**：上游错误必须立即中断流式响应。
4. **usage 必须在末块**：Anthropic 流式 usage 在 `message_delta`，OpenAI 流式 usage 在末块（`stream_options.include_usage=true`）。

## 各厂商流式响应格式

### OpenAI Chat SSE

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}

data: [DONE]
```

### Anthropic SSE（event-based）

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_xxx","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}
```

### Gemini SSE（alt=sse）

```
data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello!"}]},"finishReason":"STOP","index":0}]}
```

### Ollama NDJSON

```
{"model":"llama3.1","message":{"role":"assistant","content":""},"done":false}
{"model":"llama3.1","message":{"role":"assistant","content":"Hel"},"done":false}
{"model":"llama3.1","message":{"role":"assistant","content":"Hello"},"done":true,"total_duration":...}
```

## IR StreamChunk

```go
type StreamChunk struct {
    ID              string
    Object          string
    Created         int64
    Model           string
    Delta           *Message        // 增量内容
    FinishReason    string
    StopReason      string
    Usage           *Usage          // 仅末块
    Error           *UpstreamError  // 错误时中断流
    ReasoningDelta  string          // 推理增量
    ToolCallDelta   []ToolCallDelta // 工具调用增量
    ContentBlockIndex int           // Anthropic style
}
```

## 转换规则

### OpenAI Chat 流式 → Anthropic 流式

```
OpenAI chunk                    Anthropic events
─────────────────────────────────────────────────
{"choices":[{"delta":{"role":"assistant"}}]}  →  message_start
{"choices":[{"delta":{"content":""}}]}        →  content_block_start
{"choices":[{"delta":{"content":"Hel"}}]}      →  content_block_delta(text_delta)
{"choices":[{"delta":{},"finish_reason":"stop"}]}  →  content_block_stop, message_delta(stop_reason), message_stop
{"choices":[],"usage":{...}}                  →  message_delta(usage)
```

### Anthropic 流式 → OpenAI Chat 流式

```
Anthropic event                   OpenAI chunk
──────────────────────────────────────────────
message_start                     {"choices":[{"delta":{"role":"assistant"}}]}
content_block_start(text)         {"choices":[{"delta":{"content":""}}]}
content_block_delta(text_delta)   {"choices":[{"delta":{"content":"Hel"}}]}
content_block_stop                (no-op)
message_delta(stop_reason)        {"choices":[{"delta":{},"finish_reason":"stop"}]}
message_delta(usage)              {"choices":[],"usage":{...}}
message_stop                      data: [DONE]
```

### Gemini 流式 → OpenAI Chat 流式

```
Gemini chunk                      OpenAI chunk
──────────────────────────────────────────────
{"candidates":[{"content":{"parts":[{"text":""}]}}]}   →  {"choices":[{"delta":{"role":"assistant"}}]}
{"candidates":[{"content":{"parts":[{"text":"Hel"}}]}]} →  {"choices":[{"delta":{"content":"Hel"}}]}
{"candidates":[{"finishReason":"STOP"}]}                →  {"choices":[{"delta":{},"finish_reason":"stop"}]}
{"usageMetadata":{...}}                                 →  {"choices":[],"usage":{...}}
```

## 流式响应合成器

### 职责

流式响应合成器（位于 `domains/streaming/`）负责：

1. **读取上游流**：按厂商格式读取 chunk。
2. **ParseXxxStreamChunk**：解析为 IR StreamChunk。
3. **合成客户端流**：按客户端协议生成 SSE/NDJSON。
4. **错误传播**：上游错误立即中断流并下发错误事件。
5. **usage 注入**：仅在末块注入 usage。
6. **finish_reason 规范化**：按客户端方言映射。

### 实现位置

- `domains/streaming/executors/executor_chat.go`：OpenAI Chat 出站流
- `domains/streaming/executors/executor_anthropic.go`：Anthropic 出站流
- `domains/streaming/executors/native_responses.go`：OpenAI Responses 流
- `internal/ir/response_protocols.go`：响应序列化（含流式）

## 关键边界场景

### 1. 上游无 usage

某些厂商（如老版 Ollama）不返回 usage。网关需要：

- 在末块注入估算的 usage
- 客户端期望 usage 必填，否则 SDK 行为异常

### 2. 上游 finish_reason 不在枚举

某些厂商（如 GLM）返回特殊 finish_reason（`network_error`、`sensitive`）。网关需要：

- 解析时识别为错误（`UpstreamError`）
- 中断流式响应
- 返回错误事件

### 3. 客户端断连

客户端可能中途断连。网关需要：

- 取消上游 HTTP 请求
- 释放资源
- 不再下发任何 chunk

### 4. 上游断连

上游可能中途断连。网关需要：

- 检测到断连后立即下发错误事件给客户端
- 关闭客户端连接

### 5. thinking 流式（Anthropic）

Anthropic thinking 流式需要正确的 event 顺序：

```
message_start
content_block_start (type: thinking, signature: "")
content_block_delta (thinking_delta)
content_block_delta (signature_delta)
content_block_stop
content_block_start (type: text, text: "")
content_block_delta (text_delta)
content_block_stop
message_delta
message_stop
```

⚠️ signature_delta 必须紧跟 thinking_delta，否则 Anthropic SDK 会验证失败。

### 6. tool_calls 流式拼接

OpenAI 流式 tool_calls.arguments 是增量 JSON 字符串：

```
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file\":"}}]}}]}
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.py\""}}]}}]}
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}
```

网关需要：
1. 累积同一 `index` 的 arguments 字符串
2. 解析累积后的 JSON
3. 序列化为目标协议时按目标规则输出
