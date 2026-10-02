# Claude Code CLI / Agent 请求格式要求

## 概述

Claude Code 是 Anthropic 的官方命令行 Agent，**直接走 Anthropic Messages 协议**（不是 OpenAI 兼容模式）。

## 入站格式

完全符合 [anthropic-sdk.md](anthropic-sdk.md) 中描述的 Anthropic Messages API。

唯一差异：

- Claude Code 会发送 `anthropic-beta` header，列出启用的 beta 特性
- 启用扩展思考时，Claude Code 会发送 `thinking.type: "enabled"`
- Claude Code 的 tool 列表可能包含自定义 Agent 工具（如 `Bash`、`Edit`、`MultiEdit`）

## Claude Code 的特殊请求模式

### 1. 扩展思考（Extended Thinking）

```json
{
  "thinking": {
    "type": "enabled",
    "budget_tokens": 5000
  }
}
```

`budget_tokens` 必须小于 `max_tokens`。

### 2. Prompt Caching

通过 `cache_control` 块控制：

```json
{
  "system": [
    {
      "type": "text",
      "text": "You are Claude Code, Anthropic's official CLI for Claude.",
      "cache_control": {"type": "ephemeral"}
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "Long context here...",
          "cache_control": {"type": "ephemeral"}
        }
      ]
    }
  ]
}
```

### 3. Tool 工具（Agent 内置）

Claude Code 的 tools 数组包含：

```json
{
  "tools": [
    {"name": "Bash", "description": "...", "input_schema": {...}},
    {"name": "Edit", "description": "...", "input_schema": {...}},
    {"name": "Read", "description": "...", "input_schema": {...}},
    {"name": "Glob", "description": "...", "input_schema": {...}},
    {"name": "Grep", "description": "...", "input_schema": {...}},
    {"name": "MultiEdit", "description": "...", "input_schema": {...}},
    {"name": "WebFetch", "description": "...", "input_schema": {...}}
  ]
}
```

### 4. 上下文管理与编辑恢复

Claude Code 会利用 Anthropic 的 `context_management` beta：

```json
{
  "context_management": {
    "edits": [
      {
        "type": "clear_tool_uses_20250919",
        "trigger": {"type": "input_tokens", "value": 50000},
        "keep": {"type": "tool_uses", "value": 5}
      }
    ]
  }
}
```

### 5. 多模态

Claude Code 可以发送图片内容：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "Look at this image"},
    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
  ]
}
```

## 期望的响应格式

完全符合 Anthropic Messages API。Claude Code 期望：

1. `message_start` 事件先于任何 `content_block_start`
2. `thinking` block 在 `text` block 之前
3. `tool_use` block 后跟对应的 `tool_result`（在 user message 中）
4. `usage` 字段在流式中通过 `message_delta` 更新

## 网关对 Claude Code 的特殊支持

### 1. 透传 Anthropic 协议

如果后端是 Anthropic 模型，网关只做认证/路由/计费，body 透明转发。

### 2. 翻译到 OpenAI 兼容厂商

如果后端是 OpenAI/Qwen/GLM 等 OpenAI 兼容厂商：

1. **ParseAnthropic**：解析 Claude Code 发送的 Anthropic 请求。
2. **SerializeOpenAI**：
   - 把 `system` 字段移到 messages 数组开头作为 system message
   - `max_tokens` 透传
   - `tools[].input_schema` → `tools[].function.parameters`
   - `thinking` 字段丢弃（OpenAI 兼容厂商不支持）
   - `cache_control` 字段丢弃
   - `context_management` 字段丢弃
3. **ParseOpenAIResponse → SerializeAnthropicResponse**：
   - `choices[].message` → `content[].text` block
   - `tool_calls` → `content[].tool_use` block + 后续 user message 的 `tool_result`
   - `finish_reason` → `stop_reason`
   - `usage.prompt_tokens` → `usage.input_tokens`
   - `usage.completion_tokens` → `usage.output_tokens`

### 3. 翻译到 Gemini

类似 OpenAI 路径，额外处理 role/parts/functionDeclarations 转换。

## 不兼容警告

⚠️ **Claude Code 通过 OpenAI 兼容厂商调用时，部分功能不可用**：

- ❌ `thinking`：除 Anthropic 外的厂商不识别 thinking block
- ❌ `cache_control`：prompt caching 是 Anthropic 私有特性
- ❌ `context_management`：是 Anthropic beta 特性
- ✅ tools：OpenAI 兼容厂商支持 function calling，但 tool name 必须符合 OpenAI 命名规范（PascalCase 不一定支持）
- ✅ streaming：所有厂商都支持

## 网关审计点

1. **thinking 块必须有 signature**：Anthropic 序列化时绝不能伪造 signature。
2. **cache_control 必须丢弃**：除非目标方言也是 Anthropic。
3. **context_management 必须丢弃**：除非目标方言也是 Anthropic。
4. **tool_result 必须在 user message**：不能合并到 assistant message。
5. **tool name 命名规范化**：OpenAI 要求 `^[a-zA-Z0-9_-]{1,64}$`，Claude Code 工具名需要规范化。
