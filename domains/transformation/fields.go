package transformation

// standardRequestFields 是 IR 已知处理的顶层请求字段（OpenAI + Anthropic 共集）。
//
// 这些字段会被 IR 的 Parse/Serialize 正确转换，不需要存入 ExtensionsBag。
// 任何不在该集合内的顶层字段视为"扩展属性"，需要靠 ExtensionsBag 往返保留。
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
	"stream_options":      true,
	"parallel_tool_calls": true,
	"store":               true,
	"metadata":            true,
	"reasoning_effort":    true,

	// Anthropic 特有
	"system":         true,
	"stop_sequences": true,
	"thinking":       true,
}

// isStandardField 报告字段名是否是 IR 已知处理的标准字段。
func isStandardField(field string) bool {
	return standardRequestFields[field]
}
