package paramreg

// specs 是参数全集注册表。
//
// 数据来源（一手，见 docs/参数全量兼容/01-审计基线与研究结论.md 第 4 节）：
//   - OpenAI:    openai/openai-openapi openapi.yaml spec v2.3.0
//   - Anthropic: anthropics/anthropic-sdk-python types/message_create_params.py（Stainless 由 OpenAPI 生成）
//   - Gemini:    googleapis generative_service.proto + js-genai src/types.ts
//   - 其余厂商:  各自官方文档 / OpenAPI / SDK
//
// 维护规则：
//  1. KindIRHandled 必须确认 internal/ir 真的 parse + serialize 了该字段，
//     否则会重演 stream_options 静默丢失事故（fields.go:8-13）。
//  2. 未登记 = 未知字段 = 无条件透传。所以"拿不准就不登记"是安全的默认。
//  3. 已从厂商现行 spec 移除的历史字段仍需登记（老客户端还在发），Note 里标注。
var specs = []FieldSpec{
	// ═══════════════════════════════════════════════════════════
	// 跨厂商通用 —— IR 已处理
	// ═══════════════════════════════════════════════════════════
	{Name: "model", Kind: KindIRHandled, IRPath: "Model"},
	{Name: "messages", Kind: KindIRHandled, IRPath: "Messages"},
	{Name: "stream", Kind: KindIRHandled, IRPath: "Stream"},
	{Name: "temperature", Kind: KindIRHandled, IRPath: "Temperature",
		Note: "上限各异：Anthropic 1.0 / Mistral 1.5 / Kimi 1.0 / 其余 2.0。thinking 启用时 Anthropic 要求为 1 或不传"},
	{Name: "top_p", Kind: KindIRHandled, IRPath: "TopP"},
	{Name: "tools", Kind: KindIRHandled, IRPath: "Tools"},
	{Name: "tool_choice", Kind: KindIRHandled, IRPath: "ToolChoice",
		RejectedBy: nil, Note: "GLM 只接受 auto；Mistral 额外支持非 OpenAI 的 any"},
	{Name: "max_tokens", Kind: KindIRHandled, IRPath: "MaxTokens",
		Note: "OpenAI 已废弃且与 o 系列不兼容，须改用 max_completion_tokens"},
	{Name: "user", Kind: KindIRHandled, IRPath: "User",
		Note: "OpenAI 已废弃 → safety_identifier + prompt_cache_key；vLLM 接受但忽略"},

	// top_k：Anthropic/Gemini 原生；OpenAI Chat 无此字段但多数兼容厂商支持
	{Name: "top_k", Kind: KindIRHandled, IRPath: "TopK",
		Dialects: []Dialect{DialectAnthropic, DialectGemini, DialectOpenAIChat},
		Note:     "OpenAI 官方无此字段；DeepSeek/Qwen/GLM/vLLM/OpenRouter 等兼容厂商支持"},

	{Name: "stop", Aliases: []string{"stop_sequences"}, Kind: KindIRHandled, IRPath: "Stop",
		RejectedBy: []Dialect{DialectGrok},
		Note:       "Grok 推理模型上硬报错；OpenAI o3/o4-mini 不支持；GLM 最多 4 个但只生效 1 个"},

	// ═══════════════════════════════════════════════════════════
	// OpenAI Chat Completions
	// ═══════════════════════════════════════════════════════════
	{Name: "max_completion_tokens", Kind: KindIRHandled, IRPath: "MaxTokens",
		Dialects: []Dialect{DialectOpenAIChat, DialectResponses},
		Note:     "IR 折叠进 MaxTokens（parse_openai.go:154-158），恒以 max_tokens 重新发出"},
	{Name: "frequency_penalty", Kind: KindIRHandled, IRPath: "FrequencyPenalty",
		RejectedBy: []Dialect{DialectGrok},
		Note:       "Grok 推理模型硬报错；DeepSeek 已废弃且静默忽略；MiniMax 静默忽略"},
	{Name: "presence_penalty", Kind: KindIRHandled, IRPath: "PresencePenalty",
		RejectedBy: []Dialect{DialectGrok},
		Note:       "同 frequency_penalty"},
	{Name: "logprobs", Kind: KindIRHandled, IRPath: "Logprobs"},
	{Name: "top_logprobs", Kind: KindIRHandled, IRPath: "TopLogprobs"},
	{Name: "logit_bias", Kind: KindIRHandled, IRPath: "LogitBias",
		Note: "MiniMax 静默忽略"},
	{Name: "response_format", Kind: KindIRHandled, IRPath: "ResponseFormat"},
	{Name: "seed", Kind: KindIRHandled, IRPath: "Seed",
		Note: "OpenAI 已标记 Deprecated；Mistral 用 random_seed"},
	{Name: "n", Kind: KindIRHandled, IRPath: "N",
		Note: "MiniMax 必须为 1；Qwen 仅 qwen-plus 支持且带 tools 时强制 1"},
	{Name: "parallel_tool_calls", Kind: KindIRHandled, IRPath: "ParallelToolCalls"},
	{Name: "store", Kind: KindIRHandled, IRPath: "Store",
		Note: "Chat Completions 默认 false，Responses 默认 true"},
	{Name: "service_tier", Kind: KindIRHandled, IRPath: "ServiceTier",
		Note: "OpenAI auto|default|flex|scale|priority|fast；Anthropic 仅 auto|standard_only；Ark auto|default"},
	{Name: "modalities", Kind: KindIRHandled, IRPath: "Modalities"},
	{Name: "audio", Kind: KindIRHandled, IRPath: "AudioConfig"},
	{Name: "prediction", Kind: KindIRHandled, IRPath: "Prediction"},
	{Name: "verbosity", Kind: KindIRHandled, IRPath: "Verbosity",
		Note: "Chat Completions 顶层；Responses 嵌在 text.verbosity"},
	{Name: "web_search_options", Kind: KindIRHandled, IRPath: "WebSearchOptions"},
	{Name: "prompt_cache_key", Kind: KindIRHandled, IRPath: "PromptCacheKey",
		Note: "Codex CLI 依赖此字段做缓存命中，丢弃会显著抬升成本"},
	{Name: "safety_identifier", Kind: KindIRHandled, IRPath: "SafetyIdentifier"},
	{Name: "truncation", Kind: KindIRHandled, IRPath: "Truncation"},
	{Name: "previous_response_id", Kind: KindIRHandled, IRPath: "PreviousResponseID"},

	// stream_options：IR 零处理（fields.go:8-13 事故），必须靠透传
	{Name: "stream_options", Kind: KindPortable,
		Dialects: []Dialect{DialectOpenAIChat, DialectResponses},
		Note:     "IR 不 parse/serialize；executor.go:5089 事后注入兜底。OpenRouter 的 include_usage 已废弃为 no-op"},

	{Name: "metadata", Kind: KindPortable,
		Note: "Anthropic 仅保留 metadata.user_id（parse_anthropic.go:118-124）；Claude Code 发的是 JSON 编码字符串"},
	{Name: "moderation", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenAIChat, DialectResponses}},
	{Name: "prompt_cache_options", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenAIChat, DialectResponses},
		Note:     "gpt-5.6+；ttl 仅 30m；mode implicit|explicit"},
	{Name: "prompt_cache_retention", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenAIChat, DialectResponses},
		Note:     "已废弃 → prompt_cache_options.ttl"},
	{Name: "function_call", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenAIChat}, Note: "已废弃 → tool_choice"},
	{Name: "functions", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenAIChat}, Note: "已废弃 → tools"},

	// ═══════════════════════════════════════════════════════════
	// OpenAI Responses API
	// ═══════════════════════════════════════════════════════════
	{Name: "input", Kind: KindIRHandled, IRPath: "Messages",
		Dialects: []Dialect{DialectResponses}},
	{Name: "instructions", Kind: KindIRHandled, IRPath: "System",
		Dialects: []Dialect{DialectResponses}},
	{Name: "include", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses},
		Note:     "Codex CLI 恒发 [reasoning.encrypted_content]；丢弃会静默破坏无状态多轮推理"},
	{Name: "text", Kind: KindIRHandled, IRPath: "ResponseFormat/Verbosity",
		Dialects: []Dialect{DialectResponses}},
	{Name: "max_output_tokens", Kind: KindIRHandled, IRPath: "MaxTokens",
		Dialects: []Dialect{DialectResponses}},
	{Name: "max_tool_calls", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses}},
	{Name: "background", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses}},
	{Name: "conversation", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses},
		Note:     "与 previous_response_id 互斥"},
	{Name: "prompt", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses},
		Note:     "可复用 prompt 模板 {id,version,variables}"},
	{Name: "client_metadata", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectResponses},
		Note:     "Codex CLI 发送 session_id/thread_id/turn_id/installation_id/window_id"},

	// ═══════════════════════════════════════════════════════════
	// Anthropic Messages
	// ═══════════════════════════════════════════════════════════
	{Name: "system", Kind: KindIRHandled, IRPath: "System",
		Dialects: []Dialect{DialectAnthropic},
		Note:     "Claude Code 发数组形态且带 cache_control；不可擅自增删 block（会 400）"},
	{Name: "cache_control", Kind: KindIRHandled, IRPath: "CacheControl",
		Dialects: []Dialect{DialectAnthropic},
		Note:     "ttl 5m|1h；scope global。executor.go:5117 另有按会话注入"},
	{Name: "documents", Kind: KindIRHandled, IRPath: "Documents",
		Dialects: []Dialect{DialectAnthropic}},
	{Name: "mcp_servers", Kind: KindIRHandled, IRPath: "MCPServers",
		Dialects: []Dialect{DialectAnthropic}},
	{Name: "context_management", Kind: KindIRHandled, IRPath: "ContextManagement",
		Dialects: []Dialect{DialectAnthropic, DialectResponses},
		Note:     "Anthropic edits[] 三种变体；Responses 侧只支持 compaction。语义不同，勿混用"},
	{Name: "container", Kind: KindIRHandled, IRPath: "Container",
		Dialects: []Dialect{DialectAnthropic},
		Note:     "GA 是裸字符串，beta 形态是对象 {id,skills}"},
	{Name: "betas", Kind: KindPortable,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "Claude Code 作为 body 数组发送，SDK 会转 anthropic-beta header。取值每月变动，须原样透传不可白名单过滤"},
	{Name: "output_config", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "GA：effort(low|medium|high|xhigh|max) + format；beta 追加 task_budget"},
	{Name: "output_format", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "已废弃 → output_config.format"},
	{Name: "speed", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "standard|fast，需 fast-mode-2026-02-01 beta。Claude Code 会发 fast"},
	{Name: "inference_geo", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic}},
	{Name: "diagnostics", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "previous_message_id，用于 prompt cache 偏离诊断"},
	{Name: "fallbacks", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "服务端重试链。未登记的键会在 Anthropic 侧 parse 时拒绝"},
	{Name: "fallback_credit_token", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectAnthropic},
		Note:     "5 分钟内有效，跨组织/工作区不可用"},

	// ═══════════════════════════════════════════════════════════
	// Gemini generateContent
	// ═══════════════════════════════════════════════════════════
	{Name: "contents", Kind: KindIRHandled, IRPath: "Messages",
		Dialects: []Dialect{DialectGemini}},
	{Name: "systemInstruction", Kind: KindIRHandled, IRPath: "System",
		Dialects: []Dialect{DialectGemini}},
	{Name: "toolConfig", Kind: KindIRHandled, IRPath: "ToolChoice",
		Dialects: []Dialect{DialectGemini}},
	{Name: "generationConfig", Kind: KindIRHandled, IRPath: "(多字段)",
		Dialects: []Dialect{DialectGemini}},
	{Name: "safetySettings", Kind: KindIRHandled, IRPath: "SafetySettings",
		Dialects: []Dialect{DialectGemini},
		Note:     "P3 修复前是静默丢失：列入 knownFields 却从不赋值给 IR，也无 loss 事件"},
	{Name: "cachedContent", Kind: KindIRHandled, IRPath: "CachedContent",
		Dialects: []Dialect{DialectGemini},
		Note:     "同 safetySettings，P3 修复"},
	{Name: "labels", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectGemini},
		Note:     "仅 Vertex AI；Gemini Developer API 的 converter 收到会直接抛错"},
	{Name: "serviceTier", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectGemini},
		Note:     "unspecified|flex|standard|priority。proto 里没有，仅 REST 文档 + SDK"},

	// ═══════════════════════════════════════════════════════════
	// 推理/思考 —— 详见 internal/ir/reasoning.go 的归一化层（P5）
	// ═══════════════════════════════════════════════════════════
	//
	// 注意（2026-08-11）：这些字段在 P5 之前只有部分 dialect 的 parser 处理了它们。
	// 例如 thinking 只有 parse_anthropic 认识，parse_openai 会把它放进 Extensions。
	// 因此 enable_thinking / thinking_budget / thinking_token_budget 在 P5 完成之前
	// 必须是 KindPortable（透传），否则会重演 stream_options 静默丢失事故。
	// reasoning_effort / thinking / reasoning 已有对应的 IR parse + serialize，标为
	// KindIRHandled 是正确的（ActionRestore 兜底保证万无一失）。
	{Name: "reasoning_effort", Kind: KindIRHandled, IRPath: "Reasoning.Effort",
		Note: "枚举各厂商不一致：DeepSeek low|high|max / GLM 7 档 / Grok none|low|medium|high / Ark minimal|low|medium|high。须按目标能力收窄"},
	{Name: "reasoning", Kind: KindIRHandled, IRPath: "Reasoning",
		Note: "OpenAI Responses {effort,summary,context,mode}；OpenRouter {effort,max_tokens,exclude,enabled}，其中 effort 与 max_tokens 互斥"},
	{Name: "thinking", Kind: KindIRHandled, IRPath: "Thinking/Reasoning",
		Note: "取值分歧：Anthropic enabled|adaptive|disabled / DeepSeek·GLM·Kimi enabled|disabled / Ark 多 auto / MiniMax 用 disabled|adaptive（无 enabled）"},

	// P5 之前：enable_thinking / thinking_budget / thinking_token_budget
	// 是 KindPortable（IR 尚未统一处理，须靠 Extensions 透传）。
	// P5 完成后将改为 KindIRHandled。
	{Name: "enable_thinking", Kind: KindPortable,
		Dialects: []Dialect{DialectQwen, DialectVLLM},
		Note:     "Qwen 顶层布尔；开源权重模型带 thinking 时只能流式调用。P5 完成前须 Portable"},
	{Name: "thinking_budget", Kind: KindPortable,
		Dialects: []Dialect{DialectQwen},
		Note:     "Qwen 顶层整数，配合 enable_thinking。P5 完成前须 Portable"},
	{Name: "thinking_token_budget", Kind: KindPortable,
		Dialects: []Dialect{DialectVLLM},
		Note:     "vLLM；-1 表示不限。P5 完成前须 Portable"},
	{Name: "chat_template_kwargs", Kind: KindPortable,
		Dialects: []Dialect{DialectVLLM},
		Note:     "vLLM 托管开源模型经 {\"enable_thinking\":bool} 控制思考；整体透传，思考语义由 reasoning 归一化层另行处理"},
	{Name: "include_reasoning", Kind: KindPortable,
		Dialects: []Dialect{DialectVLLM},
		Note:     "vLLM 默认 true"},
	{Name: "preserve_thinking", Kind: KindPortable,
		Dialects: []Dialect{DialectQwen},
		Note:     "Qwen 思考历史保留"},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— DeepSeek
	// ═══════════════════════════════════════════════════════════
	{Name: "prefix", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectDeepSeek},
		Note:     "assistant 消息续写，需 /beta base URL"},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— Qwen / DashScope
	// ═══════════════════════════════════════════════════════════
	{Name: "enable_search", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},
	{Name: "search_options", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},
	{Name: "result_format", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},
	{Name: "incremental_output", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},
	{Name: "translation_options", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},
	{Name: "vl_high_resolution_images", Kind: KindDialectOnly, Dialects: []Dialect{DialectQwen}},

	// repetition_penalty：多厂商通用，但网关当前**完全无处理**（审计 1.6）
	{Name: "repetition_penalty", Kind: KindPortable,
		Dialects: []Dialect{DialectQwen, DialectGLM, DialectArk, DialectVLLM, DialectOpenRouter},
		Note:     "网关此前零处理。Qwen/GLM/Ark/vLLM/OpenRouter 均支持"},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— GLM / 智谱
	// ═══════════════════════════════════════════════════════════
	{Name: "do_sample", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectGLM},
		Note:     "false 会禁用 temperature/top_p"},
	{Name: "request_id", Kind: KindDialectOnly, Dialects: []Dialect{DialectGLM}},
	{Name: "user_id", Kind: KindDialectOnly, Dialects: []Dialect{DialectGLM}},
	{Name: "tool_stream", Kind: KindDialectOnly, Dialects: []Dialect{DialectGLM}},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— MiniMax
	// ═══════════════════════════════════════════════════════════
	{Name: "reasoning_split", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax},
		Note:     "true 才拆分 reasoning_content"},
	{Name: "mask_sensitive_info", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax},
		Note:     "已从现行 OpenAPI spec 移除；保留登记以兼容老客户端"},
	{Name: "reply_constraints", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax}, Note: "同上，已移除"},
	{Name: "bot_setting", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax}, Note: "同上，已移除"},
	{Name: "tokens_to_generate", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax}, Note: "同上，已移除"},
	{Name: "aigc_watermark", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMiniMax}, Note: "同上，已移除"},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— Grok / xAI
	// ═══════════════════════════════════════════════════════════
	{Name: "search_parameters", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectGrok},
		Note:     "mode off|on|auto；allowed_websites 最多 5 个且与 excluded_websites 互斥"},
	{Name: "deferred", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectGrok},
		Note:     "轮询 GET /v1/chat/deferred-completion/{request_id}"},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— Mistral
	// ═══════════════════════════════════════════════════════════
	{Name: "safe_prompt", Kind: KindDialectOnly, Dialects: []Dialect{DialectMistral}},
	{Name: "random_seed", Kind: KindTranslatable,
		Dialects: []Dialect{DialectMistral}, Translate: translateRandomSeed,
		Note: "Mistral 的 seed 写法"},
	{Name: "prompt_mode", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectMistral}, Note: "唯一取值 reasoning"},
	{Name: "guardrails", Kind: KindDialectOnly, Dialects: []Dialect{DialectMistral}},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— OpenRouter
	// ═══════════════════════════════════════════════════════════
	{Name: "provider", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenRouter}, Note: "provider 偏好路由"},
	{Name: "models", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenRouter}, Note: "备选模型列表"},
	{Name: "route", Kind: KindDialectOnly, Dialects: []Dialect{DialectOpenRouter}},
	{Name: "plugins", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenRouter}, Note: "约 11 种插件，含 context-compression"},
	{Name: "transforms", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectOpenRouter},
		Note:     "已下线，改为 context-compression 插件；保留登记兼容老客户端"},
	{Name: "min_p", Kind: KindPortable,
		Dialects: []Dialect{DialectOpenRouter, DialectVLLM}},
	{Name: "top_a", Kind: KindDialectOnly, Dialects: []Dialect{DialectOpenRouter}},
	{Name: "session_id", Kind: KindPortable,
		Dialects: []Dialect{DialectOpenRouter, DialectVLLM}},
	{Name: "trace", Kind: KindDialectOnly, Dialects: []Dialect{DialectOpenRouter}},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— vLLM
	// ═══════════════════════════════════════════════════════════
	{Name: "structured_outputs", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectVLLM},
		Note:     "main 分支已合并 guided_json/regex/grammar/choice 到此字段"},
	{Name: "guided_json", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectVLLM}, Note: "main 已删除，保留兼容老客户端"},
	{Name: "guided_regex", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}, Note: "同上"},
	{Name: "guided_grammar", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}, Note: "同上"},
	{Name: "guided_choice", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}, Note: "同上"},
	{Name: "structural_tag", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "add_generation_prompt", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "continue_final_message", Kind: KindDialectOnly,
		Dialects: []Dialect{DialectVLLM}, Note: "与 add_generation_prompt 冲突"},
	{Name: "echo", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "min_tokens", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "stop_token_ids", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "bad_words", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "ignore_eos", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "include_stop_str_in_output", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "skip_special_tokens", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "spaces_between_special_tokens", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "truncate_prompt_tokens", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "truncation_side", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "prompt_logprobs", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "logprob_token_ids", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "allowed_token_ids", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "return_token_ids", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "cache_salt", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "kv_transfer_params", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "ec_transfer_params", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "vllm_xargs", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "repetition_detection", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "stream_interval", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "priority", Kind: KindPortable,
		Dialects: []Dialect{DialectVLLM}},
	{Name: "best_of", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "use_beam_search", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},
	{Name: "length_penalty", Kind: KindDialectOnly, Dialects: []Dialect{DialectVLLM}},

	// ═══════════════════════════════════════════════════════════
	// 厂商私有 —— Ollama
	// ═══════════════════════════════════════════════════════════
	//
	// Ollama 使用 openai-chat 线格式，所以 DialectOllama 的 BaseProtocol 是
	// openai_chat。当 TargetProvider 未设置时（目标 catalog 未知），
	// dst 会被解析为 DialectOpenAIChat，而 KindDialectOnly 只允许 DialectOllama，
	// 导致这些字段被错误裁剪。
	//
	// 正确处理：Ollama 私有字段标为 KindPortable。理由：Ollama 服务端对不认识的
	// 字段会静默忽略，不会报错；而这些字段是操作 Ollama 行为的唯一途径
	// （context 用于多轮续写，keep_alive 控制模型驻留，format 控制输出格式）。
	// 如需阻止它们到达其它上游，在 strip_request_fields 里配置。
	{Name: "keep_alive", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
	{Name: "format", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
	{Name: "options", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
	{Name: "think", Kind: KindPortable,
		Dialects: []Dialect{DialectOllama}, Note: "Ollama 的思考开关"},
	{Name: "context", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
	{Name: "raw", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
	{Name: "template", Kind: KindPortable, Dialects: []Dialect{DialectOllama}},
}
