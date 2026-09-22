# 入站解析流水线（Parse Pipeline）

## 概述

网关在收到客户端请求后，按以下顺序执行解析：

```
HTTP Request (raw bytes)
    ↓
1. 协议识别（按 path + Content-Type + Header）
    ↓
2. ParseXxx → InternalRequest (IR)
    ↓
3. inbound paramguard（可选）
    ↓
4. sanitizer（去敏感信息）
    ↓
5. reasoncap（推理 token 上限）
    ↓
6. URSM v2 路由
    ↓
7. 选定 provider candidate
```

## 步骤详解

### 1. 协议识别

依据：

| 路径 | 协议 |
|------|------|
| `/v1/chat/completions` | OpenAI Chat Completions |
| `/v1/responses` | OpenAI Responses |
| `/v1/messages` | Anthropic Messages |
| `/v1beta/models/{model}:generateContent` | Gemini generateContent |

辅助判定：
- Anthropic 通过 `x-api-key` + `anthropic-version` header
- OpenAI 通过 `Authorization: Bearer`
- Gemini 通过 `?key=` query 或 Authorization

### 2. Parse 阶段

**入口函数**（位于 `internal/ir/`）：

| 函数 | 协议 |
|------|------|
| `ParseOpenAI(body []byte) (*InternalRequest, error)` | OpenAI Chat |
| `ParseResponses(body []byte) (*InternalRequest, error)` | OpenAI Responses |
| `ParseAnthropic(body []byte) (*InternalRequest, error)` | Anthropic Messages |
| `ParseGemini(body []byte) (*InternalRequest, error)` | Gemini generateContent |
| `ParseOpenAIStreamChunk(body []byte) (*StreamChunk, error)` | OpenAI 流式 chunk |
| `ParseAnthropicStreamEvent(event []byte) (*StreamChunk, error)` | Anthropic 流式事件 |
| `ParseGeminiStreamChunk(body []byte) (*StreamChunk, error)` | Gemini 流式 chunk |

**约束**：

1. **不丢失原始信息**：所有字段（含未知字段）必须保留。
2. **多模态统一**：把 string content 和 array content 都转为 `[]ContentBlock`。
3. **role 统一**：把 Gemini `"model"` 转为 IR `"assistant"`。
4. **tool_calls.arguments 已 parse**：字符串 JSON 在 IR 内必须解析为 map。
5. **错误响应也要 parse**：HTTP 200 但 body 是错误时（如 MiniMax `base_resp`），必须识别并返回 `UpstreamError`。

### 3. inbound paramguard

入站时按客户端方言执行参数守卫：

```go
inboundDialect := paramreg.DialectForCatalogCode(clientCatalogCode)
paramguard.Apply(reqBytes, inboundDialect)
```

守卫规则：
- 删除客户端协议不支持的字段
- 规范化字段值（如 `temperature: -0.1` → 错误）
- 限制字段大小（如 `max_tokens` 上限）

### 4. sanitizer

去除敏感信息：

- API key
- 用户密码
- 信用卡号
- 自定义敏感字段

### 5. reasoncap

推理 token 上限控制（针对 DeepSeek/Qwen/GLM 等推理模型）：

```go
reasoncap.Apply(irReq, maxReasoningTokens)
```

超出上限时截断 reasoning。

### 6. URSM v2 路由

详见 URSM v2 模块。本步骤不修改 IR，只选择 provider candidate。

### 7. provider candidate

URSM v2 选定后，进入下一步 Serialize 流水线。
