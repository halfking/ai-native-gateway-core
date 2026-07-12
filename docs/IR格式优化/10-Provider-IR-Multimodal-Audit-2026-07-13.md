# Audit-10: Provider IR 多模态与计费完整性审计

**审计日期**: 2026-07-13
**审计人**: AI Agent
**范围**: OpenAI / Anthropic / Gemini / GLM / MiniMax / DeepSeek / Ollama / Qwen / **Doubao** 九个原厂的请求与响应 IR 转换、多模态支持、usage 提取、计费链路
**前置基线**: `a6edd018f` (audit-10 修复 Extensions / detail / system_fingerprint 等)
**更新**: 2026-07-13 追加 Doubao 章节（§2.9），与火山方舟 Ark 与 volcengine-coding 聚合端点

---

## 1. 执行摘要

经过对 `internal/ir/`、`domains/streaming/usage.go`、`maas/service.go`、`domains/transformation/sanitizer.go` 以及官方公开资料的逐项核对，得出以下结论：

| 维度 | 当前状态 | 风险等级 |
|------|----------|----------|
| 文本请求/响应（Chat / Messages） | 基本可用但有遗漏（详见 §2.1） | 中 |
| 工具调用（tool_calls / tool_use） | Chat↔Messages 可逆；MiniMax tool_call_id 已修复 | 低 |
| 文本流式（SSE） | Chat 与 Anthropic 已支持；Ollama NDJSON / Gemini SSE 未实现 | 中 |
| 文本 usage | 仅 4 类 token（input/output/cache_read/cache_write） | 中 |
| 多模态 IR | 仅 `image` 一类；audio / video / document / file 全部缺失 | **高** |
| 多模态 usage | 完全缺失：reasoning/audio/image/video/document 提取器不存在 | **高** |
| 多模态计费 | MaAS 计费仅 4 类 token × 单价；无 image/audio/second/document 维度 | **高** |
| Gemini 原生 adapter | 未实现，强制走 OpenAI 兼容层；`inlineData/fileData` 丢失 | **高** |
| Ollama 原生 adapter | 未实现；`eval_count/duration` 计入 telemetry 而非 cost | **高** |
| capability 路由 | `models_canonical.modality` 单值枚举，无法表达 image+audio | 中 |
| Reasoning / Thinking | Chat `reasoning_content` 已解析；Anthropic `signature` 已保留 | 低 |
| Responses API | 非流已支持（SerializeResponsesResponse）；原生 Parse 缺失 | 中 |
| Doubao / 火山方舟 | OpenAI 兼容文本链路可复用；原生多模态、Responses、文件、缓存和 usage 细分未完成 | **高** |

**总体判断**：当前 IR 是"文本 + 图像 + 工具 + 推理"的子集，不是真正的全模态协议抽象。要支持多模态计费和多厂商兼容，必须在 IR 类型层、usage 提取层、计费 schema 层同时演进，且需要原生 Gemini/Ollama adapter。

---

## 2. 厂商逐项审计

### 2.1 OpenAI Chat Completions / Responses

#### 2.1.1 当前 IR 覆盖

- **请求解析** `internal/ir/parse_openai.go`:
  - ✅ text、image_url（detail 字段已保留，audit-10 修复）
  - ✅ tool_calls、tool_choice、response_format、stop、logprobs、seed
  - ✅ Extensions（unknown 顶层字段提取）
  - ❌ `input_audio`（OpenAI Chat 输入音频）
  - ❌ `input_file` / `input_image`（OpenAI Responses 输入文件 / 图片 item）
  - ❌ `modalities` / `audio`（输出模态配置）
  - ❌ `previous_response_id` / `prompt_cache_key` / `safety_identifier`（OpenAI Responses 关联字段）

- **请求序列化** `internal/ir/serialize_openai.go`:
  - ✅ 文本/image/tool 全链路
  - ❌ input_audio、modalities、prompt_cache_key、safety_identifier、previous_response_id 无对应处理

- **响应解析** `internal/ir/response.go::ParseOpenAIResponse`:
  - ✅ text / tool_calls / usage(prompt/completion/total)
  - ✅ `reasoning_content`（已识别为 ReasoningContent）
  - ❌ `output_audio`（OpenAI 输出音频对象）
  - ❌ `refusal`（OpenAI 安全拒绝内容）

- **Responses API 原生**（`response.go::SerializeResponsesResponse`）:
  - ✅ 非流响应序列化（Phase E, 2026-07-01）
  - ❌ Responses 原生 Parse（`input[]` items 仍走 Chat 降级，见 `01-现状审计与修正.md` §2.2）
  - ❌ Responses 流式（typed SSE events：response.output_item.added / .delta / .done）

- **流式** `internal/ir/stream.go`:
  - ✅ OpenAI Chat SSE chunk
  - ❌ OpenAI Responses typed events

#### 2.1.2 官方资料核对（https://platform.openai.com/docs）

| 字段 | OpenAI 文档 | 当前 IR |
|------|------------|---------|
| `input_audio` (input) | Chat completions 支持 base64 音频 / wav / mp3 | ❌ 未解析 |
| `modalities` (output) | `["text","audio"]` | ❌ 未解析 |
| `audio` (output config) | voice / format / speed | ❌ 未解析 |
| `output_audio` (response) | base64 音频块 + transcript | ❌ 未解析 |
| `refusal` (response message) | 安全拒绝字符串 | ❌ 未序列化 |
| `prompt_cache_key` | cache key for routing | ❌ |
| `safety_identifier` | abuse tracking | ❌ |
| `previous_response_id` | Responses 链式 | ❌ |
| `truncation` | "auto" / "disabled" | ❌ |
| `parallel_tool_calls` | boolean | ❌ |
| `prediction` | predicted output (latency) | ❌ |
| `verbosity` | low/medium/high | ❌ |
| `web_search_options` | search context size | ❌ |

#### 2.1.3 关键风险

- **input_audio 完全丢失**：客户端送 chat audio → IR 不解析 → 上游缺失 audio 字段 → 模型按纯文本回答（bug）。代码位置：`internal/ir/parse_openai.go:213` switch 只处理 text/image_url。
- **输出 audio 无表达**：即使上游返回 `output_audio`，`response.go::parseOpenAIResponseContentBlock` 不识别 → IR 丢失输出音频，客户端拿到的 response 没有 audio 字段。
- **prompt_cache_key / safety_identifier 丢失**：与 OpenAI cache billing 关联，丢失影响 cache 命中与计费。

### 2.2 Anthropic Messages

#### 2.2.1 当前 IR 覆盖

- **请求解析** `internal/ir/parse_anthropic.go`:
  - ✅ text、image（base64/url）、tool_use、tool_result、thinking、redacted_thinking、cache_control
  - ✅ system prompt（string / array of blocks / PDF documents）
  - ✅ thinking + signature、metadata.user_id、documents、top_k
  - ❌ PDF 在 system 之外的 message content（Anthropic 支持 image/document/PDF 都在 message.content）

- **请求序列化** `internal/ir/serialize_anthropic.go`:
  - ✅ MiniMax tool_call_id 变体已处理（`targetProvider == "minimax"`）
  - ✅ tool_use 序列化、tool_result 序列化、thinking signature
  - ❌ Anthropic `container_upload` / `container.skills`（Claude 4.5 container 工具）
  - ❌ Anthropic `mcp_servers` / `context_management`（MCP 集成）

- **响应解析** `internal/ir/response.go::ParseAnthropicResponse`:
  - ✅ text、tool_use、thinking + signature
  - ❌ Anthropic `web_search_tool_result`（web 搜索结果块）
  - ❌ Anthropic `code_execution_tool_result`（code execution 结果）
  - ❌ Anthropic `container_upload` 引用（response 侧）

- **流式**:
  - ✅ Anthropic SSE（message_start / content_block_start / content_block_delta / message_delta / message_stop）
  - ❌ 流式 usage 累加规则：当前 stream.go 只在 message_delta 累加 output_tokens，cache_read/cache_creation 可能未累加

#### 2.2.2 官方资料核对（https://docs.anthropic.com/en/api/messages）

| 字段 | Anthropic 文档 | 当前 IR |
|------|---------------|---------|
| `image` (base64/url) | message content image block | ✅ 已实现 |
| `document` (PDF / text) | message content document block | ⚠️ 仅在 system 数组下支持，message content 下 document block 会被丢弃（见 `parseAnthropicContentBlock` default 分支） |
| `tool_use` / `tool_result` | 标准 | ✅ |
| `thinking` + signature | 标准 | ✅（audit-10 修复 PR-2） |
| `redacted_thinking` | 标准 | ✅ |
| `cache_control` (per block) | ephemeral | ✅ |
| `mcp_servers` | Claude 4.5 MCP | ❌ 未解析 |
| `context_management` | edit/clear tools | ❌ 未解析 |
| `container` (uploaded file refs) | container uploads | ❌ 未解析 |
| `web_search_20250305` tool | server-side tool | ❌ |
| `code_execution_20250522` tool | server-side tool | ❌ |
| `betas` header | preview features | ❌ |
| `container.skills` / `container.command` | container tools | ❌ |

#### 2.2.3 关键风险

- **message content 下的 document block 丢失**：客户端送 `[{type:"document", source:{type:"base64", media_type:"application/pdf", data:...}}]` 进 message.content 时，IR 走到 `parseAnthropicContentBlock` 的 default 分支，被存为 RawContent 但 `ContentBlock.Type != "document"`，序列化回 Anthropic 时丢失。代码位置：`internal/ir/parse_anthropic.go:351-403` 仅识别 text/tool_use/tool_result/image/thinking/redacted_thinking。
- **server-side tools 全部丢失**：Anthropic 提供的 web_search / code_execution / computer_use 等"官方工具"被 IR 降级为未知字段，未走 tool_use 路径，可能在 Extensions bag 中被丢弃。
- **MCP 集成未支持**：Claude 4.5 引入的 `mcp_servers` 配置无法通过 IR 透传。

### 2.3 Google Gemini (generateContent)

#### 2.3.1 当前 IR 覆盖

- **请求解析**：❌ 完全未实现。`internal/providercap/capability.go:39` 对 `protocol == "gemini-generate"` 仍走默认 OpenAI 路径。
- **请求序列化**：❌ 完全未实现。
- **响应解析**：❌ 未实现。`ParseAnthropicResponse` / `ParseOpenAIResponse` 不识别 `candidates[]` / `usageMetadata` / `modelVersion`。
- **流式**：`streamGenerateContent?alt=sse` 未实现。
- **现状**：所有走 Gemini 的流量被强制以 OpenAI Chat compatibility 形式转发，导致 `inlineData`、`fileData`、`functionCall`、`thought` 等 native 字段无法表达。

#### 2.3.2 官方资料核对（https://ai.google.dev/api/generate-content）

| 字段 | Gemini 文档 | 当前 IR |
|------|------------|---------|
| `contents[].parts[]` | 多模态 parts | ❌ |
| `systemInstruction.parts` | text parts | ❌ |
| `inlineData` (base64) | `{mimeType, data}` | ❌ |
| `fileData` | `{mimeType, fileUri}` | ❌ |
| `functionCall` / `functionResponse` | tool call / result | ❌ |
| `generationConfig` | temperature, topP, topK, maxOutputTokens, responseMimeType, responseSchema | ❌ |
| `safetySettings` | per category thresholds | ❌ |
| `tools[].functionDeclarations` | JSON Schema tools | ❌ |
| `toolConfig` | ANY / NONE / function name | ❌ |
| `cachedContent` | context caching reference | ❌ |
| `usageMetadata` | promptTokenCount / candidatesTokenCount / totalTokenCount / cachedContentTokenCount / thoughtsTokenCount / toolUsePromptTokenCount / `promptTokensDetails[].modality` / `candidatesTokensDetails[].modality` | ❌ |
| `thought` (thinking) | reasoning parts | ❌ |
| Streaming `GenerateContentResponse` | per-chunk candidates + usageMetadata | ❌ |
| `responseModalities` | TEXT / IMAGE / AUDIO | ❌ |

#### 2.3.3 关键风险

- **Gemini 多模态完全丢失**：客户端通过 OpenAI 兼容层送 image_url → Gemini 上游收到的是 `{"type":"image_url"}` 形式，但 Gemini 原生是 `inline_data`，请求被拒或忽略图片（取决于 SDK）。
- **Gemini 原生计费维度缺失**：`usageMetadata.promptTokensDetails[].modality`（`IMAGE`/`VIDEO`/`AUDIO`）和 `cachedContentTokenCount` 无法被 IR 提取，导致 cache 多模态 token 计费漏掉。
- **thoughtsTokenCount 缺失**：Gemini 2.5+ thinking mode 单独计费，IR 没有 reasoning metric。

### 2.4 DeepSeek (OpenAI 兼容)

#### 2.4.1 当前 IR 覆盖

- **请求解析**：✅ 通过 OpenAI Chat 兼容；DeepSeek 私有字段如 `thinking`、`reasoning_effort` 落入 Extensions bag。
- **请求序列化**：✅ Extensions 恢复（audit-10 修复 stripper）。
- **响应解析**：⚠️ `response.go::ParseOpenAIResponse` 只识别标准 OpenAI 字段。DeepSeek 返回的 `usage.prompt_cache_hit_tokens` / `usage.prompt_cache_miss_tokens` 走标准 cache token 字段，audit-10 已修复。
- **流式**：✅ OpenAI SSE。
- **Anthropic 兼容**：❌ DeepSeek 也提供 Anthropic Messages 兼容入口（https://api-docs.deepseek.com/guides/anthropic_api），未做专门适配。

#### 2.4.2 官方资料核对（https://api-docs.deepseek.com）

| 字段 | DeepSeek 文档 | 当前 IR |
|------|--------------|---------|
| `model` (deepseek-chat / deepseek-reasoner) | 标准 | ✅ |
| `messages` / `tools` / `tool_choice` | OpenAI 兼容 | ✅ |
| `stream` / `stream_options` | OpenAI 兼容 | ✅ |
| `response_format` (`json_schema`) | OpenAI 兼容 | ✅ |
| `thinking` (reasoner) | type=budget_tokens, reasoning_effort? | ⚠️ 走 Extensions，未结构化 |
| `frequency_penalty` etc | 标准 OpenAI | ✅ |
| `usage.prompt_tokens` / `completion_tokens` / `total_tokens` | 标准 | ✅ |
| `usage.prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` | cache billing 关键 | ✅（audit-10 修复） |
| `usage.completion_tokens_details.reasoning_tokens` | R1 reasoning 计费 | ⚠️ 提取但无对应 IR / 计量字段（见 `usage.go` 只有 4 字段） |
| `usage.completion_tokens_details` (image/audio?) | 暂无 | n/a |
| `system_fingerprint` | OpenAI 1.0+ | ✅（audit-10 修复） |
| `reasoning_content` (R1) | 推理内容 | ✅（已识别为 ReasoningContent） |
| Anthropic compatibility | messages API | ❌ 未实现 |

#### 2.4.3 关键风险

- **reasoning_tokens 计费缺失**：DeepSeek R1 在 `usage.completion_tokens_details.reasoning_tokens` 中单独计费，但 `UsageData` 没有 `ReasoningTokens` 字段，MaAS 按 0 计费（漏掉 R1 计费）。
- **cache_hit/miss 维度独立**：DeepSeek `prompt_cache_hit_tokens` 是缓存命中部分（优惠价），`prompt_cache_miss_tokens` 是未缓存部分（全价）。当前 `CacheReadTokens` 把 hit 计入 cache_read，但 miss 应计入 prompt_tokens。`usage.go:62` 只在 `cached_tokens` / `cache_read` 等字段里取值，DeepSeek 的 hit/miss 字段未读取。
- **Anthropic 兼容层未支持**：客户端走 Anthropic SDK 但上游是 DeepSeek Anthropic 兼容入口时，IR 处理按 OpenAI 处理，丢失 Anthropic 特有字段。

### 2.5 GLM (智谱 / Zhipu OpenAI 兼容)

#### 2.5.1 当前 IR 覆盖

- **请求解析**：✅ OpenAI Chat 兼容；GLM 私有字段（`thinking`、`web_search`、`tool_choice` 变体）走 Extensions。
- **请求序列化**：✅ Extensions 恢复（audit-10 修复）。
- **响应解析**：⚠️ 标准字段已识别；GLM 特有的 `usage.prompt_tokens_details.cached_tokens` 走 `prompt_tokens_details.cached_tokens` 路径，`usage.go:74` 已处理。
- **GLM-4V 视觉模型**：⚠️ IR 已知支持 image，但 GLM-4V 对 image 尺寸/格式限制未校验。
- **GLM-4-Audio**：❌ GLM-4-Audio 支持音频输入输出，IR 无 audio 字段，兼容层只能传文本。

#### 2.5.2 官方资料核对（https://open.bigmodel.cn/dev/api）

| 字段 | GLM 文档 | 当前 IR |
|------|----------|---------|
| `model` (glm-4 / glm-4v / glm-4-9b 等) | 标准 | ✅ |
| `messages` / `tools` | OpenAI 兼容 | ✅ |
| `thinking.type` / `reasoning_effort` | 推理模式 | ⚠️ Extensions，未结构化 |
| `web_search` (enable / search_query) | 联网搜索 | ⚠️ Extensions |
| `response_format` | OpenAI 兼容 | ✅ |
| `tools` (code_interpreter / web_search / drawing_tool / retrieval) | 工具类型 | ⚠️ Extensions |
| `do_sample` / `top_p` / `temperature` | 标准 | ✅ |
| `user_id` | abuse tracking | ⚠️ Extensions |
| `request_id` (response) | 响应回传 | ✅（audit-10 修复） |
| `usage.prompt_tokens` / `completion_tokens` / `total_tokens` | 标准 | ✅ |
| `usage.prompt_tokens_details.cached_tokens` | cache | ✅ |
| `usage.completion_tokens_details.reasoning_tokens` | reasoning | ⚠️ 提取但无 IR 字段 |
| `system_fingerprint` | OpenAI 1.0+ | ✅（audit-10 修复） |
| `web_search` result blocks | 多模态搜索结果 | ❌ |
| `drawing_tool` (image generation) | 图片生成 | ❌ |
| Audio (GLM-4-Voice / GLM-4-Audio) | input/output audio | ❌ |
| `image_url` (vision model) | OpenAI 兼容 | ✅（仅 image） |

#### 2.5.3 关键风险

- **reasoning_tokens 同样缺失**：GLM-4.6 / GLM-Z1 的 reasoning 计费字段与 DeepSeek R1 同结构，IR 没有字段。
- **web_search 工具结果为多模态**：GLM web_search 返回结果包含图片/文本混合，目前 IR 未结构化处理。
- **drawing_tool 输出图片**：GLM-4-Plus 支持图像生成，输出 `output_image` 字段，IR 未解析。
- **GLM-4-Audio 模态**：input/output audio 完全未实现。

### 2.6 MiniMax (Anthropic 兼容 + OpenAI 兼容 + 原生)

#### 2.6.1 当前 IR 覆盖

- **请求解析**：✅ Anthropic 兼容（含 tool_call_id 字段变体，audit-10 修复）。
- **请求序列化**：✅ MiniMax `tool_call_id` 变体在 `serialize_anthropic.go:178,339` 已分支处理。
- **响应解析**：⚠️ 走 Anthropic 兼容层，未实现 MiniMax 原生 `base_resp` / `nvext` / `reasoning_split` 字段解析。
- **流式**：✅ Anthropic SSE。

#### 2.6.2 官方资料核对（https://platform.minimax.io/docs）

| 字段 | MiniMax 文档 | 当前 IR |
|------|--------------|---------|
| `model` (abab / MiniMax-M3) | 标准 | ✅ |
| `messages` / `tools` (Anthropic 兼容) | Anthropic Messages | ✅ |
| `tool_result.tool_call_id` | MiniMax 变体（替代 tool_use_id） | ✅（audit-10 修复） |
| `reasoning_split` | 是否分离 reasoning 与 answer | ⚠️ Extensions |
| `reasoning_details` | reasoning token 列表 | ⚠️ Extensions |
| `audio_content` (MiniMax-1.5/2/3 voice models) | input/output audio | ❌ |
| `video_content` (MiniMax-1.5/2/3 video models) | input/output video | ❌ |
| `image_understanding` | vision input | ✅（image） |
| `nvext` (native extension) | provider-private parameters | ⚠️ Extensions |
| `base_resp` (response wrapper) | status_code / status_msg | ⚠️ Extensions |
| `service_tier` | OpenAI 1.0+ 字段 | ✅（audit-10 修复） |
| `usage.reasoning_tokens` (M3) | reasoning 计费 | ⚠️ 提取无 IR 字段 |
| `usage.cache_read_tokens` | cache billing | ✅ |
| `tool_choice` (`required` / `specific`) | tool 强制 | ✅ |
| Thinking (`thinking.type = enabled`) | reasoning | ✅ |

#### 2.6.3 关键风险

- **audio / video 模态缺失**：MiniMax-1.5/2/3 voice / video 模型支持音视频对话，但 IR 无 audio/video 内容块，`parse_anthropic.go` 收到 audio_content 时会走 default 分支。
- **reasoning_split 未结构化**：M3 reasoner 的 `reasoning_split` 字段决定 reasoning 是否单独计费，IR 无法区分两种计费路径。
- **base_resp 状态码未捕获**：MiniMax 返回 `base_resp.status_code != 0` 表示业务错误（非 HTTP 错误），当前解析层不读 base_resp，错误码语义丢失。

### 2.7 Ollama (`/api/chat` + `/api/generate`)

#### 2.7.1 当前 IR 覆盖

- **请求解析**：❌ `/api/chat` 与 `/api/generate` 字段（`format` / `options` / `keep_alive` / `think`）未结构化，仅能走 OpenAI 兼容层。
- **请求序列化**：❌ 原生字段未生成。
- **响应解析**：⚠️ 走 OpenAI 兼容层；`prompt_eval_count` / `eval_count` / `total_duration` / `load_duration` / `prompt_eval_duration` / `eval_duration` 字段被 `ParseOpenAIResponse` 忽略。
- **流式**：❌ Ollama NDJSON（每行一个 JSON 对象，`message` / `done` / `done_reason`）未实现，走 SSE 模拟层。

#### 2.7.2 官方资料核对（https://docs.ollama.com/api/chat）

| 字段 | Ollama 文档 | 当前 IR |
|------|------------|---------|
| `model` | 标准 | ✅ |
| `messages` | `[{"role":"user","content":"...","images":[base64]}]` | ⚠️ images 数组未识别 |
| `tools` | function calling | ✅（OpenAI 兼容层） |
| `format` | json / json schema | ⚠️ Extensions |
| `options` | num_ctx / temperature / top_p / top_k / seed 等 | ⚠️ Extensions |
| `stream` | boolean | ✅ |
| `keep_alive` | 模型保活时间 | ⚠️ Extensions |
| `think` (Ollama 0.5+) | enable thinking | ⚠️ Extensions |
| Response `message.content` | 文本 | ✅ |
| Response `message.thinking` | 推理内容 | ⚠️ Extensions，未识别 |
| Response `message.tool_calls` | tool call | ✅ |
| Response `message.images` (output) | 输出图片 | ❌ |
| Response `done_reason` | "stop" / "length" / "load" | ⚠️ Extensions |
| Response `prompt_eval_count` / `eval_count` | 输入 / 输出 token | ⚠️ 走 OpenAI usage 字段读取失败（Ollama 字段名不同） |
| Response `total_duration` / `load_duration` / `prompt_eval_duration` / `eval_duration` | 性能 telemetry | ❌ |
| `/api/generate` `prompt` / `suffix` / `raw` / `template` / `context` | generate 专用 | ❌ |

#### 2.7.3 关键风险

- **NDJSON 帧解析缺失**：Ollama 流式是 NDJSON（每行一个 JSON），当前 stream 框架假设 SSE；`done` 事件含 usage 与 done_reason，未被 IR 提取。
- **Ollama usage 字段不匹配**：`prompt_eval_count` / `eval_count` 与 OpenAI `prompt_tokens` / `completion_tokens` 不同名，`ParseOpenAIResponse` 不识别 → usage 永远为 0。
- **本地部署计费语义不同**：Ollama 是本地 GPU，billing 不是 per_token 而是 per-GPU-second。当前 MaAS 按 token 计费，对 Ollama 来说语义错误。需要 deployment-scoped provider rate card（见 §3.4）。
- **thinking 字段未识别**：Ollama 0.5+ thinking mode 的 `message.thinking` 未走 IR ReasoningContent 路径。

### 2.8 Qwen / DashScope (OpenAI 兼容 + 原生)

#### 2.8.1 当前 IR 覆盖

- **请求解析**：✅ OpenAI Chat 兼容；Qwen/DashScope 原生端点（`/api/v1/services/aigc/{model}/...`）未实现。
- **请求序列化**：✅ Extensions 恢复。
- **响应解析**：⚠️ 标准 OpenAI 字段识别；DashScope 原生 `usage.input_tokens` / `output_tokens` 字段名与 OpenAI 不同。
- **流式**：✅ OpenAI SSE。
- **多模态模型**（qwen-vl / qwen2-vl / qwen-audio）：⚠️ VL 系列用 image_url 已支持；audio 系列无 audio 字段。

#### 2.8.2 官方资料核对（https://help.aliyun.com/zh/model-studio）

| 字段 | Qwen/DashScope 文档 | 当前 IR |
|------|--------------------|---------|
| `model` (qwen-plus / qwen-vl-max / qwen-audio 等) | 标准 | ✅ |
| `messages` / `tools` | OpenAI 兼容 | ✅ |
| `vl_high_resolution_images` (qwen-vl-max) | 视觉细节 | ⚠️ Extensions |
| `audio_url` (qwen-audio) | 音频输入 | ❌ |
| `video_url` (qwen-vl-video) | 视频输入 | ❌ |
| `file_url` (DashScope 文件) | 文件输入 | ❌ |
| `response_format` | OpenAI 兼容 | ✅ |
| `incremental_output` | DashScope 流式开关 | ⚠️ Extensions |
| `result_format` | message / json | ⚠️ Extensions |
| DashScope 原生 usage 字段 | `input_tokens` / `output_tokens` / `image_tokens` / `audio_tokens` | ❌ 未识别 |
| Qwen3-VL `image_tokens` 字段 | 视觉 token 计费 | ❌ |

#### 2.8.3 关键风险

- **qwen-audio 完全不支持**：audio_url 是 Qwen 特有字段，IR 无 audio 内容块。
- **qwen-vl-video 完全不支持**：video_url 同样未识别。
- **Qwen VL image_tokens 计费缺失**：qwen-vl 系列按 `image_tokens` 单独计费，IR 未提取。
- **DashScope 原生端点未支持**：与 Gemini 原生相同的问题，OpenAI 兼容层缺失 DashScope 特有字段。

### 2.9 Doubao / 火山方舟 Ark

#### 2.9.1 当前接入形态

仓库当前把 Doubao 配置为 OpenAI 兼容供应商：

- `deploy/sql/schemas/baseline/02-seed.sql:870`：`provider_catalog.code='doubao'`，协议为 `openai-completions`，入口为火山方舟 Coding API。
- `deploy/sql/schemas/baseline/02-seed.sql:883`：`provider_catalog.code='volcengine-coding'`，同样使用 `openai-completions`，但它是聚合入口，包含 Doubao、GLM、DeepSeek、MiniMax、Kimi 等模型家族。
- `catalog/display.go` 与 `discovery/normalize.go` 已将 `doubao` 识别为 ByteDance / Doubao。
- `domains/streaming/strip_doubao_fields.go` 已对部分火山私有字段做清理；`system_fingerprint` 按 audit-09 修复保留。

必须区分两种数据边界：

1. **官方 Doubao offer**：可以使用 Doubao 私有 usage、缓存、推理和多模态能力。
2. **Volcengine Coding 聚合 offer**：同一 endpoint 可能返回不同厂商字段，不能无条件应用 Doubao 规则；应以 resolved provider family / model family 为准。

#### 2.9.2 当前 IR 覆盖

- **文本请求与工具**：普通 `messages`、`tools`、`tool_choice`、`response_format`、SSE 可复用 OpenAI Chat parser。
- **图像输入**：OpenAI-compatible `image_url` 可进入现有 IR，但没有按 Doubao 视觉模型能力、MIME、URL/base64 大小限制进行校验。
- **视频、文档、音频输入**：仓库当前 IR 没有独立 `video`、`document`、`audio` 内容块，无法准确表达火山方舟官方的对应能力。
- **Responses API**：官方文档已列出 Responses API 入口，但当前 IR 仍以 Chat Completions 兼容路径为主，未实现 Doubao Responses item/event 的原生适配。
- **文件输入**：火山方舟 File API / file URI 未接入 `AssetRef` / `FilePart`，文件引用会降级或丢失。
- **流式**：SSE 文本链路可用；原生多模态流式事件、Responses typed event 和 provider-specific usage 终止事件未结构化。

#### 2.9.3 官方能力与字段核对

官方火山方舟文档目录明确提供：文本生成、图片理解、视频理解、文档理解、音频理解、Function Calling、Web Search、MCP、深度思考、上下文管理、上下文缓存、File API、Responses API 和结构化输出。

| 能力/字段 | 火山方舟文档方向 | 当前 IR |
|------|------|------|
| `messages` / text generation | Chat Completions compatible | ✅ |
| `tools` / Function Calling | function calling | ✅ 基础 OpenAI 形态 |
| structured output | JSON / schema output | ⚠️ 依赖 OpenAI 兼容字段，未按模型能力校验 |
| `image_url` / 图片理解 | vision models | ⚠️ 仅通用 image block，无 Doubao capability 约束 |
| video input | 视频理解 | ❌ 无 `video` ContentBlock |
| document input | 文档理解 | ❌ 无 `document` ContentBlock / File API |
| audio input | 音频理解 | ❌ 无 `audio` ContentBlock |
| deep thinking | 深度思考 / reasoning | ⚠️ 可能落入 `reasoning_content` 或 Extensions，未结构化为 reasoning metric |
| context cache | 上下文缓存 / Context API | ⚠️ 只有通用 cache 字段，未识别 Doubao cache class / 命中字段 |
| File API / file URI | 文件输入 | ❌ 未接入 AssetRef / provider file |
| Responses API | 原生 response items/events | ❌ 未实现 Doubao 原生 adapter |
| Web Search / MCP | provider-side tools | ⚠️ 未建模，无法区分普通 function tool 与 server-side tool |
| private request IDs | `doubao_request_id` / `volc_request_id` / `seeddance_request_id` | ⚠️ stripper 清理，未进入安全 metadata |
| `system_fingerprint` | OpenAI-compatible client field | ✅ 保留 |
| `seed_token_usage` | 火山私有 token 计量字段 | ❌ 作为 private field 被清理，未进入 UsageMetric |

#### 2.9.4 Usage 与计费风险

当前 `domains/streaming/usage.go::UsageData` 只识别 `prompt_tokens`、`completion_tokens`、缓存读写四类字段。对 Doubao 产生以下风险：

- 推理模型的 reasoning token 可能只保留在 `reasoning_content` 或 provider detail 中，无法进入 `ReasoningTokens`，导致推理成本漏计。
- 视觉、视频、音频、文档的 modality detail 没有独立 metric，无法区分 token、图片、秒数、页数等计费单位。
- `seed_token_usage` 被当作私有字段删除；如果它是供应商账单对账所需的精确 token 快照，当前实现会丢失审计证据。
- 聚合入口 `volcengine-coding` 中的 GLM / DeepSeek / MiniMax 响应不能使用 Doubao usage extractor，否则会把其他厂商的字段误计为 Doubao 成本。
- 上游 `usage` 缺失时，现有估算逻辑只能估文本 token；多模态请求不得把媒体数量或时长伪造为文本 token，应记录 `gateway_estimate` 来源并进入待对账状态。

#### 2.9.5 私有字段处理建议

`StripDoubaoFieldsBody` 当前适合做客户端响应净化，但不应承担 usage 丢弃职责。建议按字段分类处理：

| 字段类别 | 当前动作 | 建议动作 |
|------|------|------|
| `system_fingerprint` | 保留 | 保留为标准 metadata |
| `doubao_request_id` / `volc_request_id` | 删除 | 脱敏 hash 后写 request metadata，便于供应商对账 |
| `content_safety_score` / `sensitive_check` | 删除 | 默认不回传客户端；仅在受控安全审计日志中保存摘要 |
| `model_endpoint` / `ab_test_group` | 删除 | 保存非敏感枚举或 hash，不进入普通 response |
| `seed_token_usage` | 删除 | 先解析到 `UsageMetric.Source=provider`，再按策略决定是否向客户端暴露 |
| 未知 Doubao 字段 | 保留/删除不明确 | 进入 loss report 或 extensions，禁止静默丢弃 |

#### 2.9.6 关键风险

- **能力错配**：当前 `provider_catalog` 只声明 `openai-completions`，无法阻止文本模型接收图片、视频、音频或文档。
- **聚合误判**：`volcengine-coding` 是聚合入口，按 provider code 使用 Doubao stripper 或 usage extractor 会误伤其他厂商响应。
- **原生能力丢失**：File API、Responses、Context API、视频/音频/文档输入没有统一 IR 表达。
- **计费漏项**：reasoning、modality detail、`seed_token_usage` 未进入 UsageMetric，当前 MaAS 只能按文本 token 估算。
- **可审计性不足**：私有 request ID 被直接删除，供应商账单对账和故障定位缺少关联键。

---

## 3. 多模态 IR 扩展方案

### 3.1 设计目标

按 `02-目标架构.md` §4 的统一 ContentPart 模型，针对当前 IR 缺失做最小补丁式扩展：

- **不破坏现有 Parse → Serialize 链路**：保留 `ImageSource` 等现有结构，新增 `MediaPart` 作为新通道。
- **新增 4 类内容块**：`audio`、`video`、`document`、`file`。
- **统一 AssetRef**：所有非文本内容用同一种 source 表达（url/data/file_id）。
- **保持 Options 语义**：OpenAI image detail、Anthropic cache hint 等通过统一 `Options` 表达。
- **Audit-10 Extensions 机制不变**：vendor 私有字段继续走 Extensions bag，不引入新的内容类型。

### 3.2 类型扩展（`internal/ir/types.go`）

```go
// ContentBlock 新增字段（保留 ImageSource 不变以兼容现有测试）
type ContentBlock struct {
    Type   string
    Text   string
    Image  *ImageSource // 兼容路径，文本/图片场景继续用
    ToolUse *ToolUse
    ToolResult *ToolResult
    Thinking *ThinkingBlock
    RedactedThinking string
    CacheControl *CacheControl
    Index *int
    RawContent any

    // ── 新增多模态通道（Phase C） ──
    Audio    *MediaPart `json:"audio,omitempty"`    // type="audio"
    Video    *MediaPart `json:"video,omitempty"`    // type="video"
    Document *MediaPart `json:"document,omitempty"` // type="document" (PDF/text/csv)
    File     *FilePart  `json:"file,omitempty"`     // type="file" (provider file id, no media kind)
}

// MediaPart 统一表达 image/audio/video/document 的内容（除 image 保留兼容）
type MediaPart struct {
    Kind     MediaKind  `json:"kind"`              // image / audio / video / document
    MIMEType string     `json:"mime_type"`         // image/png, audio/wav, video/mp4, application/pdf
    Source   AssetRef   `json:"source"`            // 统一引用
    Filename string     `json:"filename,omitempty"` // 文档原名
    Options  MediaOptions `json:"options,omitempty"`
}

// AssetRef 统一的资产引用方式
type AssetRef struct {
    Kind    AssetRefKind `json:"kind"`              // url / data / provider_file
    URL     string       `json:"url,omitempty"`     // HTTPS URL（外链）
    Data    string       `json:"data,omitempty"`    // base64 数据（不含前缀）
    FileID  string       `json:"file_id,omitempty"` // provider 原生 file id（如 Gemini files API）
    MIMESuggested string `json:"mime_suggested,omitempty"` // 由 provider 给的 mime 提示
}

// MediaOptions 媒体处理选项
type MediaOptions struct {
    Detail  string         `json:"detail,omitempty"`  // OpenAI image: "low"/"high"/"auto"
    Start   *time.Duration `json:"start,omitempty"`   // 音视频裁剪起点
    End     *time.Duration `json:"end,omitempty"`     // 音视频裁剪终点
    HighRes bool           `json:"high_res,omitempty"` // qwen-vl-max 高分辨率
    AudioVoice string      `json:"voice,omitempty"`    // OpenAI tts voice
}

// FilePart provider-managed file 引用（如 Anthropic container_upload / Gemini files）
type FilePart struct {
    Kind      string `json:"kind"`               // "anthropic_container" / "gemini_file" / "openai_file"
    FileID    string `json:"file_id"`
    MIMEType  string `json:"mime_type,omitempty"`
    Size      int64  `json:"size,omitempty"`
    ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type MediaKind string
const (
    MediaImage    MediaKind = "image"
    MediaAudio    MediaKind = "audio"
    MediaVideo    MediaKind = "video"
    MediaDocument MediaKind = "document"
)

type AssetRefKind string
const (
    AssetURL          AssetRefKind = "url"
    AssetData         AssetRefKind = "data"
    AssetProviderFile AssetRefKind = "provider_file"
)
```

### 3.3 解析与序列化扩展（parse/serialize 层）

#### 3.3.1 Parse 扩展

- `parseOpenAIContentBlocks` 增加 case：`input_audio` → `Audio *MediaPart`、`input_file` → `File *FilePart`。
- `parseAnthropicContentBlocks` 增加 case：message content 下的 `document` block → `Document *MediaPart`、`audio` (Claude 4.6 预览) → `Audio *MediaPart`。
- 新增 `parseGeminiContentPart(parts []any)`：识别 `text` / `inline_data` / `file_data` → 转换为 IR ContentBlock；`functionCall` / `functionResponse` / `thought` 分别走 tool_use / tool_result / thinking。
- 新增 `parseOllamaChat(messages []any)`：识别 `images: []base64` → `Image *MediaPart`；`thinking` → `Thinking`。

#### 3.3.2 Serialize 扩展

- `serializeOpenAIMessageContent` 增加 case：`audio` → `{type:"input_audio", input_audio:{data, format}}`、`file` → `{type:"input_file", file_id, mime_type}`。
- `serializeAnthropicContentBlock` 增加 case：`document` → `{type:"document", source:{type, media_type, data/url}}`、`audio` → Anthropic 暂无 audio 内容块，但 Claude 4.6 预览用 `{type:"audio", source:{type:"base64", data, media_type}}`。
- 新增 `serializeGeminiRequest` / `serializeGeminiResponse`：把 IR ContentBlock 转回 `parts: [{text}, {inline_data: {mimeType, data}}, {file_data: {mimeType, fileUri}}]`。
- 新增 `serializeOllamaChatRequest` / `serializeOllamaChatResponse` / `parseOllamaChatNDJSON`。

### 3.4 Capability 与路由

#### 3.4.1 数据库 schema

```sql
-- model_capabilities: 模型级能力
CREATE TABLE model_capabilities (
    canonical_id INTEGER NOT NULL REFERENCES models_canonical(id),
    direction    TEXT NOT NULL CHECK (direction IN ('input','output')),
    capability   TEXT NOT NULL,  -- text/image/audio/video/document/file/tool_call/web_search/code_execution/mcp/reasoning
    constraints  JSONB NOT NULL DEFAULT '{}'::jsonb, -- max_size, mime_pattern, etc
    source       TEXT NOT NULL,  -- 'manual' / 'official_doc' / 'capture'
    verified_at  TIMESTAMPTZ,
    PRIMARY KEY (canonical_id, direction, capability)
);

-- offer_capability_overrides: 凭据级能力覆盖（disable / replace）
CREATE TABLE offer_capability_overrides (
    credential_model_binding_id BIGINT NOT NULL REFERENCES credential_model_bindings(id),
    direction    TEXT NOT NULL,
    capability   TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL,
    constraints  JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (credential_model_binding_id, direction, capability)
);

CREATE INDEX idx_model_capabilities_capability ON model_capabilities (capability, direction);
```

#### 3.4.2 Capability Set

```go
type CapabilitySet struct {
    InputKinds     map[MediaKind]struct{} // image/audio/video/document
    OutputKinds    map[MediaKind]struct{}
    Features       map[Feature]struct{}   // tool_call/web_search/code_execution/mcp/reasoning/structured_output/prompt_cache
    MIMEPatterns   []string               // ["image/*", "audio/wav", "application/pdf"]
    MaxInlineBytes int64                  // 单 inline media 大小限制
    SupportsURL    bool                   // 是否支持外链 URL
    RequiresFileAPI bool                  // 是否必须先调用 provider file upload
    Streaming      StreamFraming          // sse/ndjson
}

type Feature string
const (
    FeatToolCall         Feature = "tool_call"
    FeatWebSearch        Feature = "web_search"
    FeatCodeExecution    Feature = "code_execution"
    FeatMCP              Feature = "mcp"
    FeatReasoning        Feature = "reasoning"
    FeatStructuredOutput Feature = "structured_output"
    FeatPromptCache      Feature = "prompt_cache"
    FeatContainer        Feature = "container"
)
```

#### 3.4.3 路由判定

```text
required_input_kinds ⊆ offer.input_kinds
required_output_kinds ⊆ offer.output_kinds
required_features ⊆ offer.features
all media MIME 在 MIMEPatterns 内
每个 media size ≤ MaxInlineBytes
external URL 必须 SupportsURL
provider_file 引用必须 RequiresFileAPI 且 file ID 属同一 provider
```

迁移期间保留旧 `modality` 字段：
- `text` → CapabilitySet.InputKinds={}
- `vision` → {image}
- `audio` → {audio}
- `multimodal` → {image, audio?, video?, document?}（人工标注）
- `embedding` → Feature only，不参与多模态路由

### 3.5 流式扩展

#### 3.5.1 事件类型

```go
type StreamEventType string
const (
    StreamStart          StreamEventType = "start"
    StreamItemAdded      StreamEventType = "item_added"        // Responses API
    StreamItemDelta      StreamEventType = "item_delta"
    StreamItemDone       StreamEventType = "item_done"
    StreamContentPartDelta StreamEventType = "part_delta"      // Anthropic
    StreamToolUseDelta   StreamEventType = "tool_use_delta"
    StreamReasoningDelta StreamEventType = "reasoning_delta"
    StreamUsageUpdate    StreamEventType = "usage_update"      // Ollama done / Anthropic message_delta
    StreamError          StreamEventType = "error"
    StreamDone           StreamEventType = "done"
)
```

#### 3.5.2 Adapter 实现责任

- `openai-chat`：SSE chunk → StreamEvent（item_delta / tool_use_delta / usage_update）
- `openai-responses`：typed SSE events（response.output_item.added / .delta / .done）
- `anthropic-messages`：SSE 多 event 流（message_start / content_block_* / message_delta / message_stop），usage 在 message_start（input）与 message_delta（output）累加
- `gemini-generate`：SSE GenerateContentResponse，candidates[0].content.parts[] 转 StreamEvent
- `ollama-chat`：NDJSON（每行一个 JSON，message 增量 + done 终止对象）

### 3.6 安全与资源限制

| 限制 | 默认 | 说明 |
|------|------|------|
| 单 request body | 50 MB | multipart 与 base64 |
| 单 media | 20 MB | base64 解码后 |
| media count | 50 / message | 防止 abuse |
| URL scheme | https only | 阻止 http（生产） |
| SSRF 阻断 | loopback / link-local / metadata IP | 防止内网探测 |
| provider_file | 必须同一 provider | 防止跨 provider file ID 复用 |
| MIME 校验 | 实际 magic bytes | 不可只信声明 |

### 3.7 Extensions 与 loss report

- 多模态新字段走 IR 标准通道，不进入 Extensions bag。
- 跨协议时多模态字段 loss 写入 `LossReport.Dropped`：
  - `field_class=media`、`media_kind=audio`、`action=drop`、`reason=target_protocol_unsupported`。
- `field_class` 取值：`media` / `tool` / `generation` / `usage` / `metadata`，便于租户策略。
- LossReport 不携带原始敏感数据（base64、URL、file_id 全部摘要化或丢弃）。

---

## 4. 多模态计费方案

### 4.1 现状与缺口

#### 4.1.1 `domains/streaming/usage.go` 当前 UsageData

```go
type UsageData struct {
    PromptTokens     *int
    CompletionTokens *int
    CacheReadTokens  *int
    CacheWriteTokens *int
}
```

只有 4 类 token，调用 `ExtractUsageFromChunk` 走硬编码字段名映射，无 provider-specific extractor。

#### 4.1.2 `maas/service.go::ChargeRequest` 当前签名

```go
func (s *Service) ChargeRequest(ctx context.Context, tenantID, requestID, canonicalName string,
    promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) (int64, error)
```

调用 `CalcCredits(prompt, completion, cache_read, cache_write, rates)`，rate 字段：

```go
BaseCreditsPer1M         int64
BaseCreditsPer1MIn       int64
BaseCreditsPer1MOut      int64
BaseCreditsPer1MCacheIn  int64
BaseCreditsPer1MCacheOut int64
```

#### 4.1.3 DB schema 当前

- `models_canonical.modality text`（CHECK: text/vision/audio/multimodal/embedding）
- `credential_model_bindings.plan_meta JSONB`（含 `unit: "per_image"` 等字符串）
- `pricing_plans.plan_json JSONB`

#### 4.1.4 缺口

| 缺口 | 后果 |
|------|------|
| 无 reasoning_tokens 字段 | DeepSeek R1 / GLM-Z1 / MiniMax-M3 / Claude thinking 计费漏掉 |
| 无 image/audio/video/document tokens | Qwen-VL / GLM-4V / Gemini 多模态按 image_tokens 单独计费但网关按 0 计费 |
| 无 audio_seconds / video_seconds | Gemini audio/video 按 duration 计费，IR 无 duration 字段 |
| 无 modality detail arrays | Gemini `promptTokensDetails[].modality` 与 `candidatesTokensDetails[].modality` 未提取 |
| 固定 8 列定价（in/out/cache_in/cache_out/image_*） | 不能表达 request 价 / service tier / region / 时间窗 / 单位换算 |
| 供应商成本与租户 credits 混用 | 改一处不影响另一处，账单对账困难 |
| `unit: "per_image"` 是字符串 | 无法机器校验单位是否合法 |

### 4.2 canonical usage metrics

按 `02-目标架构.md` §8 的 UsageMetric 设计：

```go
type UsageMetricName string
const (
    MetricInputTokens      UsageMetricName = "input_tokens"
    MetricOutputTokens     UsageMetricName = "output_tokens"
    MetricCachedTokens     UsageMetricName = "cached_tokens"
    MetricCacheWriteTokens UsageMetricName = "cache_write_tokens"
    MetricReasoningTokens  UsageMetricName = "reasoning_tokens"

    MetricImageTokens      UsageMetricName = "image_tokens"
    MetricImageCount       UsageMetricName = "image_count"
    MetricImageTiles       UsageMetricName = "image_tiles"

    MetricAudioTokens      UsageMetricName = "audio_tokens"
    MetricAudioSeconds     UsageMetricName = "audio_seconds"

    MetricVideoTokens      UsageMetricName = "video_tokens"
    MetricVideoSeconds     UsageMetricName = "video_seconds"
    MetricVideoFrames      UsageMetricName = "video_frames"

    MetricDocumentTokens   UsageMetricName = "document_tokens"
    MetricDocumentPages    UsageMetricName = "document_pages"

    MetricRequests         UsageMetricName = "requests"
)

type UsageUnit string
const (
    UnitToken       UsageUnit = "token"
    UnitImage       UsageUnit = "image"
    UnitTile        UsageUnit = "tile"
    UnitSecond      UsageUnit = "second"
    UnitFrame       UsageUnit = "frame"
    UnitPage        UsageUnit = "page"
    UnitRequest     UsageUnit = "request"
    UnitChar        UsageUnit = "char"
    UnitGPUSecond   UsageUnit = "gpu_second"
)

type UsageMetric struct {
    Metric          UsageMetricName
    Direction       Direction  // input / output
    Quantity        decimal.Decimal
    Unit            UsageUnit
    Modality        MediaKind  // image/audio/video/document/text
    Source          UsageSource // provider / gateway_estimate / invoice_reconcile
    IncludedInTotal bool       // Gemini detail 已包含在 input/output total 中时 = true
    ProviderPath    string     // "usage.prompt_tokens_details.cached_tokens"
}
```

### 4.3 Provider-specific Usage Normalizer

#### 4.3.1 接口

```go
type UsageNormalizer interface {
    Extract(rawUsage json.RawMessage) ([]UsageMetric, error)
}

type ProviderUsageNormalizer struct {
    Provider  string  // "openai" / "anthropic" / "deepseek" / "glm" / "qwen" / "minimax" / "gemini" / "ollama"
    Extractors []FieldExtractor
}

type FieldExtractor struct {
    Match   func(map[string]json.RawMessage) bool  // 匹配 raw usage shape
    Extract func(map[string]json.RawMessage) ([]UsageMetric, error)
}
```

#### 4.3.2 每个厂商的关键提取器

| Provider | 字段 | 提取为 |
|----------|------|--------|
| OpenAI | `prompt_tokens` | input_tokens (token) |
| OpenAI | `completion_tokens` | output_tokens (token) |
| OpenAI | `prompt_tokens_details.cached_tokens` | cached_tokens (token, IncludedInTotal=true) |
| OpenAI | `completion_tokens_details.reasoning_tokens` | reasoning_tokens (token, IncludedInTotal=true) |
| OpenAI | `completion_tokens_details.audio_tokens` | audio_tokens (token) |
| OpenAI | `prompt_tokens_details.audio_tokens` | audio_tokens (token, input) |
| Anthropic | `input_tokens` / `output_tokens` | input/output_tokens |
| Anthropic | `cache_read_input_tokens` | cached_tokens |
| Anthropic | `cache_creation_input_tokens` | cache_write_tokens |
| DeepSeek | `prompt_cache_hit_tokens` | cached_tokens (deepseek_cache_hit price tier) |
| DeepSeek | `prompt_cache_miss_tokens` | input_tokens (full price) |
| DeepSeek | `completion_tokens_details.reasoning_tokens` | reasoning_tokens |
| GLM | `prompt_tokens_details.cached_tokens` | cached_tokens |
| GLM | `completion_tokens_details.reasoning_tokens` | reasoning_tokens |
| Qwen-VL | `image_tokens` | image_tokens (IncludedInTotal=true) |
| Qwen-Audio | `audio_tokens` | audio_tokens |
| MiniMax | `reasoning_tokens` | reasoning_tokens |
| MiniMax | `cache_read_tokens` | cached_tokens |
| Gemini | `promptTokenCount` / `candidatesTokenCount` | input/output_tokens |
| Gemini | `cachedContentTokenCount` | cached_tokens |
| Gemini | `thoughtsTokenCount` | reasoning_tokens |
| Gemini | `promptTokensDetails[].modality=IMAGE,tokenCount` | image_tokens (IncludedInTotal=true) |
| Gemini | `promptTokensDetails[].modality=AUDIO,tokenCount` | audio_tokens |
| Gemini | `promptTokensDetails[].modality=VIDEO,tokenCount` | video_tokens |
| Ollama | `prompt_eval_count` | input_tokens |
| Ollama | `eval_count` | output_tokens |
| Ollama | `total_duration / 1e9` | gpu_seconds (telemetry 推算，不进 cost) |

### 4.4 Rate Component 模型

#### 4.4.1 数据库 schema

```sql
-- 供应商成本 rate card
CREATE TABLE provider_rate_cards (
    id           BIGSERIAL PRIMARY KEY,
    provider_id  INTEGER NOT NULL,
    catalog_code TEXT NOT NULL,
    model_family TEXT NOT NULL,
    region       TEXT NOT NULL DEFAULT 'global',
    currency     TEXT NOT NULL DEFAULT 'USD',
    status       TEXT NOT NULL DEFAULT 'active', -- active / archived / draft
    version      INTEGER NOT NULL DEFAULT 1,
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (catalog_code, model_family, region, version, effective_from)
);

-- 供应商成本 rate component
CREATE TABLE provider_rate_components (
    id           BIGSERIAL PRIMARY KEY,
    rate_card_id BIGINT NOT NULL REFERENCES provider_rate_cards(id) ON DELETE CASCADE,
    metric       TEXT NOT NULL,       -- input_tokens / cached_tokens / image_count / audio_seconds
    direction    TEXT NOT NULL CHECK (direction IN ('input','output','flat')),
    modality     TEXT NOT NULL DEFAULT 'text',  -- text/image/audio/video/document
    unit         TEXT NOT NULL,       -- token/image/second/request
    scale        BIGINT NOT NULL DEFAULT 1000000, -- 每多少 unit 收一次价（默认 1M token）
    rate         NUMERIC(20, 8) NOT NULL,
    context_min  BIGINT,
    context_max  BIGINT,
    service_tier TEXT,                 -- standard/priority/batch
    cache_class  TEXT                  -- read/write/miss/hit
);

-- 租户 credits rate card（同样结构）
CREATE TABLE tenant_credit_rate_cards (
    id BIGSERIAL PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    offer_id   BIGINT REFERENCES credential_model_bindings(id),
    metric TEXT NOT NULL,
    direction TEXT NOT NULL,
    modality TEXT NOT NULL DEFAULT 'text',
    unit  TEXT NOT NULL,
    scale BIGINT NOT NULL DEFAULT 1000000,
    rate  NUMERIC(20, 8) NOT NULL,
    context_min BIGINT,
    context_max BIGINT,
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to   TIMESTAMPTZ,
    UNIQUE (tenant_id, offer_id, metric, modality, effective_from)
);
```

#### 4.4.2 Rate Component 规则

```go
type RateComponent struct {
    Metric      UsageMetricName
    Direction   Direction
    Modality    MediaKind
    Unit        UsageUnit
    Scale       int64  // 1000000 表示每 1M token
    Rate        decimal.Decimal
    ContextMin  *int64
    ContextMax  *int64
    ServiceTier string
    CacheClass  string
}
```

#### 4.4.3 计价函数

```go
func ComputeCost(metrics []UsageMetric, components []RateComponent) (decimal.Decimal, []ChargeLine, error) {
    var total decimal.Decimal
    var lines []ChargeLine
    matched := make(map[string]bool)

    for _, m := range metrics {
        component := selectComponent(m, components)
        if component == nil {
            return decimal.Zero, nil, &MissingRateError{Metric: m.Metric, Modality: m.Modality}
        }
        if component.Scale == 0 {
            return decimal.Zero, nil, fmt.Errorf("rate component scale=0 for metric %s", m.Metric)
        }
        qty := m.Quantity.Div(decimal.NewFromInt(component.Scale))
        cost := qty.Mul(component.Rate)
        total = total.Add(cost)
        lines = append(lines, ChargeLine{
            Metric: m.Metric,
            Quantity: m.Quantity,
            Unit: m.Unit,
            Rate: component.Rate,
            Cost: cost,
            ComponentID: component.ID,
        })
        matched[component.ID] = true
    }

    return total, lines, nil
}
```

### 4.5 幂等与防重扣

#### 4.5.1 request_charge_details

```sql
CREATE TABLE request_charge_details (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,  -- 网关 request ID，幂等键
    tenant_id TEXT NOT NULL,
    provider_id INTEGER NOT NULL,
    credential_id INTEGER NOT NULL,
    canonical_id INTEGER,
    calculation_version INTEGER NOT NULL,
    usage_snapshot JSONB NOT NULL,    -- 原始 usage JSON
    usage_metrics JSONB NOT NULL,     -- 归一化后的 UsageMetric 列表
    provider_rate_snapshot JSONB NOT NULL,
    credit_rate_snapshot JSONB,
    cost_usd NUMERIC(20, 8),
    credits_charged NUMERIC(20, 8),
    usage_source TEXT NOT NULL,        -- provider / gateway_estimate / invoice_reconcile
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_charge_request_id ON request_charge_details (request_id);
CREATE INDEX idx_charge_tenant_created ON request_charge_details (tenant_id, created_at DESC);
```

#### 4.5.2 ledger 与 idempotency

- ledger 仍用 `entry_type='consume'`，通过 `ref_id=request_id` 关联 charge detail
- 写入 charge detail 用 `INSERT ... ON CONFLICT (request_id) DO NOTHING`
- ledger 写入前先 SELECT `ref_id=request_id AND entry_type='consume'`，已存在则跳过
- 重复 webhook / 流式 retry 不会重复扣费

#### 4.5.3 aggregate 与 detail 防重复

```go
// calculator 在 rate selection 前执行一致性校验
func ValidateUsageMetrics(metrics []UsageMetric) error {
    hasAggregate := map[UsageMetricName]bool{}
    hasDetail := map[UsageMetricName]bool{}

    for _, m := range metrics {
        if m.IncludedInTotal {
            hasDetail[m.Metric] = true
        } else {
            hasAggregate[m.Metric] = true
        }
    }

    // input/output_tokens 已包含 image_tokens 时，rate 不能同时按 input_tokens + image_tokens 计费
    for name := range hasDetail {
        if hasAggregate[name] {
            return &UsageConflictError{
                Metric: name,
                Reason: "aggregate and detail both present",
            }
        }
    }
    return nil
}
```

### 4.6 供应商成本 vs 租户 credits 双轨

- `provider_rate_components`：网关结算给供应商的成本（影响毛利）
- `tenant_credit_rate_components`：租户按 credit 抵扣（影响收入）
- 两个 calculator 共享归一化 UsageMetric，分别选择 rate component
- 任一 calculator 失败时仍记录另一侧，保证账单可对账

### 4.7 Pricing 迁移

| 阶段 | 范围 | 验证 |
|------|------|------|
| Phase D-1 | 新增 tables，shadow-only；旧 calculator 继续工作 | 新表无生产读取 |
| Phase D-2 | 新 calculator 在 tenant=default 上跑 shadow，旧 calculator 继续 | log diff < 0.01% |
| Phase D-3 | 按租户灰度切到新 calculator，按 calculation_version 决定走哪条 | 切流后 24h 无告警 |
| Phase D-4 | 旧 calculator 仅保留 fallback | 新表成为 SSOT |

---

## 5. 实施任务清单

### 5.1 P0 — 必须立即修复（1-2 周）

#### P0-1: UsageData 字段扩展 + Reasoning 计费

**目标**: DeepSeek R1 / GLM-Z1 / MiniMax-M3 / Claude thinking 计费不再丢失

**任务**:
1. `internal/ir/usage_normalizer.go` 新增 `UsageData` 扩展字段：`ReasoningTokens *int`、`AudioTokens *int`、`ImageTokens *int`、`VideoTokens *int`、`DocumentTokens *int`、`RequestCount *int`
2. `domains/streaming/usage.go::ExtractUsageFromChunk` 增加 field extractor 注册机制
3. 新增 `internal/ir/usage_normalizer.go::NormalizeUsage(rawUsage, provider)` 根据 provider 选择 extractor
4. `maas/service.go::ChargeRequest` 新签名支持 reasoning / audio / image / video tokens
5. `model_credit_rates` 表新增 `credits_per_1m_reasoning`、`credits_per_1m_audio`、`credits_per_1m_image` 等字段（向后兼容：ADD COLUMN NULL）

**验证**:
- DeepSeek R1 fixture：reasoning_tokens=100，cost = 100 × reasoning_rate / 1M
- 双跑新旧 calculator：旧 calculator 算 0，新 calculator 算正确值，diff 报告

#### P0-2: input_audio / output_audio IR 支持（OpenAI）

**目标**: OpenAI Chat audio / audio output 客户端可用

**任务**:
1. `internal/ir/parse_openai.go::parseOpenAIContentBlocks` 增加 case `"input_audio"` → `Audio *MediaPart`
2. `internal/ir/serialize_openai.go::serializeOpenAIMessageContent` 增加 case `"audio"` → `{type:"input_audio", input_audio:{data, format}}`
3. `internal/ir/response.go::parseOpenAIResponseContentBlock` 增加 case `"output_audio"` → 输出 audio block
4. fixture: OpenAI audio request / response 真实脱敏（从生产抓）

**验证**:
- OpenAI gpt-4o-audio-preview request round-trip 不丢字段
- response output_audio 不丢 audio.data

#### P0-3: Anthropic message content 下 document block 修复

**目标**: PDF 在 user message content 不丢失

**任务**:
1. `internal/ir/parse_anthropic.go::parseAnthropicContentBlock` 增加 case `"document"` → `Document *MediaPart`
2. `serialize_anthropic.go::serializeAnthropicContentBlock` 增加 case `"document"` → Anthropic document block
3. 删除 `types.go::PDFDocument`/`PDFSource`（统一到 MediaPart.Kind=document + MIME=application/pdf）

**验证**:
- Claude PDF fixture: content=[{type:"document", source:{type:"base64", media_type:"application/pdf", data:"..."}}] 不丢

#### P0-4: Usage Normalizer 提取器注册

**目标**: 每个 provider 至少 5 个 UsageMetric 可被提取

**任务**:
1. `internal/ir/usage_normalizer.go` 定义 `UsageNormalizer` 接口
2. 为 DeepSeek / GLM / Qwen / MiniMax / Gemini / Ollama 各实现一个 `UsageNormalizer`
3. `domains/streaming/usage.go::ExtractUsageFromChunk` 接受 provider 参数
4. 新增 `internal/ir/usage_normalizer_test.go` 覆盖所有 extractor

**验证**:
- 每个 provider 的真实响应 fixture 都能提取出正确 metric
- unit test 覆盖率 ≥ 80%

### 5.2 P1 — 多模态能力与原生 adapter（2-4 周）

#### P1-1: MediaPart / AssetRef 类型与序列化

**目标**: IR 支持 audio / video / document / file 多模态内容

**任务**:
1. `internal/ir/types.go` 新增 `MediaPart`、`AssetRef`、`MediaOptions`、`FilePart`、`MediaKind`、`AssetRefKind`（见 §3.2）
2. `ContentBlock` 新增 `Audio` / `Video` / `Document` / `File` 字段（保留 `Image` 兼容）
3. 解析/序列化函数逐 adapter 增加 case
4. 不删除 `ImageSource`，迁移期双写

**验证**:
- OpenAI image_url / input_audio / input_file fixture round-trip
- Anthropic image / document fixture round-trip
- 旧 image fixture 不回归

#### P1-2: Capability 表 + 路由

**目标**: 多模态请求只路由到能力匹配的 offer

**任务**:
1. 实施 §3.4 schema（`model_capabilities`、`offer_capability_overrides`）
2. 初始数据：从 `models_canonical.modality` + 官方文档 + audit-09 真实 capture 填充
3. capability resolver 在 routing 前判定 request.required_input_kinds ⊆ offer.input_kinds
4. 缺能力返回 `422 unsupported_model_capability`，响应含 missing_capability 列表

**验证**:
- qwen-vl-max 的 image+audio 请求能路由到 qwen-vl-max（input_kinds={image,audio}）
- qwen-vl-max 的 audio-only 请求被拒绝（input_kinds={image,audio}，audio 包含）
- qwen-text 模型收到 image 请求被拒绝

#### P1-3: Gemini 原生 Adapter

**目标**: 不依赖 OpenAI 兼容层支持 Gemini native 协议

**任务**:
1. `internal/ir/parse_gemini.go` / `serialize_gemini.go`
2. contents[].parts[] 解析：`text` / `inline_data` / `file_data` / `function_call` / `function_response` / `thought`
3. `usageMetadata` 归一化为 UsageMetric[]（含 modality detail arrays）
4. 流式 `streamGenerateContent?alt=sse` decoder
5. `internal/providercap/capability.go::Resolve` 增加 `gemini-generate` protocol 分支

**验证**:
- Gemini 2.5 Flash text-only fixture
- Gemini 2.5 Flash image input fixture（inlineData + fileData）
- Gemini 2.5 Pro video fixture
- Gemini 2.5 Flash thinking fixture（含 thoughtsTokenCount）

#### P1-4: Ollama 原生 Adapter

**目标**: `/api/chat` NDJSON 正确解析 + eval_count 转 UsageMetric

**任务**:
1. `internal/ir/parse_ollama.go` / `serialize_ollama.go`
2. NDJSON stream decoder（每行一个 JSON）
3. `message.images: []base64` → `Image *MediaPart`
4. `message.thinking` → ReasoningContent
5. `done` 事件含 `prompt_eval_count` / `eval_count` / `total_duration` 转 UsageMetric
6. `durations` 仅入 telemetry，不入 cost

**验证**:
- Ollama qwen2.5-vl chat fixture（base64 images）
- Ollama qwen3 chat fixture（含 thinking）
- 流式 NDJSON 多帧正确组装

#### P1-5: Rate Component 表 + Calculator

**目标**: 多单位计费可表达

**任务**:
1. 实施 §4.4 schema（`provider_rate_cards` / `provider_rate_components` / `tenant_credit_rate_cards` / `tenant_credit_rate_components`）
2. `maas/calculator.go` 新版 calculator：按 metric + modality + unit 选 rate component
3. shadow mode 双跑 7 天
4. Phase D-2 / D-3 / D-4 灰度切流

**验证**:
- Gemini image fixture: image_tokens=258, image_rate=$0.001/1k → cost=$0.000258
- DeepSeek R1 fixture: reasoning_tokens=100 → 按 reasoning_rate 计费
- Ollama fixture: gpu_seconds × gpu_rate
- 重复 webhook 不重复扣费（idempotency 测试）

#### P1-6: Doubao / Volcengine Provider Family 隔离

**目标**: 官方 Doubao 与火山方舟聚合入口使用正确的 adapter、stripper、usage normalizer 和 rate card。

**任务**:
1. 将 `provider_catalog.code`、`model_family`、`protocol` 分离，禁止仅根据 `volcengine-coding` 选择 Doubao 规则。
2. 为 `doubao` 官方 offer 注册 Doubao profile；为 `volcengine-coding` 注册按 resolved model family 分派的 profile。
3. 将 `StripDoubaoFieldsBody` 改为显式 profile 调用，并保留 `system_fingerprint`、脱敏 request ID 和 provider usage snapshot。
4. 新增 Doubao usage normalizer：`prompt_tokens`、`completion_tokens`、cache、reasoning、modality detail、`seed_token_usage`。
5. 增加 `image` / `video` / `audio` / `document` capability seed 数据，未声明能力时返回 `422 unsupported_model_capability`。
6. 增加 Doubao 专用 rate card，区分文本 token、缓存 token、reasoning token 和媒体单位；聚合入口按实际 model family 选择费率。

**验证**:
- 官方 Doubao response 不丢 `system_fingerprint`，私有 request ID 只进入脱敏 metadata。
- `volcengine-coding` 返回 GLM / DeepSeek / MiniMax fixture 时，不触发 Doubao stripper 或 Doubao rate card。
- Doubao reasoning/cache fixture 产生正确 `UsageMetric`，重复完成事件不重复扣费。
- Doubao vision model 可接收图片；文本模型接收图片时路由拒绝。

### 5.3 P2 — 长期演进（1-2 月）

#### P2-1: Qwen / DashScope 原生 adapter

**目标**: DashScope multimodal model（qwen-vl-max / qwen-audio / qwen-vl-video）原生支持

#### P2-2: Responses API 原生 Parse + 流式

**目标**: `function_call_output` / `input_image` / `input_file` 等 Responses items 正确处理

#### P2-2a: Doubao 原生多模态 / Responses / File API

**目标**: 火山方舟视频、文档、音频、文件引用与 Responses 原生事件进入统一 IR。

#### P2-3: MiniMax 原生 audio / video / MiniMax-M3 thinking

**目标**: MiniMax voice / video 模型 + thinking mode 计费

#### P2-4: Invoice reconciliation

**目标**: 供应商账单与 IR usage 对账，diff > 阈值告警

#### P2-5: LossReport 持久化

**目标**: 跨协议不可映射字段写入 `request_loss_details` 表，便于租户审查

---

## 6. 验证矩阵

### 6.1 Per-provider fixture 要求

每个原厂至少覆盖以下 fixture；Doubao 还必须覆盖官方入口与聚合入口两条变体：

1. **最小文本**：system + user + assistant
2. **结构化输出**：JSON schema response_format
3. **工具调用**：单 tool、多 tool、并行 tool
4. **tool-only response**：无 text 仅有 tool_use
5. **text + tool 混合**
6. **reasoning + tool**：thinking mode 下返回 tool_use
7. **流式 usage 终止事件**：每个 provider 的 terminal usage 字段名不同
8. **provider error event**：HTTP 200 with error body
9. **unknown event / field 保留**
10. **每种支持 media 的 URL / data / file_id**
11. **超大小 / 非法 MIME / 能力不满足**
12. **cache hit / miss**：DeepSeek `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` 分离
13. **reasoning_tokens**：DeepSeek R1 / GLM-Z1 / MiniMax-M3 / Gemini thinking 各自 usage 字段
14. **modality detail**：Gemini `promptTokensDetails[].modality` 数组
15. **Doubao 官方入口**：文本、tool call、structured output、SSE、reasoning、cache、`system_fingerprint`
16. **Doubao 图片理解**：URL 与 base64 两种 `image_url`，非视觉模型能力拒绝
17. **Doubao 原生多模态**：视频、文档、音频、File API / file URI 映射到 `MediaPart` / `FilePart`
18. **Doubao Responses**：原生 response item 与流式 event，不得降级成 Chat 文本
19. **Volcengine Coding 聚合**：Doubao / GLM / DeepSeek / MiniMax 各自 response fixture，验证 provider family 隔离
20. **Doubao private usage**：`seed_token_usage`、reasoning detail、cache detail 进入 provider usage snapshot，且不直接回传敏感字段

### 6.2 计费验证用例

| 用例 | 期望 |
|------|------|
| image_tokens included_in_total=true | 只按选定 rate component 结算一次 |
| provider only returns aggregate tokens | 使用 aggregate rate，不凭空拆 modality |
| provider returns audio_seconds rate | 按 second component，不换算为伪 token |
| provider returns request-level price | 按 request component（Ollama 本地 GPU） |
| usage estimate later reconciled | 保存两版来源，生成差异，不篡改历史快照 |
| duplicate completion callback | 幂等键阻止重复 debit |
| provider cost exists, tenant rate absent | 可记录成本，租户结算按 fallback / reject policy |
| rate changes at midnight | 根据 request / rate effective timestamp 选择版本 |
| DeepSeek cache_hit + cache_miss 同时存在 | cache_hit 按 cache_rate，miss 按 prompt_rate |

### 6.3 必跑测试

```bash
# IR 层
go test ./internal/ir/... -count=1 -race

# 转换层
go test ./domains/transformation/... -count=1 -race

# 流式层
go test ./domains/streaming/... -count=1 -race

# 计费层
go test ./maas/... -count=1 -race
go test ./provider/... ./internal/providercap/... -count=1 -race

# Lint
go vet ./...
golangci-lint run --config .golangci.yml

# DB migration
psql $PG_URL -v ON_ERROR_STOP=1 -f sql/migrations/startup/046_multimodal_capabilities.sql
psql $PG_URL -v ON_ERROR_STOP=1 -f sql/migrations/startup/047_rate_components.sql
```

### 6.4 发布指标

| 指标 | 触发 |
|------|------|
| `ir_multimodal_blocks_total{kind,provider}` | 多模态块数 |
| `ir_loss_fields_total{source,target,field_class,media_kind,action}` | LossReport |
| `protocol_adapter_errors_total{adapter,stage}` | Adapter 错误 |
| `capability_rejections_total{capability,provider,reason}` | 能力拒绝 |
| `usage_metric_source_total{metric,provider,source}` | usage 提取来源 |
| `usage_metric_extraction_miss_total{metric,provider}` | usage 提取失败 |
| `billing_duplicate_charge_blocked_total{provider}` | 幂等阻止次数 |
| `billing_aggregate_detail_conflict_total{provider,metric}` | aggregate+detail 冲突 |
| `billing_reconciliation_delta{provider,model}` | 对账差异 |
| `doubao_private_field_seen_total{field,action}` | 豆包私有字段分类处理 |
| `provider_family_mismatch_total{catalog,model_family}` | 聚合入口厂商族误判 |

任何指标出现连续 5 分钟上涨 2 倍以上，自动停用新路径。

---

## 7. 相关文档

| 文档 | 用途 |
|------|------|
| `01-现状审计与修正.md` | 早期审计结论与可信现状 |
| `02-目标架构.md` | 协议 adapter / canonical IR / capability / usage / rate card 设计 |
| `03-实施与迁移计划.md` | Phase A-F 阶段任务 |
| `04-协议与验证矩阵.md` | 协议级支持矩阵 + 原厂资料索引 |
| `06-Provider-Profile审计与收敛.md` | vendor stripper 修复与 ProviderProfile 设计 |
| `08-下一步任务清单.md` | P1/P2 任务清单（UsageNormalizer / Capture / ClientCatalogCode） |
| `09-改动审计报告.md` | audit-09 vendor stripper 修复细节 |
| `10-Provider-IR-Multimodal-Audit-2026-07-13.md` §2.9 | Doubao / 火山方舟官方入口、聚合入口、私有字段与多模态审计 |
| 本文档 (10) | 当前现状的代码级审计 + 多模态与计费方案 |
| `.scratch/audit-2026-07-13-provider-ir-multimodal/00-task.md` | 任务进度追踪 |
