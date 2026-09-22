# 客户端 × 厂商格式兼容矩阵

本矩阵描述每种客户端协议（入站）与每个厂商协议（出站）的兼容性。

✅ 完整支持 | ⚠️ 部分支持，需要降级 | ❌ 不支持

## OpenAI Chat Completions 客户端 → 厂商

| 客户端 → 厂商 | 兼容度 | 备注 |
|---------------|--------|------|
| OpenAI Chat → OpenAI Chat | ✅ | 直通 |
| OpenAI Chat → Anthropic | ⚠️ | max_tokens 必填、system 字段独立、tools 重命名、tool_result 在 user message |
| OpenAI Chat → Gemini | ⚠️ | role/model 改名、parts 包装、functionDeclarations 重构 |
| OpenAI Chat → DeepSeek | ✅ | OpenAI 兼容，仅 reasoning_content 私有字段 |
| OpenAI Chat → Qwen | ✅ | OpenAI 兼容，仅 enable_search 私有字段 |
| OpenAI Chat → GLM | ✅ | OpenAI 兼容，仅 do_sample 私有字段 |
| OpenAI Chat → MiniMax | ⚠️ | base_resp 错误信号需识别 |
| OpenAI Chat → Kimi | ✅ | OpenAI 兼容 |
| OpenAI Chat → Ark | ✅ | OpenAI 兼容 |
| OpenAI Chat → Grok | ✅ | OpenAI 兼容，search_parameters 私有 |
| OpenAI Chat → Mistral | ✅ | OpenAI 兼容 |
| OpenAI Chat → OpenRouter | ✅ | OpenAI 兼容，route/models 私有 |
| OpenAI Chat → vLLM | ✅ | OpenAI 兼容 |
| OpenAI Chat → Ollama | ⚠️ | 流式是 NDJSON 而非 SSE |

## ZCode CLI / MiniMax Code / DeepSeek Code（2026-09-21 新增）

三家国内编程客户端的协议覆盖见各客户端文档（zcode.md / minimax-code.md /
deepseek-code.md）。矩阵视角的兼容性：

| 客户端 | 入站 | → OpenAI Chat | → Anthropic | → DeepSeek | → MiniMax M2/M3 | → Qwen / GLM / Kimi |
|--------|------|----------------|-------------|------------|-----------------|---------------------|
| ZCode CLI | OpenAI Chat | ✅ 直通 | ⚠️ tools 重命名 + system 拆 | ✅ 透传 | ⚠️ base_resp | ✅ OpenAI 兼容 |
| MiniMax Code | Anthropic | ⚠️ Anthropic → OpenAI | ✅ 直通（注意 thinking=adaptive） | ⚠️ OpenAI 兼容路径 | ⚠️ thinking 需 `enabled → adaptive` | ⚠️ OpenAI 兼容路径 |
| DeepSeek Code | OpenAI Chat | ✅ 直通 | ⚠️ reasoning_content → thinking（无 signature 风险） | ✅ 直通 | ⚠️ base_resp | ✅ OpenAI 兼容 |

要点：

- **MiniMax Code 默认 Anthropic Messages**：网关的 Anthropic 入站路径需要
  把 `metadata.client_type == "minimax_code"` 标记到 Prometheus label
  `client_type=minimax-code`，并把目标上游是 MiniMax M2/M3 的 thinking 字段
  从 `enabled` 转为 `adaptive`（参见 2026-09-18 事故）。
- **DeepSeek Code 调用 DeepSeek-R1**：必须保留 `reasoning_content` 字段（IR
  已支持 `InternalResponse.ReasoningContent` / `StreamChunk.ReasoningDelta`）。
- **ZCode CLI** 的 `zcode_*` 工具命名空间在 Anthropic 上游可直接透传；
  在严格 OpenAI 兼容上游（vLLM < 0.4）会因 `_` 报错，当前网关透传 OK。

## Anthropic Messages 客户端 → 厂商

| 客户端 → 厂商 | 兼容度 | 备注 |
|---------------|--------|------|
| Anthropic → Anthropic | ✅ | 直通 |
| Anthropic → OpenAI Chat | ⚠️ | system 移到 messages、max_tokens 透传、input_schema→parameters、tool_use 转换 |
| Anthropic → Gemini | ⚠️ | role/model 改名、contents/parts、functionDeclarations |
| Anthropic → DeepSeek | ⚠️ | OpenAI 兼容路径，类似 OpenAI Chat |
| Anthropic → Qwen | ⚠️ | OpenAI 兼容路径 |
| Anthropic → GLM | ⚠️ | OpenAI 兼容路径 |
| Anthropic → MiniMax | ⚠️ | base_resp 错误识别 |
| Anthropic → Kimi | ⚠️ | OpenAI 兼容路径 |
| Anthropic → Ark | ⚠️ | OpenAI 兼容路径 |
| Anthropic → Grok | ⚠️ | OpenAI 兼容路径 |
| Anthropic → Mistral | ⚠️ | OpenAI 兼容路径 |
| Anthropic → OpenRouter | ⚠️ | OpenAI 兼容路径 |
| Anthropic → vLLM | ⚠️ | OpenAI 兼容路径 |
| Anthropic → Ollama | ⚠️ | OpenAI 兼容 + NDJSON 流式 |

## OpenAI Responses 客户端 → 厂商

| 客户端 → 厂商 | 兼容度 | 备注 |
|---------------|--------|------|
| Responses → OpenAI Responses | ✅ | 直通 |
| Responses → OpenAI Chat | ⚠️ | input 拆分为 messages、output_text 提取 |
| Responses → Anthropic | ⚠️ | input 拆分、instructions→system、内置工具降级 |
| Responses → Gemini | ⚠️ | input→contents、内置工具降级 |
| Responses → 其他 OpenAI 兼容 | ⚠️ | 类似 OpenAI Chat 路径 |

## 关键不兼容字段

| 字段 | 适用客户端 | 不适用厂商 | 网关处理 |
|------|------------|------------|----------|
| `response_format` | OpenAI Chat/Responses | Anthropic、Gemini | 注入 prompt 强制 JSON |
| `tools[].function.parameters` | OpenAI | Anthropic | 重命名为 `input_schema` |
| `stream_options.include_usage` | OpenAI Chat | 大部分厂商 | 上游不返回时估算 |
| `parallel_tool_calls` | OpenAI | Anthropic、Mistral | 丢弃 |
| `tools[].type: "function"` | OpenAI | Gemini（不需要 type） | 包装层去除 |
| `tools[].type: "web_search"` | OpenAI Responses | 仅 OpenAI | 降级 + 警告 |
| `tools[].type: "file_search"` | OpenAI Responses | 仅 OpenAI | 降级 + 警告 |
| `tools[].type: "code_interpreter"` | OpenAI Responses | 仅 OpenAI | 降级 + 警告 |
| `previous_response_id` | OpenAI Responses | 仅 OpenAI | 客户端回退发完整 input |
| `thinking.type` | Anthropic | OpenAI、Gemini | 降级（Qwen/MiniMax/GLM 部分支持 reasoning） |
| `thinking.keep` | Anthropic | 全部 | 丢弃 |
| `cache_control` | Anthropic | 全部 | 丢弃（DeepSeek 等有私有缓存） |
| `context_management` | Anthropic | 全部 | 丢弃 |
| `metadata.user_id` | Anthropic | OpenAI | 映射到 `user` |
| `stop_sequences` | Anthropic | OpenAI（字段名 `stop`） | 重命名 |
| `top_k` | Anthropic / vLLM / Ollama | OpenAI | 丢弃 |

## 流式兼容性

| 客户端期望 | 厂商流式 | 网关处理 |
|------------|----------|----------|
| OpenAI SSE (`data: ...` + `data: [DONE]`) | OpenAI SSE | 直通 |
| OpenAI SSE | Anthropic SSE (event-based) | 转换为 OpenAI SSE |
| OpenAI SSE | Gemini SSE (`alt=sse`) | 转换为 OpenAI SSE |
| OpenAI SSE | Gemini JSON 数组（默认） | 包装为 SSE |
| OpenAI SSE | DeepSeek/Qwen/GLM SSE | 直通 |
| OpenAI SSE | Ollama NDJSON | 转换为 SSE |
| Anthropic SSE (event-based) | OpenAI SSE | 转换为 Anthropic SSE |
| Anthropic SSE | Gemini SSE | 转换为 Anthropic SSE |
| Anthropic SSE | Ollama NDJSON | 转换为 Anthropic SSE |

## 错误兼容

| 客户端期望 | 厂商错误 | 网关处理 |
|------------|----------|----------|
| OpenAI `{"error": {"message", "type", "code"}}` | OpenAI `{"error": {...}}` | 直通 |
| OpenAI | Anthropic `{"type": "error", "error": {...}}` | 转换为 OpenAI 风格 |
| OpenAI | Gemini `{"error": {...}}` | 直通 |
| Anthropic `{"type": "error", "error": {"type", "message"}}` | Anthropic | 直通 |
| Anthropic | OpenAI `{"error": {...}}` | 转换为 Anthropic 风格 |
| Anthropic | Gemini | 转换为 Anthropic 风格 |
| HTTP 4xx/5xx | 各厂商各异 | 网关按语义分类，统一返回 |
