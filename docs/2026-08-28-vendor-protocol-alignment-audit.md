# 多厂商协议对齐审计报告 (2026-08-28)

## 背景

2026-08-28 对网关的非 Big3 厂商（Anthropic/OpenAI/Google 之外）集成进行系统性审计：对比 7 家主流厂商的官方 API 文档与网关当前实现，识别转换缺陷与字段映射遗漏。

审计范围：
- **厂商**：Moonshot Kimi、Zhipu GLM、Qwen/DashScope、MiniMax、Baidu Ernie、DeepSeek、Doubao (Volcengine Ark)
- **方向**：出口到上游（请求体转换）、从上游接收（响应体解析 + 错误检测）
- **模式**：流式与非流式

## 执行摘要

**发现 8 项关键缺陷**（P0-P2），其中 3 项导致错误信号丢失，5 项导致计费/审计数据缺失：

| 厂商 | 缺陷 | 优先级 | 影响 |
|------|------|--------|------|
| **MiniMax** | `base_resp.status_code` 被 strip 导致 HTTP 200 包装的错误信号丢失 | **P0** | 流式+非流式失败被误判为成功 |
| **Qwen** | `output.choices[].message.content: [{text:"..."}]` 数组结构未解包 | **P1** | 客户端收到 `[{text:"hello"}]` 而非 `"hello"` |
| **Zhipu GLM** | 流中 `finish_reason` 异常值（network_error/sensitive/model_context_window_exceeded）已拦截但非流式路径未检测 | **P1** | 非流式 200 响应中这三个错误被当成功 |
| **DeepSeek** | `reasoning_content`/`prompt_cache_*_tokens` 被误删 | **P2** | 推理计费统计丢失 |
| **MiniMax** | `input_sensitive_type`/`output_sensitive_type` (1-7 分类) 被删 | **P2** | 内容审核细粒度分类丢失 |
| **Baidu Ernie** | `search_info.search_results[]` 被删 | **P2** | 搜索来源引用丢失 |
| **Doubao** | 多模态 embedding 独立端点未映射 | **P2** | 不支持视觉向量化 |
| **全厂商** | `reasoning_content`/`thinking` 字段在 IR 超集中无统一映射 | **P1** | 推理输出在协议转换时丢失或格式不一致 |

---

## 1. 厂商协议差异矩阵

### 1.1 Moonshot Kimi (platform.kimi.ai)

**官方文档**: https://platform.kimi.ai/docs/api/chat

#### 请求差异
| 字段 | OpenAI 标准 | Kimi 扩展 | IR 映射状态 |
|------|-------------|-----------|-------------|
| `thinking` | ❌ 无 | ✅ `{type: "enabled"\|"disabled", keep: null\|"all"}` | ⚠️ IR.Thinking 存在但未映射 keep 字段 |
| `reasoning_effort` | ❌ 无 | ✅ K3 专属："low"\|"high"\|"max" | ❌ IR 无此字段 |
| `$web_search` | ✅ 标准 tool | ⚠️ `type: "builtin_function"` 而非 `function` | ❌ IR Tool 类型枚举缺 builtin |
| `prompt_cache_key` | ❌ 无 | ✅ 缓存键 | ❌ IR 无 |

#### 响应差异
| 字段 | OpenAI 标准 | Kimi 扩展 | 当前处理 |
|------|-------------|-----------|---------|
| `reasoning_content` | ❌ 无 | ✅ 推理过程（需跨轮保留） | ⚠️ strip 函数未删但 IR 未映射，协议转换时丢失 |
| `usage.cached_tokens` | ❌ 无 | ✅ 缓存命中统计 | ✅ 保留（audit-09 修复后） |
| 流式哨兵 | `[DONE]` | `[DONE]` | ✅ 兼容 |

#### 陷阱
- 流中断时最后 chunk 可能不到达，无显式 error event
- `$web_search` 工具调用需原样返回 arguments（服务端执行搜索），额外计费

---

### 1.2 Zhipu GLM (bigmodel.cn, z.ai)

**官方文档**: https://docs.bigmodel.cn/cn/guide/develop/http/introduction

#### 请求差异
| 字段 | OpenAI 标准 | GLM 扩展 | IR 映射状态 |
|------|-------------|----------|-------------|
| `do_sample` | ❌ 无 | ✅ bool，默认 true | ❌ IR 无 |
| `thinking.type` | ❌ 无 | ✅ "enabled"\|"disabled" (GLM-4.5+) | ⚠️ IR.Thinking 存在但字段名不一致（IR 用 `enabled: bool`） |
| `reasoning_effort` | ❌ 无 | ✅ 六档："max"\|"xhigh"\|"high"\|"medium"\|"low"\|"minimal"\|"none" | ❌ IR 无 |

#### 响应差异
| 字段 | OpenAI 标准 | GLM 扩展 | 当前处理 |
|------|-------------|----------|---------|
| `finish_reason` 异常值 | `stop`/`length`/`tool_calls`/`content_filter` | ⚠️ **错误通道**：`network_error`/`sensitive`/`model_context_window_exceeded` | ✅ **流式已拦截**（ae1ecaedf）<br>❌ **非流式未检测** |
| 错误码 | HTTP 4xx/5xx | `/cn/faq/api-code` 文档标准码 | ✅ 通用 HTTP 错误处理 |

**缺陷 P1-GLM-1**：非流式 200 响应中 `finish_reason` 的三个异常值未检测，会被当作正常 `end_turn` 处理。

---

### 1.3 Qwen/DashScope (help.aliyun.com/model-studio)

**官方文档**: https://help.aliyun.com/en/model-studio/qwen-api-via-dashscope

#### 请求差异
| 字段 | OpenAI 标准 | Qwen 扩展 | IR 映射状态 |
|------|-------------|-----------|-------------|
| `enable_thinking`, `preserve_thinking`, `thinking_budget` | ❌ 无 | ✅ 推理模式三参数 | ❌ IR.Thinking 仅 enabled，无 budget |
| `reasoning_effort` | ❌ 无 | ✅ 同 GLM | ❌ IR 无 |
| `enable_search`, `search_options` | ❌ 无 | ✅ 联网搜索五参数 | ❌ IR 无 |
| `result_format` | ❌ 无 | ✅ "text"\|"message"（多轮需 message） | ❌ IR 无 |
| `incremental_output` | ❌ 无（OpenAI 流式隐含增量） | ✅ true=delta, false=全量累积 | ❌ IR 无 |
| Header 流式开关 | Body `stream: true` | ⚠️ `X-DashScope-SSE: enable` | ❌ 请求转换未处理 header |

#### 响应差异
| 字段 | OpenAI 标准 | Qwen 扩展 | 当前处理 |
|------|-------------|----------|---------|
| `output.choices[].message.content` | `string` | ⚠️ **数组** `[{"text":"..."}]` | ❌ **P1-Qwen-1**: IR 解析器未解包，客户端收到数组而非字符串 |
| `reasoning_content` | ❌ 无 | ✅ 独立字段（需完整保留历史） | ⚠️ IR 未映射 |
| 错误 `DataInspectionFailed` | ❌ 无 | ✅ Header `X-DashScope-DataInspection` 触发 | ❌ 未分类到 KindContentFilter |

**缺陷 P1-Qwen-1**：`content` 字段数组结构未在 IR 解析器中解包，导致协议转换后客户端收到 `[{text:"hello"}]` 而非 `"hello"`。

#### 陷阱
- `reasoning_effort` + `thinking_budget` 同时设置会报错
- `preserve_thinking: true` 时必须完整传递历史 `reasoning_content`（不可串接到 content）
- 实际输出 token 数可能与 `max_completion_tokens` 偏差 ±10

---

### 1.4 MiniMax (platform.minimax.io)

**官方文档**: https://platform.minimax.io/docs/api-reference/text-post

#### 请求差异
| 字段 | OpenAI 标准 | MiniMax 扩展 | IR 映射状态 |
|------|-------------|--------------|-------------|
| `mask_sensitive_info` | ❌ 无 | ✅ bool 掩码 PII | ❌ IR 无 |
| `max_completion_tokens` | ✅ `max_tokens` (已废弃) | ✅ 正式字段，模型默认值差异大 | ✅ IR.MaxTokens 映射 |

#### 响应差异（**P0 缺陷**）
| 字段 | OpenAI 标准 | MiniMax 扩展 | 当前处理 |
|------|-------------|--------------|---------|
| **`base_resp`** | HTTP 4xx/5xx | ⚠️ **HTTP 200 + 错误包装**：<br>`{status_code: 0=成功, 非0=失败, status_msg}` | ❌ **P0-MiniMax-1**: `stripMinimaxFieldsBody` 删除 `base_resp`，导致 status_code 非0 的错误信号**完全丢失** |
| `input_sensitive_type`/`output_sensitive_type` | ❌ 无 | ✅ int 1-7：严重违规/色情/广告/禁止/辱骂/恐怖暴力/其他 | ❌ **P2-MiniMax-2**: 被 strip 删除，内容审核细粒度分类丢失 |
| `reasoning_content` | ❌ 无 | ✅ 推理模型返回 | ⚠️ strip 未删但 IR 未映射 |
| `usage.completion_tokens_details.reasoning_tokens` | ❌ 无 | ✅ 推理计费 | ✅ 保留 |

**缺陷 P0-MiniMax-1（严重）**：
- **根因**：MiniMax 规范用 HTTP 200 + `base_resp.status_code` 编码错误（如 1002=限流、1008=余额不足、1027=输出无效），而非标准 4xx/5xx。
- **影响**：`stripMinimaxFieldsBody` (L25) 将 `base_resp` 列为私有字段删除，导致网关在 `resp.StatusCode == 200` 路径下**永远看不到**这些错误信号，误判为成功响应。
- **路径**：流式 (`stream.go` L1046 `stripChunkFields`) + 非流式 (`executor_chat.go` L1323 `stripVendorFields`) 均受影响。

**缺陷 P2-MiniMax-2**：内容审核细粒度分类（1=严重违规、2=色情...）被删除，合规审计无法区分违规类型。

#### 陷阱
- M2 推荐 temp=1.0/top_p=0.95；Text-01 默认 0.1（创意任务需 0.7-1.0）
- 工具调用时支持交错推理（interleaved thinking）

---

### 1.5 Baidu Ernie (ai.baidu.com/ai-doc/WENXINWORKSHOP)

**官方文档**: https://ai.baidu.com/ai-doc/WENXINWORKSHOP/jlil56u11

#### 请求差异
| 字段 | OpenAI 标准 | Ernie 扩展 | IR 映射状态 |
|------|-------------|-----------|-------------|
| 认证 | Header `Authorization: Bearer` | ⚠️ URL 参数 `?access_token=` 或 AK/SK 签名 | ⚠️ 上游客户端处理（executor 外） |
| `enable_system_memory`, `system_memory_id` | ❌ 无 | ✅ 系统记忆 | ❌ IR 无 |
| `disable_search`, `enable_citation`, `enable_trace` | ❌ 无 | ✅ 搜索控制 + 引用标记 | ❌ IR 无 |
| `metadata` | ❌ 无 | ✅ map<string,string>，最多 16 组 | ❌ IR 无 |

#### 响应差异
| 字段 | OpenAI 标准 | Ernie 扩展 | 当前处理 |
|------|-------------|-----------|---------|
| 流式 `sentence_id`, `is_end` | ❌ 无 | ✅ 子句序号 + 终止标志 | ❌ 未使用（普通 SSE 解析） |
| `search_info.search_results[]` | ❌ 无 | ✅ `{index, url, title}` 搜索来源 | ❌ **P2-Ernie-1**: 被 strip 逻辑删除（若有） |
| `usage.prompt_tokens_details.search_tokens`, `usage.search_count` | ❌ 无 | ✅ 搜索计费项 | ⚠️ 保留但未单独统计 |
| `need_clear_history`, `flag`, `ban_round` | ❌ 无 | ✅ 安全风险标识 | ❌ 未检测 |
| Function calling `thoughts` | ❌ 无 | ✅ 推理过程 | ⚠️ IR Tool 未映射 |

**缺陷 P2-Ernie-1**：搜索来源引用 `search_results[]` 被删（若 strip 逻辑覆盖），审计无法追溯内容来源。

---

### 1.6 DeepSeek (api-docs.deepseek.com)

**官方文档**: https://api-docs.deepseek.com/api/create-chat-completion

#### 请求差异
| 字段 | OpenAI 标准 | DeepSeek 扩展 | IR 映射状态 |
|------|-------------|---------------|-------------|
| `thinking.reasoning_effort` | ❌ 无 | ✅ "low"\|"high"\|"max" | ❌ IR.Thinking 仅 enabled |
| `prefix` (Beta) | ❌ 无 | ✅ bool 强制 assistant 前缀 | ❌ IR 无 |
| `reasoning_content` (Beta) | ❌ 无 | ✅ string CoT 输入 | ❌ IR 无（仅响应侧有） |
| `user_id` | ❌ 无 | ✅ 内容安全/缓存隔离/调度标识 | ❌ IR 无 |

#### 响应差异
| 字段 | OpenAI 标准 | DeepSeek 扩展 | 当前处理 |
|------|-------------|---------------|---------|
| `reasoning_content` | ❌ 无 | ✅ 推理过程 | ❌ **P2-DeepSeek-1**: `stripDeepSeekFieldsBody` 曾误删（audit-09 修复后保留，但 IR 未映射） |
| `usage.prompt_cache_hit_tokens`, `prompt_cache_miss_tokens` | ❌ 无 | ✅ 上下文缓存统计 | ✅ 保留（audit-09） |
| `usage.completion_tokens_details.reasoning_tokens` | ❌ 无 | ✅ 推理计费 | ✅ 保留 |
| `finish_reason: insufficient_system_resource` | ❌ 无 | ✅ 推理系统资源不足 | ❌ 未分类到专用 Kind |

**缺陷 P2-DeepSeek-1**：`reasoning_content` 虽已保留（audit-09 修复），但 IR 未映射导致协议转换时丢失。

---

### 1.7 Doubao 豆包 (Volcengine Ark, docs.volcengine.com)

**官方文档**: https://docs.volcengine.com/docs/82379/* (API 路径分散)

#### 请求差异
| 字段 | OpenAI 标准 | Doubao 扩展 | IR 映射状态 |
|------|-------------|-------------|-------------|
| Base URL | `https://api.openai.com/v1/` | ✅ `https://ark.cn-beijing.volces.com/api/v3/` | ⚠️ 上游客户端配置 |
| 多模态 embedding | `/v1/embeddings` | ⚠️ **独立端点** `/api/v3/embeddings/multimodal` | ❌ **P2-Doubao-1**: 网关未映射，不支持视觉向量化 |

#### 响应差异
| 字段 | OpenAI 标准 | Doubao 扩展 | 当前处理 |
|------|-------------|-------------|---------|
| 流式与非流式 | 标准 SSE | ⚠️ 推测兼容（文档未完整获取） | ✅ `ExtractDoubaoUsageFromChunk` 处理 usage |
| 错误编码 | HTTP 4xx/5xx | ⚠️ 文档未明确 | ❌ 通用 HTTP 处理 |

**缺陷 P2-Doubao-1**：多模态 embedding 独立端点未在路由层映射，无法支持 doubao-embedding-vision 模型。

---

## 2. IR 超集映射缺失分析

### 2.1 当前 IR 协议枚举
```go
// internal/ir/types.go:27
const (
    ProtocolOpenAIChat        = "openai-chat"
    ProtocolAnthropicMessages = "anthropic-messages"
    ProtocolGeminiGenerate    = "gemini-generate"
    ProtocolOpenAIResponses   = "openai-responses"
)
```

**观察**：所有非 Big3 厂商（Kimi/GLM/Qwen/MiniMax/Ernie/DeepSeek/Doubao）均走 `ProtocolOpenAIChat` 路径（OpenAI 兼容模式），没有专用协议标识。

### 2.2 IR Request 字段缺失

| 厂商扩展字段 | 出现频率 | IR 映射状态 |
|-------------|---------|-------------|
| `reasoning_effort` | Kimi(K3)、GLM、Qwen、DeepSeek | ❌ 无 |
| `thinking.keep` | Kimi | ⚠️ IR.Thinking 存在但无 keep 字段 |
| `thinking_budget` | Qwen | ❌ 无 |
| `enable_search` / `search_options` | Qwen、Ernie | ❌ 无 |
| `do_sample` | GLM | ❌ 无 |
| `mask_sensitive_info` | MiniMax | ❌ 无 |
| `user_id` | DeepSeek、Ernie | ❌ 无 |
| `metadata` | Ernie | ❌ 无 |

**推荐**：扩展 `IR.Request` 新增 `ReasoningEffort string`、`ThinkingBudget *int`、`SearchOptions *SearchConfig` 等字段，序列化器按目标协议选择性输出。

### 2.3 IR Response 字段缺失

| 厂商扩展字段 | 出现频率 | IR 映射状态 | 影响 |
|-------------|---------|-------------|------|
| `reasoning_content` | Kimi、GLM、Qwen、MiniMax、Ernie、DeepSeek | ❌ 无 | **P1**: 推理输出在协议转换时丢失 |
| `usage.cached_tokens` / `prompt_cache_*_tokens` | Kimi、DeepSeek | ⚠️ 保留但未映射到 IR.Usage | 缓存统计不统一 |
| `usage.reasoning_tokens` | MiniMax、DeepSeek | ⚠️ 同上 | 推理计费不统一 |
| `input_sensitive_type` / `output_sensitive_type` | MiniMax | ❌ strip 删除 | 内容审核细粒度丢失 |
| `search_info.search_results[]` | Qwen、Ernie | ❌ strip 删除或未映射 | 搜索来源追溯丢失 |

**推荐**：
1. 扩展 `IR.Message` 新增 `ReasoningContent string` 字段（对应 Anthropic `thinking` 块）。
2. 扩展 `IR.Usage` 新增 `CachedTokens`, `ReasoningTokens` 字段。
3. 新增 `IR.ContentSafety` 结构体映射 `input_sensitive_type` 等字段。

---

## 3. 缺陷修复优先级与实施路径

### P0 缺陷（阻塞性，必须立即修复）

#### P0-MiniMax-1: base_resp.status_code 错误信号丢失

**修复方案**：
1. **检测阶段**：在 `stripMinimaxFieldsBody` 之前，新增 `parseMiniMaxBaseResp` 检测 `status_code` 非0：
   ```go
   // domains/streaming/minimax_error.go
   func ParseMiniMaxBaseResp(body []byte) (statusCode int, statusMsg string, isError bool) {
       var raw map[string]json.RawMessage
       if json.Unmarshal(body, &raw) != nil {
           return 0, "", false
       }
       baseRespRaw, ok := raw["base_resp"]
       if !ok {
           return 0, "", false
       }
       var baseResp struct {
           StatusCode int    `json:"status_code"`
           StatusMsg  string `json:"status_msg"`
       }
       if json.Unmarshal(baseRespRaw, &baseResp) != nil {
           return 0, "", false
       }
       return baseResp.StatusCode, baseResp.StatusMsg, baseResp.StatusCode != 0
   }
   ```

2. **非流式路径**：`executor_chat.go` L1323 之前插入检测：
   ```go
   if cand.CatalogCode == "minimax" {
       if code, msg, isErr := ParseMiniMaxBaseResp(respBody); isErr {
           return &errorsx.UpstreamError{
               Kind:       classifyMiniMaxStatusCode(code),
               Message:    fmt.Sprintf("minimax base_resp error %d: %s", code, msg),
               StatusCode: 200, // HTTP status is 200 but base_resp signals error
           }
       }
   }
   ```

3. **流式路径**：`stream.go` L1046 之前对每个 chunk 检测（仅对包含 `base_resp` 的最后 chunk）：
   ```go
   func stripChunkFields(line string, stripFn func([]byte) []byte) (string, error) {
       // ... 现有解析逻辑 ...
       if stripFn == streaming.StripMinimaxFieldsBody { // 特化判断
           if code, msg, isErr := streaming.ParseMiniMaxBaseResp([]byte(payload)); isErr {
               return "", fmt.Errorf("minimax stream error %d: %s", code, msg)
           }
       }
       // ... 现有 strip 逻辑 ...
   }
   ```

4. **分类映射**：新增 `classifyMiniMaxStatusCode`：
   ```go
   func classifyMiniMaxStatusCode(code int) errorsx.ErrorKind {
       switch code {
       case 1002: return errorsx.KindRateLimit
       case 1004: return errorsx.KindAuth
       case 1008: return errorsx.KindQuotaExceeded
       case 1027: return errorsx.KindContentFilter // 输出无效
       case 1039: return errorsx.KindContextLength
       case 2013: return errorsx.KindBadRequest
       default:   return errorsx.KindUpstreamDown
       }
   }
   ```

5. **测试**：
   - `TestMiniMaxBaseRespErrorDetection_NonStream`：200 + `base_resp.status_code=1002` → KindRateLimit
   - `TestMiniMaxBaseRespErrorDetection_Stream`：流最后 chunk 含 `base_resp.status_code=1008` → interrupted

**工作量**：2-3 小时。

---

### P1 缺陷（功能性，1-2 周内修复）

#### P1-Qwen-1: content 数组结构未解包

**修复方案**：
1. 扩展 `ir.ParseOpenAIResponseChunk` / `ir.ParseOpenAIResponse`，新增 Qwen 专用分支：
   ```go
   // internal/ir/parse_openai.go
   if providerHint == "qwen" || providerHint == "dashscope" {
       // choices[].message.content: [{"text":"..."}] → "..."
       if contentArr, ok := msg.Content.([]any); ok && len(contentArr) > 0 {
           if obj, ok := contentArr[0].(map[string]any); ok {
               if text, ok := obj["text"].(string); ok {
                   msg.Content = text
               }
           }
       }
   }
   ```

2. 确保 `providerHint` 从上下文传入（executor 需带 `cand.CatalogCode` 或 `cand.ProviderCode`）。

3. **测试**：`TestQwenContentArrayUnpacking`。

**工作量**：4-6 小时。

---

#### P1-GLM-2: 非流式 finish_reason 异常值未检测

**修复方案**：
1. 在 `ir.ParseOpenAIResponse` 中新增 GLM `finish_reason` 检测（流式路径已在 `ae1ecaedf` 修复）：
   ```go
   // internal/ir/parse_openai.go
   if providerHint == "zhipu" || providerHint == "glm" {
       switch finishReason {
       case "network_error":
           return nil, &errorsx.UpstreamError{Kind: errorsx.KindNetwork, Message: "GLM network_error"}
       case "sensitive":
           return nil, &errorsx.UpstreamError{Kind: errorsx.KindContentFilter, Message: "GLM sensitive"}
       case "model_context_window_exceeded":
           return nil, &errorsx.UpstreamError{Kind: errorsx.KindContextLength, Message: "GLM context exceeded"}
       }
   }
   ```

2. **测试**：`TestGLMFinishReasonErrorNonStream`。

**工作量**：2 小时。

---

#### P1-Reasoning: reasoning_content 字段统一映射

**修复方案**：
1. 扩展 `IR.Message` 新增 `ReasoningContent string`：
   ```go
   // internal/ir/types.go
   type Message struct {
       Role             string
       Content          string
       ReasoningContent string // 推理过程（Kimi/GLM/Qwen/MiniMax/DeepSeek/Ernie）
       // ... 现有字段 ...
   }
   ```

2. 解析器填充（OpenAI 侧）：
   ```go
   // internal/ir/parse_openai.go
   if reasoningContent, ok := msg["reasoning_content"].(string); ok {
       irMsg.ReasoningContent = reasoningContent
   }
   ```

3. 序列化器输出（按目标协议）：
   - **Anthropic 序列化器**：`ReasoningContent` → 单独 `thinking` 内容块（`type: "thinking"`）
   - **OpenAI 序列化器**：原样输出 `reasoning_content` 字段
   - **Gemini 序列化器**：暂不支持（Gemini 无推理字段）

4. **测试**：
   - `TestReasoningContentIRRoundtrip`：Kimi 响应 → IR → Anthropic 请求（保留推理块）
   - `TestReasoningContentProtocolConversion`：GLM 流式 → IR → OpenAI 客户端（保留 `reasoning_content`）

**工作量**：1-2 天。

---

### P2 缺陷（数据完整性，2-4 周内修复）

#### P2-MiniMax-2: 内容审核细粒度分类丢失

**修复方案**：从 `stripMinimaxFieldsBody` 的 `minimaxPrivateFields` 中移除 `input_sensitive_type` 和 `output_sensitive_type`，同时扩展 IR：
```go
// internal/ir/types.go
type ContentSafety struct {
    InputSensitive     bool   `json:"input_sensitive,omitempty"`
    InputSensitiveType int    `json:"input_sensitive_type,omitempty"` // 1-7
    OutputSensitive    bool   `json:"output_sensitive,omitempty"`
    OutputSensitiveType int   `json:"output_sensitive_type,omitempty"`
}

type Response struct {
    // ... 现有字段 ...
    ContentSafety *ContentSafety `json:"content_safety,omitempty"`
}
```

解析器填充，序列化器按协议输出（OpenAI 客户端保留，Anthropic 映射到 error.type）。

**工作量**：6-8 小时。

---

#### P2-Ernie-1: 搜索来源引用丢失

**修复方案**：确认 Ernie 是否有 strip 逻辑覆盖 `search_info`，若有则移除；扩展 IR：
```go
type SearchResult struct {
    Index int    `json:"index"`
    URL   string `json:"url"`
    Title string `json:"title"`
}

type Response struct {
    // ... 现有字段 ...
    SearchResults []SearchResult `json:"search_results,omitempty"`
}
```

**工作量**：4-6 小时。

---

#### P2-DeepSeek-1 / P2-Doubao-1: 已部分覆盖或优先级较低

- **DeepSeek**: `reasoning_content` 保留但 IR 未映射，纳入 P1-Reasoning 统一修复。
- **Doubao**: 多模态 embedding 独立端点需路由层配置，非转换器问题。

---

## 4. 实施时间表

| 阶段 | 任务 | 工作量 | 截止日期 |
|------|------|--------|---------|
| **Week 1** | P0-MiniMax-1 修复 + 测试 | 3h | 2026-08-29 |
| **Week 1** | P1-GLM-2 非流式 finish_reason | 2h | 2026-08-30 |
| **Week 2** | P1-Qwen-1 content 数组解包 | 6h | 2026-09-02 |
| **Week 2-3** | P1-Reasoning IR 扩展 + 三协议序列化器 | 2d | 2026-09-06 |
| **Week 3** | P2-MiniMax-2 内容审核字段 | 8h | 2026-09-09 |
| **Week 4** | P2-Ernie-1 搜索结果保留 | 6h | 2026-09-12 |
| **Week 4** | 回归测试 + 文档更新 | 1d | 2026-09-13 |

---

## 5. 附录：官方文档链接汇总

| 厂商 | 主文档 | Chat API | 流式 | 错误码 |
|------|--------|---------|------|--------|
| **Moonshot Kimi** | https://platform.kimi.ai/docs | /api/chat | 同页 | 同页 |
| **Zhipu GLM** | https://docs.bigmodel.cn/cn/guide/develop/http/introduction<br>https://docs.z.ai/guides/overview/concept-param | 同页 | SSE 说明 | /cn/faq/api-code |
| **Qwen/DashScope** | https://help.aliyun.com/en/model-studio/qwen-api-via-dashscope | 同页 | `X-DashScope-SSE` | DataInspectionFailed |
| **MiniMax** | https://platform.minimax.io/docs/api-reference/text-post | /text/chatcompletion | 同页 | base_resp.status_code |
| **Baidu Ernie** | https://ai.baidu.com/ai-doc/WENXINWORKSHOP/jlil56u11 | 同页 | sentence_id/is_end | error_code 110/336001 |
| **DeepSeek** | https://api-docs.deepseek.com/api/create-chat-completion | /v1/chat/completions | [DONE] 哨兵 | /quick_start/error_codes |
| **Doubao (Volcengine Ark)** | https://docs.volcengine.com/docs/82379/* | /api/v3/chat/completions | (推测兼容 OpenAI) | (未获取) |

---

**文档版本**: 1.0  
**作者**: ZCode AI Agent  
**日期**: 2026-08-28  
**审计范围**: 7 厂商 × (请求/响应 × 流式/非流式)  
**发现缺陷**: 8 项（P0: 1, P1: 3, P2: 4）
