# OpenAI Responses API 格式规范

## Base Protocol

- 名称: `openai-responses`
- 方言: `DialectResponses`
- 官方文档: https://platform.openai.com/docs/api-reference/responses
- 适用: gpt-5、o-series 等新模型，支持工具调用 + 内置工具（web_search、file_search、code_interpreter）

## HTTP 形态

```
POST /v1/responses HTTP/1.1
Host: api.openai.com
Authorization: Bearer {OPENAI_API_KEY}
Content-Type: application/json
```

非流式响应 `application/json`，流式响应 `text/event-stream`。

## 请求 Body（顶层字段）

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `model` | string | ✅ | 模型 ID |
| `input` | array | ✅ | 输入项数组（与 messages 不同） |
| `instructions` | string | ❌ | 系统提示 |
| `temperature` | number | ❌ | 0-2 |
| `top_p` | number | ❌ | 0-1 |
| `max_output_tokens` | integer | ❌ | 最大输出 token |
| `previous_response_id` | string | ❌ | 多轮对话：引用上一轮 response |
| `reasoning` | object | ❌ | `{"effort": "low"|"medium"|"high"}` |
| `tools` | array | ❌ | 工具定义 |
| `tool_choice` | string/object | ❌ | 工具选择 |
| `parallel_tool_calls` | boolean | ❌ | 并发工具调用 |
| `stream` | boolean | ❌ | 是否流式 |
| `truncation` | string | ❌ | `auto`/`disabled` |
| `user` | string | ❌ | 终端用户标识 |
| `metadata` | object | ❌ | 自定义元数据 |
| `include` | array | ❌ | 额外包含字段 |

## Input 结构（与 messages 不同）

```json
[
  {"type": "message", "role": "user", "content": [
    {"type": "input_text", "text": "Hello"}
  ]}
]
```

input 项类型：
- `{"type": "message", "role": "user"|"assistant", "content": [...]}`：消息
- `{"type": "function_call", "call_id": "...", "name": "...", "arguments": "..."}`：函数调用
- `{"type": "function_call_output", "call_id": "...", "output": "..."}`：函数结果
- `{"type": "reasoning", "summary": [...]}`：内部推理
- `{"type": "item_reference", "id": "..."}`：引用之前的项

`content` 元素类型：
- `input_text`：文本输入
- `input_image`：图片输入
- `input_file`：文件输入
- `output_text`：文本输出（在 response 中）

## Tools 结构（内置工具 + function）

```json
{
  "tools": [
    {"type": "function", "name": "get_weather", "description": "...", "parameters": {...}},
    {"type": "web_search"},
    {"type": "file_search"},
    {"type": "code_interpreter", "container": {"type": "auto"}},
    {"type": "computer_use_preview", "display_width": 1024, "display_height": 768}
  ]
}
```

## Tool Choice

```json
{"tool_choice": "auto"}
{"tool_choice": "required"}
{"tool_choice": "none"}
{"tool_choice": {"type": "function", "name": "get_weather"}}
```

## 响应（非流式）

```json
{
  "id": "resp_xxx",
  "object": "response",
  "created_at": 1700000000,
  "model": "gpt-5",
  "status": "completed",  // "completed" | "failed" | "in_progress" | "cancelled"
  "output": [
    {
      "id": "msg_xxx",
      "type": "message",
      "role": "assistant",
      "status": "completed",
      "content": [
        {"type": "output_text", "text": "Hello!", "annotations": []}
      ]
    }
  ],
  "usage": {
    "input_tokens": 10,
    "output_tokens": 20,
    "total_tokens": 30,
    "input_tokens_details": {"cached_tokens": 0},
    "output_tokens_details": {"reasoning_tokens": 5}
  },
  "previous_response_id": null,
  "instructions": null,
  "reasoning": null
}
```

`status` 取值：
- `completed`：完成
- `failed`：失败
- `in_progress`：进行中
- `cancelled`：已取消

## 流式响应（SSE）

事件类型：

| Event | 说明 |
|-------|------|
| `response.created` | 响应创建，携带初始壳 |
| `response.in_progress` | 响应进行中 |
| `response.output_item.added` | 新增输出项 |
| `response.content_part.added` | 新增内容块 |
| `response.output_text.delta` | 输出文本增量 |
| `response.output_text.done` | 输出文本完成 |
| `response.output_item.done` | 输出项完成 |
| `response.function_call_arguments.delta` | 函数调用参数增量 |
| `response.function_call_arguments.done` | 函数调用参数完成 |
| `response.web_search_call.*` | 内置 web_search 工具事件 |
| `response.file_search_call.*` | 内置 file_search 工具事件 |
| `response.completed` | 响应完成（含 usage） |
| `response.failed` | 响应失败 |
| `error` | 错误事件 |

## 错误响应

```json
{
  "error": {
    "message": "...",
    "type": "invalid_request_error",
    "code": "..."
  }
}
```

## 与 Chat Completions 的关键差异

1. **`input` 而非 `messages`**：Responses API 用 `input` 数组，每项是带 `type` 的复合结构。
2. **`output` 数组**：响应是 output 数组，每项带 `id` 和 `status`。
3. **`previous_response_id`**：服务端持久化对话历史，客户端可省略 input 只需传 ID。
4. **内置工具**：`web_search`、`file_search`、`code_interpreter` 是平台提供，无需 function schema。
5. **`reasoning` 而非 `reasoning_effort`**：Responses API 把推理参数包到 `reasoning.effort`。
6. **`max_output_tokens`**：取代 `max_tokens` 和 `max_completion_tokens`。
7. **`status` 字段**：响应级状态，区别于 `finish_reason`。

## IR 映射

| Responses API 字段 | IR 字段 |
|--------------------|---------|
| `input[].role` | `Message.Role` |
| `input[].content[].input_text.text` | `Message.Content[].Text` |
| `output[].content[].output_text.text` | `Response.Content[].Text` |
| `output[].content[].output_text.annotations` | `Message.Annotations` |
| `usage.input_tokens_details.cached_tokens` | `Usage.CachedTokens` |
| `usage.output_tokens_details.reasoning_tokens` | `Usage.ReasoningTokens` |
| `reasoning.effort` | `Request.ReasoningEffort` |
| `previous_response_id` | `Request.PreviousResponseID` |
| `tools[].type: "web_search"` | `Request.WebSearchTool` |
| `tools[].type: "code_interpreter"` | `Request.CodeInterpreterTool` |
| `tools[].type: "function"` | `Tool` (标准 function) |

## 序列化注意

1. 流式响应事件命名与 Chat Completions 的 SSE delta 完全不同，IR 内部统一为 `StreamChunk`，序列化为目标协议时按目标规则生成事件。
2. `output_text.annotations` 包含 file_citation、url_citation、file_path 等类型，IR 需要保留。
3. `previous_response_id` 模式下，客户端不需要重复发历史，IR 需要特殊处理（不强制序列化 input 历史）。
