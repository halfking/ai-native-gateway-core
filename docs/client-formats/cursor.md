# Cursor IDE 内置 Agent 请求格式要求

## 概述

Cursor 是 IDE Agent，通过 OpenAI 兼容的 Chat Completions 协议接入网关。

## 入站格式

完全符合 [openai-sdk.md](openai-sdk.md) 中描述的 OpenAI Chat Completions API。

唯一差异：

- Cursor 在 baseURL 路径上会带 `/v1`
- Cursor 的模型名采用自己的命名（`claude-3.5-sonnet`、`gpt-4o` 等），由网关映射到对应厂商的模型 ID
- Cursor 支持多模态（图片粘贴）

## Cursor 的特殊请求模式

### 1. 模型选择

Cursor 允许用户在 UI 选择模型，请求体里 `model` 字段是 Cursor 命名：

| Cursor 模型名 | 实际厂商模型 |
|---------------|--------------|
| `claude-3.5-sonnet` | Anthropic Claude 3.5 Sonnet |
| `claude-3.7-sonnet` | Anthropic Claude 3.7 Sonnet |
| `gpt-4o` | OpenAI GPT-4o |
| `gpt-4o-mini` | OpenAI GPT-4o mini |
| `o1-preview` | OpenAI o1-preview |
| `o1-mini` | OpenAI o1-mini |
| `cursor-small` | Cursor 自家小模型（基于开源） |

### 2. 自动应用配置

Cursor 通过工具调用自动应用代码改动：

```json
{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "apply_edit",
        "description": "Apply an edit to a file",
        "parameters": {...}
      }
    }
  ]
}
```

### 3. 多模态图片输入

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "Fix this code"},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,...", "detail": "high"}}
  ]
}
```

### 4. Stream

Cursor 默认使用流式响应。

## 期望的响应格式

完全符合 OpenAI Chat Completions API。

Cursor 特别依赖：
- `finish_reason: "tool_calls"` 才能触发代码应用
- `tool_calls[].function.name` 必须精确匹配
- `tool_calls[].function.arguments` 必须是有效 JSON 字符串
- 流式 delta 中 `tool_calls` 增量传输必须完整

## 网关对 Cursor 的特殊支持

### 1. 模型名映射

网关需要把 Cursor 模型名映射到具体厂商的模型 ID：

```yaml
cursor_models:
  claude-3.5-sonnet: anthropic/claude-3-5-sonnet-20241022
  gpt-4o: openai/gpt-4o-2024-08-06
  cursor-small: openai/gpt-4o-mini
```

### 2. 协议转换

当 Cursor 通过 OpenAI 协议访问 Anthropic 模型时：

1. **ParseOpenAI**：解析 Cursor 请求。
2. **SerializeAnthropic**：转换 body 为 Anthropic Messages。
3. **ParseAnthropicResponse → SerializeOpenAIResponse**：反向转换。

注意：
- `max_tokens` 必须填（Cursor 不一定传）
- `system` 字段必须提取
- `tools[].function.parameters` → `tools[].input_schema`

### 3. 流式工具调用

Cursor 的流式 tool 调用必须完整：

```
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_xxx","type":"function","function":{"name":"apply_edit","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.py\""}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}
```

`arguments` 是流式增量拼接的 JSON 字符串，IR 需要在序列化时正确累积。

## 不兼容警告

- ❌ `stream_options.include_usage`：Cursor 不解析
- ❌ `response_format`：Cursor 不使用 JSON 模式
- ❌ `parallel_tool_calls`：Cursor 默认单 tool call
- ✅ `tools`：完整支持
- ✅ 多模态：完整支持
