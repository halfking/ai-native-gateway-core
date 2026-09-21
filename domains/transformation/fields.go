package transformation

import (
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// standardRequestFields 是 IR 已知处理的顶层请求字段（OpenAI + Anthropic 共集）。
//
// 这些字段会被 IR 的 Parse/Serialize 正确转换，不需要存入 ExtensionsBag。
// 任何不在该集合内的顶层字段视为"扩展属性"，需要靠 ExtensionsBag 往返保留。
//
// 注意（2026-07-27 审计 F-2）：只有 IR 真正在 parse_openai/serialize_openai 里
// 处理的字段才能放进这个集合。曾经误列的 stream_options / metadata 实际上 IR
// 从不 parse/serialize（internal/ir 零引用），导致它们在 IR 路由下被静默丢弃
// （stream_options 丢失会让 stream_options.include_usage 流式 usage 帧不到达上游）。
// 现已移出，改由 ExtensionsBag 在同协议路由上透传往返保留。新增字段前请先确认
// internal/ir 确有对应处理，否则走 ExtensionsBag 透传。
var standardRequestFields = map[string]bool{
	// 公共字段
	"model":       true,
	"messages":    true,
	"max_tokens":  true,
	"temperature": true,
	"top_p":       true,
	"top_k":       true,
	"stream":      true,
	"stop":        true,
	"tools":       true,
	"tool_choice": true,
	"user":        true,
	"n":           true,

	// OpenAI 特有
	"frequency_penalty":   true,
	"presence_penalty":    true,
	"logit_bias":          true,
	"logprobs":            true,
	"top_logprobs":        true,
	"response_format":     true,
	"seed":                true,
	"service_tier":        true,
	"parallel_tool_calls": true,
	"store":               true,
	"reasoning_effort":    true,

	// audit-provider-multimodal (2026-07-13): Personalized provider fields
	"modalities":           true,
	"audio":                true,
	"prediction":           true,
	"verbosity":            true,
	"web_search_options":   true,
	"prompt_cache_key":     true,
	"safety_identifier":    true,
	"previous_response_id": true,
	"truncation":           true,

	// Anthropic 特有
	"system":         true,
	"stop_sequences": true,
	"thinking":       true,
	"cache_control":  true,
	"documents":      true,

	// 注意：Ollama 特有字段（keep_alive, format, context, raw, template）
	// 当前未在此列表中，因此会被 Extensions 机制捕获并透传。
	// IR 核心不处理这些字段，依赖 Extensions 往返保留。
}

// isStandardField 报告字段名是否是 IR 已知处理的标准字段。
//
// 2026-08-11：数据源改为 internal/paramreg。此前本文件的 standardRequestFields
// 与 internal/ir/parse_*.go 各自的 knownFields 是两套并行清单，漂移后造成
// stream_options / metadata 被静默丢弃的真实事故（见本文件顶部注释）。
// 现由注册表统一供给，从结构上消除这类漂移。
//
// standardRequestFields 保留作为 PARAMREG_ENABLED=false 的回退路径。
func isStandardField(field string) bool {
	if !paramregEnabled() {
		return standardRequestFields[field]
	}
	return irHandledFields()[strings.ToLower(strings.TrimSpace(field))]
}

// standardResponseFields 是 IR 响应解析器(ParseOpenAIResponse / ParseAnthropic-
// Response)与序列化器(Serialize*Response)实际消费/产出的顶层响应字段。
//
// 2026-09-21 R50 根修: extractResponseExtensions 此前复用请求字段白名单,导致
// 源协议的标准响应字段(choices/created/object 等)被当"扩展"回填到跨协议序列化
// 产物上——OpenAI 上游 → Anthropic 客户端时,最终 body 同时带 choices(还原)与
// content(IR),下游 classify 先命中 choices 再按 OpenAI 重新转换,usage 里的
// prompt_tokens 键不存在(被 IR 换成了 input_tokens),归零。响应侧提取必须
// 按本集合过滤:这些字段两个方向的序列化器都会自己写,根本不需要扩展往返;
// 留给 Extensions 的只剩厂商私有/自定义字段(如 base_resp 前已被 vendorstrip
// 清理的那类之外的真正非标键)。
var standardResponseFields = map[string]bool{
	// 共有
	"id": true, "model": true, "usage": true, "created": true,
	// OpenAI chat completion
	"object": true, "choices": true, "service_tier": true, "system_fingerprint": true,
	// Anthropic message
	"type": true, "role": true, "content": true,
	"stop_reason": true, "stop_sequence": true, "container": true,
}

// isStandardResponseField 报告字段名是否是响应体的协议标准字段。
func isStandardResponseField(field string) bool {
	return standardResponseFields[field]
}

// irHandledFields 缓存注册表的 IR 已处理字段集合。
var irHandledFields = sync.OnceValue(paramreg.IRHandledFields)
