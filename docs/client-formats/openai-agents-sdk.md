# OpenAI Agents SDK 请求格式要求

## 概述

OpenAI Agents SDK 是 OpenAI 推出的 Agent 框架，调用 Responses API（不是 Chat Completions）。

## 适用 SDK

- Python: `openai-agents`
- TypeScript: `@openai/agents`

## 客户端配置

```python
from agents import Agent, Runner
from openai import AsyncOpenAI

client = AsyncOpenAI(
    api_key="any-string",
    base_url="https://gateway.example.com/v1",
)
```

## 入站格式

符合 OpenAI Responses API（见 [openai-responses.md](../vendor-formats/openai-responses.md)）。

`POST /v1/responses`，body 含 `model`、`input`、`instructions`、`tools` 等字段。

## Responses API 关键字段（SDK 强依赖）

| 字段 | SDK 期望 | 缺失后果 |
|------|----------|----------|
| `id: "resp_xxx"` | 必须存在 | SDK 抛错 |
| `object: "response"` | 必填 | SDK 校验失败 |
| `status: "completed"` | 必填，枚举值 | SDK 行为异常 |
| `output[].type: "message"` | 必填 | SDK 解析失败 |
| `output[].content[].type: "output_text"` | 必填 | SDK 解析失败 |
| `usage.input_tokens`/`output_tokens`/`total_tokens` | 必须存在 | 计费异常 |
| 流式事件 `response.output_text.delta` | 必须存在 | SDK 流式 UI 异常 |

## 期望的工具

OpenAI Agents SDK 支持两类工具：

1. **OpenAI 内置工具**：
   - `web_search`
   - `file_search`
   - `code_interpreter`
   - `computer_use_preview`

2. **自定义 function tools**：
   ```json
   {"type": "function", "name": "...", "description": "...", "parameters": {...}}
   ```

## 网关对 OpenAI Agents SDK 的特殊支持

### 场景 1：Agents SDK → OpenAI 模型

直通模式，body 透明转发。

### 场景 2：Agents SDK → Anthropic 模型

1. **ParseResponses**：解析 SDK 请求。
2. **SerializeAnthropic**：
   - `input` 数组拆分为 `system`（来自 `instructions`）和 `messages`
   - `max_output_tokens` → `max_tokens`
   - `tools[].parameters` → `tools[].input_schema`
   - `reasoning.effort` → `thinking.type: "enabled"` + `budget_tokens`（仅部分对应）
3. **ParseAnthropicResponse → SerializeResponsesResponse**：
   - `content[]` → `output[].content[].output_text`
   - `stop_reason: "end_turn"` → `status: "completed"`
   - `usage.input_tokens` → `usage.input_tokens`

### 场景 3：Agents SDK → Gemini

类似场景 2，额外处理：
- `input` → `contents[]`
- `systemInstruction` 独立
- `tools` → `functionDeclarations`

## 不兼容警告

⚠️ **OpenAI Responses API 通过非 OpenAI 厂商调用时，多个内置工具不可用**：

- ❌ `web_search`：仅 OpenAI 支持（其他厂商各自实现，需要客户端切换协议）
- ❌ `file_search`：仅 OpenAI 支持
- ❌ `code_interpreter`：仅 OpenAI 支持
- ❌ `computer_use_preview`：仅 OpenAI 支持
- ✅ function tools：所有厂商支持
- ✅ `previous_response_id`：仅 OpenAI 支持（其他厂商需要客户端手动维护对话历史）

## 网关审计点

1. **内置工具必须检测并降级**：当目标方言不是 OpenAI 时，web_search/file_search/code_interpreter 必须被丢弃并返回警告。
2. **previous_response_id 必须检测并降级**：当目标方言不是 OpenAI 时，需要客户端回退到发完整 `input`。
3. **流式事件类型必须按 Responses 规范**：不能简化为 Chat Completions 的 delta。
