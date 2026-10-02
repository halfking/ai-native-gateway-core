# 出站序列化流水线（Serialize Pipeline）

## 概述

```
InternalRequest (IR)
    ↓
1. outbound paramguard
    ↓
2. reasonnorm（规范化 reasoning）
    ↓
3. SerializeXxx → HTTP Request body
    ↓
4. HTTP 客户端发送
```

## 步骤详解

### 1. outbound paramguard

按目标厂商方言执行参数守卫：

```go
targetDialect := paramreg.Resolve(cand.CatalogCode, cand.Protocol)
bodyBytes = paramguard.Apply(bodyBytes, targetDialect)
```

守卫规则（按方言）：

| 方言 | 守卫规则 |
|------|----------|
| `DialectAnthropic` | 删除 `parallel_tool_calls`，重命名 `tools[].function.parameters` → `input_schema`，补 `max_tokens` 必填 |
| `DialectDeepSeek` | 删除不支持字段（如 `web_search_options`），保留 `reasoning_effort` |
| `DialectQwen` | 保留 `enable_search` + `search_options`，删除 Anthropic 私有字段 |
| `DialectGLM` | 删除 OpenAI Responses 私有字段，保留 `do_sample` |
| `DialectMiniMax` | 保留 `mask_sensitive_info`、`bot_setting` |
| `DialectOllama` | 流式检查 NDJSON 支持 |

**字段决策树**：

```
paramreg.Decide(field, srcDialect, dstDialect)
  ├── ActionKeep       保留字段
  ├── ActionDrop       删除字段
  ├── ActionRename     重命名（如 parameters → input_schema）
  ├── ActionRestore    当 dst 认识该字段时还原
  └── ActionTranslate  按规则转换值
```

### 2. reasonnorm

规范化 reasoning 内容：

```go
reasonnorm.Normalize(irReq, targetDialect)
```

规则：
- 截断超长 reasoning
- 替换 PII
- 调整 reasoning 顺序（多轮对话中 reasoning 与 content 的位置）

### 3. Serialize 阶段

**入口函数**（位于 `internal/ir/`）：

| 函数 | 协议 |
|------|------|
| `SerializeOpenAI(req *InternalRequest) ([]byte, error)` | OpenAI Chat |
| `SerializeResponses(req *InternalRequest) ([]byte, error)` | OpenAI Responses |
| `SerializeAnthropic(req *InternalRequest) ([]byte, error)` | Anthropic Messages |
| `SerializeGemini(req *InternalRequest) ([]byte, error)` | Gemini generateContent |

**约束**：

1. **按方言序列化**：DeepSeek 序列化时必须包含 `reasoning_effort`，Qwen 序列化时必须包含 `enable_search`。
2. **role 还原**：IR `"assistant"` → Gemini `"model"`。
3. **system 字段位置**：OpenAI 是 system message，Anthropic 是顶层 `system`，Gemini 是 `systemInstruction`。
4. **tools 重命名**：`parameters` → Anthropic `input_schema`，Gemini 包到 `functionDeclarations`。
5. **tool_calls.arguments 序列化**：IR map → 字符串 JSON（OpenAI）/ JSON 对象（Anthropic、Gemini）。
6. **thinking 处理**：
   - Anthropic 序列化时 thinking block 必须带 signature
   - 没有 signature 的 thinking 不能输出为 Anthropic thinking
   - 非 Anthropic 方言序列化时 thinking 丢弃

### 4. 厂商特定补充

序列化后，针对特定厂商还需要补充：

```go
// executor_chat.go 中的 finalizeChatRequestBody
bodyBytes = transformation.Apply(bodyBytes, targetDialect)
bodyBytes = compression.Apply(bodyBytes, ctxWindow)
bodyBytes = context_summarize.Apply(bodyBytes, ...)
```

具体模块：

| 模块 | 职责 |
|------|------|
| `domains/transformation` | 字段名映射、字段补全、字段删除 |
| `domains/streaming/executors` | 压缩、上下文缩减、限流 |
| `internal/reasoncap` | reasoning token 上限控制 |
| `internal/reasonnorm` | reasoning 内容规范化 |

### 5. HTTP Header 补充

按厂商补充 HTTP header：

```go
// Anthropic
req.Header.Set("x-api-key", creds.APIKey)
req.Header.Set("anthropic-version", "2023-06-01")

// OpenAI
req.Header.Set("Authorization", "Bearer "+creds.APIKey)

// Gemini
req.URL.RawQuery = "key=" + creds.APIKey
// 或
req.Header.Set("Authorization", "Bearer "+creds.APIKey)
```

### 6. URL 构造

按厂商构造请求 URL：

```go
// OpenAI
url := "https://api.openai.com/v1/chat/completions"

// Azure OpenAI
url := fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s/chat/completions?api-version=2024-08-01-preview", region, deployment)

// Anthropic
url := "https://api.anthropic.com/v1/messages"

// Gemini
url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
```
