# 工具调用的跨协议转换

## 概述

不同厂商的 tool 定义、tool 调用消息结构差异很大，本文档描述 IR 如何统一表示这些差异。

## Tool 定义

### IR 表示

```go
type Tool struct {
    Type        string         // "function" | "web_search" | "file_search" | "code_interpreter"
    Name        string
    Description string
    Parameters  map[string]any // JSON Schema
    Strict      bool           // OpenAI 严格模式
}
```

### OpenAI 表示

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "...",
    "parameters": {
      "type": "object",
      "properties": {"location": {"type": "string"}},
      "required": ["location"]
    }
  }
}
```

OpenAI 还有 `strict: true` 字段，强制 JSON Schema 合规。

### Anthropic 表示

```json
{
  "name": "get_weather",
  "description": "...",
  "input_schema": {
    "type": "object",
    "properties": {"location": {"type": "string"}},
    "required": ["location"]
  }
}
```

注意：
- 没有 `type: "function"` 包装
- `input_schema` 而非 `parameters`
- 不支持 `strict`

### Gemini 表示

```json
{
  "functionDeclarations": [
    {
      "name": "get_weather",
      "description": "...",
      "parameters": {
        "type": "object",
        "properties": {"location": {"type": "string"}},
        "required": ["location"]
      }
    }
  ]
}
```

注意：
- 多个 function 共用一个 `functionDeclarations` 数组
- 没有 `type: "function"` 包装
- `parameters` 字段名同 OpenAI

### 序列化规则

| 源 | 目标 OpenAI | 目标 Anthropic | 目标 Gemini |
|----|-----------|----------------|-------------|
| IR Tool | `{"type":"function","function":{"name":...,"parameters":...}}` | `{"name":...,"input_schema":...}` | `{"functionDeclarations":[{"name":...,"parameters":...}]}` |

## Tool Choice

### IR 表示

```go
type ToolChoice struct {
    Type         string // "auto" | "none" | "required" | "any" | "function"
    FunctionName string
}
```

### 各厂商表示

| IR | OpenAI | Anthropic | Gemini |
|----|--------|-----------|--------|
| `{Type: "auto"}` | `"auto"` | `{"type":"auto"}` | `{"functionCallingConfig":{"mode":"AUTO"}}` |
| `{Type: "none"}` | `"none"` | `{"type":"none"}`（如果支持） | `{"functionCallingConfig":{"mode":"NONE"}}` |
| `{Type: "required"}` | `"required"` | `{"type":"any"}` | `{"functionCallingConfig":{"mode":"ANY"}}` |
| `{Type: "any"}` | `"required"` | `{"type":"any"}` | `{"functionCallingConfig":{"mode":"ANY"}}` |
| `{Type: "function", FunctionName: "x"}` | `{"type":"function","function":{"name":"x"}}` | `{"type":"tool","name":"x"}` | `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["x"]}}` |

## Tool Calls（assistant message 内）

### IR 表示

```go
type ToolCall struct {
    ID       string
    Type     string // "function"
    Function struct {
        Name      string
        Arguments map[string]any // 已 parse 为 map
    }
}
```

### 各厂商表示

#### OpenAI（assistant message）

```json
{
  "role": "assistant",
  "tool_calls": [
    {
      "id": "call_xxx",
      "type": "function",
      "function": {
        "name": "get_weather",
        "arguments": "{\"location\":\"SF\"}"  // 字符串 JSON
      }
    }
  ]
}
```

#### OpenAI（流式 delta）

```json
{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_xxx","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]}}]}
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":"}}]}}]}
{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]}}]}
{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}
```

`arguments` 是增量拼接的字符串，IR 需要在解析时累积。

#### Anthropic（assistant message content block）

```json
{
  "role": "assistant",
  "content": [
    {"type": "text", "text": "Let me check the weather"},
    {"type": "tool_use", "id": "toolu_xxx", "name": "get_weather", "input": {"location": "SF"}}
  ]
}
```

注意：
- tool_use 是 content block，**不是** tool_calls 字段
- `input` 是对象，**不是**字符串

#### Gemini（parts）

```json
{
  "role": "model",
  "parts": [
    {"text": "Let me check the weather"},
    {"functionCall": {"name": "get_weather", "args": {"location": "SF"}}}
  ]
}
```

注意：
- `functionCall` 是 part type
- `args` 是对象

### 序列化规则

| 源 | 目标 OpenAI | 目标 Anthropic | 目标 Gemini |
|----|-----------|----------------|-------------|
| IR ToolCall | `tool_calls[]` + `arguments` 字符串 | content block `tool_use` + `input` 对象 | parts `functionCall` + `args` 对象 |

⚠️ **关键**：OpenAI `arguments` 必须是字符串，IR map 在序列化时必须 `json.Marshal`。

## Tool Result（tool response）

### IR 表示

```go
type ToolResult struct {
    ToolUseID string
    Content   string
    IsError   bool
}
```

### 各厂商表示

#### OpenAI

```json
{
  "role": "tool",
  "tool_call_id": "call_xxx",
  "content": "sunny"
}
```

#### Anthropic（user message）

```json
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_xxx",
      "content": "sunny",
      "is_error": false
    }
  ]
}
```

#### Gemini（user message parts）

```json
{
  "role": "user",
  "parts": [
    {
      "functionResponse": {
        "name": "get_weather",
        "response": {"result": "sunny"}
      }
    }
  ]
}
```

注意 Gemini `functionResponse` 不需要 ID，但需要 function name。

### 序列化规则

| 源 | 目标 OpenAI | 目标 Anthropic | 目标 Gemini |
|----|-----------|----------------|-------------|
| IR ToolResult | `role:"tool"` message + `tool_call_id` | `role:"user"` message + `tool_result` content block | `role:"user"` parts + `functionResponse` |

## 多轮 Tool 调用对话

完整的 tool 调用对话示例（IR 视角）：

```
1. user: "What's the weather in SF?"
2. assistant:
   - content: "Let me check"
   - tool_calls: [{name: "get_weather", args: {location: "SF"}}]
3. tool: {tool_call_id: "call_xxx", content: "sunny"}
4. assistant: "It's sunny in SF"
```

不同厂商的多轮 tool 调用结构差异：

| 厂商 | Tool result 位置 | 命名 |
|------|------------------|------|
| OpenAI | `role: "tool"` message | `tool_call_id` |
| Anthropic | `role: "user"` message，content block `tool_result` | `tool_use_id` |
| Gemini | `role: "user"` parts `functionResponse` | `name` (function name) |

IR 统一用 `ToolResult` 类型，序列化时按目标协议还原。

## 内置工具

OpenAI Responses API 提供内置工具：

| 工具 | 类型 | 是否可移植 |
|------|------|-----------|
| `web_search` | 内置 | 仅 OpenAI |
| `file_search` | 内置 | 仅 OpenAI |
| `code_interpreter` | 内置 | 仅 OpenAI |
| `computer_use_preview` | 内置 | 仅 OpenAI |

当目标厂商不支持内置工具时，网关必须：
1. 丢弃内置工具
2. 返回警告给客户端（`x-gateway-warning` header）
3. 客户端需要降级处理
