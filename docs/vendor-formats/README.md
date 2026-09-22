# 原厂格式文档目录

本文档集合面向网关对外发起请求的场景：每接入一个新的 LLM 厂商，网关必须按原厂的 HTTP/JSON 形态（路径、Header、Body、字段名、字段类型、流式事件名等）原样构造请求，而不是把 OpenAI 的格式强加给所有厂商。

## 协议与方言层级

网关采用 **IR（Internal Representation）+ Dialect（方言）** 的两层结构：

1. **协议层（base protocol）**：HTTP 形状 + JSON 顶层 schema，对应每个原厂的官方 SDK 调用的形态。
2. **方言层（dialect）**：在某个基础协议之上的厂商私有扩展字段、错误码映射、流式事件命名差异。

```
IR（中性内部表示）── ParseOpenAI ──→ IR
       │
       ├── SerializeOpenAIChat ───→ OpenAI Chat Completions（base protocol）
       │       ├─ DialectDeepSeek ─→ DeepSeek 私有字段（reasoning_content 等）
       │       ├─ DialectQwen ─────→ Qwen 私有字段（enable_search 等）
       │       ├─ DialectGLM ──────→ GLM 私有字段（do_sample 等）
       │       ├─ DialectMiniMax ──→ MiniMax 私有字段（mask_sensitive_info 等）
       │       ├─ DialectKimi ─────→ Kimi 私有字段（thinking.keep 等）
       │       ├─ DialectArk ──────→ 豆包/Ark 私有字段
       │       ├─ DialectGrok ─────→ Grok 私有字段（x_search 等）
       │       ├─ DialectMistral ──→ Mistral 私有字段
       │       ├─ DialectOpenRouter→ OpenRouter 私有字段（provider 路由等）
       │       ├─ DialectVLLM ─────→ vLLM 私有字段
       │       └─ DialectOllama ────→ Ollama 私有字段
       │
       ├── SerializeAnthropic ───→ Anthropic Messages（base protocol）
       ├── SerializeResponses ───→ OpenAI Responses（base protocol）
       └── SerializeGemini ──────→ Gemini generateContent（base protocol）
```

每种方言都基于某个 base protocol：见 `internal/paramreg/dialect.go` 的 `parentProtocol` 表。

## 文件清单

| 文件 | 内容 |
|------|------|
| [openai.md](openai.md) | OpenAI Chat Completions API 完整规范 |
| [openai-responses.md](openai-responses.md) | OpenAI Responses API 规范（用于 gpt-5/o-series） |
| [anthropic.md](anthropic.md) | Anthropic Messages API 完整规范 |
| [gemini.md](gemini.md) | Google Gemini generateContent API 规范 |
| [deepseek.md](deepseek.md) | DeepSeek 方言规范（基于 OpenAI 兼容模式） |
| [qwen.md](qwen.md) | 通义千问/DashScope 方言规范 |
| [glm.md](glm.md) | 智谱 GLM 方言规范 |
| [minimax.md](minimax.md) | MiniMax 方言规范 |
| [kimi.md](kimi.md) | 月之暗面 Kimi 方言规范 |
| [ark-doubao.md](ark-doubao.md) | 火山引擎 Ark / 豆包方言规范 |
| [grok.md](grok.md) | xAI Grok 方言规范 |
| [mistral.md](mistral.md) | Mistral AI 方言规范 |
| [openrouter.md](openrouter.md) | OpenRouter 方言规范 |
| [vllm.md](vllm.md) | vLLM 自托管方言规范 |
| [ollama.md](ollama.md) | Ollama 自托管方言规范 |

## 协议摘要

| Provider | Base protocol | Path | Stream header | Auth |
|----------|---------------|------|---------------|------|
| OpenAI | `openai-chat` / `openai-responses` | `/v1/chat/completions` 或 `/v1/responses` | `text/event-stream` | `Authorization: Bearer` |
| Azure OpenAI | `openai-chat` | 部署路径 | `text/event-stream` | `api-key` header |
| Anthropic | `anthropic-messages` | `/v1/messages` | `text/event-stream` | `x-api-key` + `anthropic-version` |
| Google Gemini | `gemini-generate` | `/v1beta/models/{model}:generateContent` 或 `:streamGenerateContent` | JSON 数组 / SSE | `?key=` query 或 Bearer |
| DeepSeek | `openai-chat` | `/v1/chat/completions` | SSE | Bearer |
| Qwen (DashScope) | `openai-chat` 兼容 + 私有字段 | `/compatible-mode/v1/chat/completions` | SSE | Bearer |
| GLM (智谱) | `openai-chat` 兼容 + 私有字段 | `/api/paas/v4/chat/completions` | SSE | Bearer |
| MiniMax | `openai-chat` 兼容 + 私有字段 | `/v1/text/chatcompletion_v2` | SSE | Bearer |
| Kimi (Moonshot) | `openai-chat` 兼容 + 私有字段 | `/v1/chat/completions` | SSE | Bearer |
| 豆包 Ark | `openai-chat` 兼容 + 私有字段 | `/api/v3/chat/completions` | SSE | Bearer |
| xAI Grok | `openai-chat` 兼容 + 私有字段 | `/v1/chat/completions` | SSE | Bearer |
| Mistral | `openai-chat` 兼容 + 私有字段 | `/v1/chat/completions` | SSE | Bearer |
| OpenRouter | `openai-chat` 兼容 + 私有字段 | `/api/v1/chat/completions` | SSE | Bearer |
| vLLM | `openai-chat` 兼容 + 私有字段 | `/v1/chat/completions` | SSE | Bearer |
| Ollama | `openai-chat` 兼容 + 私有字段 | `/api/chat` | NDJSON | 无需认证 |

## 共同约束（所有厂商必须遵守）

1. **流式响应**：客户端通过 `stream: true` 触发；上游按 chunk 发送，**网关不可整段缓存后再下发**，否则会破坏交互体验。
2. **错误码**：每个厂商都有自己的错误码体系（HTTP 4xx/5xx + 业务错误码），网关需要分类映射到 IR 层定义的统一错误。
3. **Token 计费**：每个厂商的 `usage` 字段位置/字段名不同，需要标准化到 `IR.Usage`。
4. **Tool 调用**：OpenAI/Anthropic/Gemini 三大体系的 tool 定义、调用消息结构差异很大，IR 需要在解析时抹平差异。
5. **Reasoning/Thinking**：不同厂商的"推理"内容位置/格式不同（OpenAI `reasoning_content`、Anthropic `thinking` block、Gemini `thought`），需要在 IR 层统一。
