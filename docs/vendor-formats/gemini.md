# Google Gemini generateContent API 格式规范

## Base Protocol

- 名称: `gemini-generate`
- 方言: `DialectGemini`
- 官方文档: https://ai.google.dev/api/generate-content

## HTTP 形态

```
POST /v1beta/models/{MODEL}:generateContent?key={API_KEY} HTTP/1.1
Host: generativelanguage.googleapis.com
Content-Type: application/json

{...}
```

流式版本：

```
POST /v1beta/models/{MODEL}:streamGenerateContent?alt=sse HTTP/1.1
```

注意：
- API key 通过 query string 或 `Authorization: Bearer` 头传递。
- 流式响应使用 `alt=sse` 时为 SSE；省略时为分块 JSON 数组。

## 请求 Body（顶层字段）

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `contents` | array | ✅ | 对话内容 |
| `systemInstruction` | object | ❌ | 系统提示 |
| `tools` | array | ❌ | 工具定义 |
| `toolConfig` | object | ❌ | 工具调用策略 |
| `safetySettings` | array | ❌ | 安全阈值 |
| `generationConfig` | object | ❌ | 生成参数 |
| `cachedContent` | string | ❌ | 上下文缓存 ID |

## Contents 结构

```json
{
  "role": "user",
  "parts": [
    {"text": "Hello"},
    {"inlineData": {"mimeType": "image/png", "data": "base64..."}}
  ]
}
```

`role` 取值：`user` 或 `model`（注意：模型消息用 `model` 而非 `assistant`）。

`parts` 类型：
- `{"text": "..."}`：文本
- `{"inlineData": {"mimeType": "...", "data": "base64"}}`：内联数据
- `{"fileData": {"mimeType": "...", "fileUri": "gs://..."}}`：云端文件
- `{"functionCall": {"name": "...", "args": {...}}}`：模型调用工具
- `{"functionResponse": {"name": "...", "response": {...}}`：工具结果
- `{"codeExecutionResult": {...}}`：代码执行结果

## System Instruction

```json
{
  "systemInstruction": {
    "role": "system",
    "parts": [{"text": "You are a helpful assistant."}]
  }
}
```

注意：Gemini 的 systemInstruction **必须**带 `role: "system"`。

## Tools 结构

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

注意：Gemini 没有 `{"type": "function"}` 包装层，直接用 `functionDeclarations`。

## ToolConfig

```json
{
  "toolConfig": {
    "functionCallingConfig": {
      "mode": "AUTO" | "ANY" | "NONE",
      "allowedFunctionNames": ["get_weather"]
    }
  }
}
```

`mode` 取值：
- `AUTO`：模型自行决定
- `ANY`：必须调用某个工具
- `NONE`：禁止工具调用

## SafetySettings

```json
[
  {
    "category": "HARM_CATEGORY_HARASSMENT",
    "threshold": "BLOCK_MEDIUM_AND_ABOVE"
  }
]
```

`category` 取值：
- `HARM_CATEGORY_HARASSMENT`
- `HARM_CATEGORY_HATE_SPEECH`
- `HARM_CATEGORY_SEXUALLY_EXPLICIT`
- `HARM_CATEGORY_DANGEROUS_CONTENT`

`threshold` 取值：`BLOCK_NONE`/`BLOCK_LOW_AND_ABOVE`/`BLOCK_MEDIUM_AND_ABOVE`/`BLOCK_ONLY_HIGH`。

## GenerationConfig

```json
{
  "temperature": 1.0,
  "topP": 0.95,
  "topK": 40,
  "maxOutputTokens": 2048,
  "stopSequences": ["\n"],
  "candidateCount": 1,
  "responseMimeType": "application/json",
  "responseSchema": {...},
  "thinkingConfig": {"thinkingBudget": 1024, "includeThoughts": true}
}
```

`thinkingConfig`：
- `thinkingBudget`：思考 token 上限，0 表示关闭，>0 启用
- `includeThoughts`：是否在响应中返回思考内容

## 响应（非流式）

```json
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "Hello!"}]
      },
      "finishReason": "STOP",
      "safetyRatings": [...],
      "citationMetadata": {...},
      "tokenCount": 0,
      "index": 0
    }
  ],
  "promptFeedback": {"safetyRatings": [...]},
  "usageMetadata": {
    "promptTokenCount": 10,
    "candidatesTokenCount": 20,
    "totalTokenCount": 30,
    "cachedContentTokenCount": 5,
    "thoughtsTokenCount": 8
  },
  "modelVersion": "gemini-1.5-pro"
}
```

`finishReason` 取值：
- `STOP`：自然结束
- `MAX_TOKENS`：达到 maxOutputTokens
- `SAFETY`：被安全策略过滤
- `RECITATION`：疑似抄袭
- `OTHER`：其他

## 流式响应（JSON 数组）

省略 `alt=sse` 时返回 JSON 数组，每行一个对象，结构同 `candidates[0]` 增量更新。

```
{"candidates": [{"content": {"role": "model", "parts": [{"text": ""}]}, "index": 0}]}
{"candidates": [{"content": {"role": "model", "parts": [{"text": "Hel"}]}, "index": 0}]}
{"candidates": [{"content": {"role": "model", "parts": [{"text": "Hello"}]}, "finishReason": "STOP", "index": 0}]}
{"usageMetadata": {...}, "modelVersion": "..."}
```

## SSE 模式（alt=sse）

```
data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello!"}]},"finishReason":"STOP","index":0}]}
```

## Thinking parts（Gemini 2.5+）

思考内容以独立 part 返回：

```json
{"parts": [
  {"text": "思考过程...", "thought": true},
  {"text": "最终答案"}
]}
```

`thought: true` 标记的 part 是思考内容；IR 必须映射到 `InternalResponse.ReasoningContent` 或专用的 `ThoughtPart`。

## 错误响应

```json
{
  "error": {
    "code": 400,
    "message": "...",
    "status": "INVALID_ARGUMENT"
  }
}
```

`status` 取值：`INVALID_ARGUMENT`/`UNAUTHENTICATED`/`PERMISSION_DENIED`/`NOT_FOUND`/`RESOURCE_EXHAUSTED`/`INTERNAL`/`UNAVAILABLE`/`DEADLINE_EXCEEDED`。

## 特殊字段（IR 映射）

| Gemini 字段 | IR 字段 | 备注 |
|-------------|---------|------|
| `contents[].role: "model"` | `Message.Role: "assistant"` | IR 内部用 assistant |
| `systemInstruction` | `Request.System` | 独立结构 |
| `parts[].text` | `ContentBlock.Text` | |
| `parts[].functionCall.args` | `ToolCall.Arguments` | 已 parsed 为 map |
| `parts[].functionResponse.response` | `ToolResult.Content` | |
| `finishReason` | `Response.FinishReason` | 注意大写 |
| `usageMetadata.cachedContentTokenCount` | `Usage.CachedTokens` | |
| `usageMetadata.thoughtsTokenCount` | `Usage.ReasoningTokens` | Gemini 2.5+ |
| `generationConfig.thinkingConfig` | `Request.ThinkingConfig` | 内部表示 |
| `safetySettings` | `Request.SafetySettings` | 原样透传 |

## 与其他协议的关键差异

1. **角色命名**：Gemini 用 `model`，OpenAI/Anthropic 用 `assistant`。IR 必须统一，序列化时还原。
2. **Parts 数组**：所有内容都装在 `parts` 数组里，文本/图片/工具调用混排。这与 OpenAI 的 content array、Anthropic 的 content block 形式上类似但语义不同。
3. **`toolConfig` 而非 `tool_choice`**：Gemini 单独字段，序列化时要从 IR 的 `tool_choice` 转换。
4. **`safetySettings` 体系**：Gemini 独有的安全阈值配置，IR 需要保留但客户端不一定需要。
5. **`citationMetadata`**：Gemini 在响应中提供引用元数据，IR 需要捕获并可在序列化为 OpenAI 时映射为 annotations。
6. **流式 JSON 数组 vs SSE**：默认是 JSON 数组，通过 `alt=sse` 切换。网关需要在解析层抹平差异。
7. **思考内容带 `thought: true` 标记**：与 Anthropic 的 `thinking` block 形式不同，IR 内部统一。
