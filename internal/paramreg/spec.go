package paramreg

import (
	"encoding/json"
	"strings"
)

// Kind 决定一个字段的还原策略。
type Kind uint8

const (
	// KindIRHandled 表示 IR 有对应规范字段，parse/serialize 负责转换。
	// 这类字段不该出现在 Extensions 里；若出现说明 parse 侧漏了，
	// 决策返回 ActionSkip（不是丢失，序列化器已处理）。
	//
	// 警告（domains/transformation/fields.go:8-13 记录的真实事故）：
	// 只有 internal/ir 确实 parse+serialize 的字段才能标 KindIRHandled。
	// 曾把 stream_options / metadata 误标为已处理，导致它们既不进
	// Extensions 也不被序列化，流式 usage 帧到不了上游。
	KindIRHandled Kind = iota

	// KindPortable 表示语义跨厂商通用、可安全跨方言透传的字段。
	// 例如 repetition_penalty：多家 OpenAI 兼容厂商都认，IR 无对应字段。
	KindPortable

	// KindDialectOnly 表示方言私有字段，仅目标方言匹配时还原。
	// 例如 Anthropic 的 context_management —— 泄漏给 Mistral 会硬 400。
	KindDialectOnly

	// KindTranslatable 表示有跨方言映射函数的字段。
	KindTranslatable
)

// TranslateFunc 把字段值从源方言转换到目标方言。
//
// 返回 (newKey, newValue, ok)：
//   - ok=false 表示该 src→dst 组合无映射，调用方按 ActionDrop 处理
//   - newKey 可与原 key 不同（如 max_completion_tokens → max_tokens）
type TranslateFunc func(value json.RawMessage, src, dst Dialect) (string, json.RawMessage, bool)

// FieldSpec 描述一个已登记参数。
type FieldSpec struct {
	// Name 是规范字段名（线格式上的 JSON key）。
	Name string

	// Aliases 是等价的其它写法。查表时一并匹配。
	Aliases []string

	// Kind 决定还原策略。
	Kind Kind

	// Dialects 是认识该字段的方言集合。
	// KindDialectOnly 时作为还原白名单；KindPortable 时仅供审计。
	Dialects []Dialect

	// RejectedBy 是会因该字段硬报错的方言。优先级高于一切其它规则。
	//
	// 例：Grok 推理模型收到 presence_penalty / frequency_penalty / stop 直接报错；
	// Mistral 的 additionalProperties:false 会拒绝任何未知键。
	RejectedBy []Dialect

	// IRPath 指向 IR 中的对应字段（KindIRHandled 时填），便于审计与排查。
	IRPath string

	// Translate 是跨方言转换函数（KindTranslatable 时填）。
	Translate TranslateFunc

	// Note 记录来源与约束。一手来源索引见
	// docs/参数全量兼容/01-审计基线与研究结论.md 第 4 节。
	Note string
}

// dialectSet 把 Dialects 展开为查询用集合，含底层协议继承。
//
// 例：某字段 Dialects=[DialectOpenAIChat]，则 DeepSeek/GLM/Qwen 等
// OpenAI 兼容方言也应视为认识它（它们的 BaseProtocol 是 openai_chat）。
func (s *FieldSpec) knownBy(d Dialect) bool {
	if d == DialectUnknown {
		// 未知目标方言：走最宽松路径，避免静默丢参数。
		return true
	}
	for _, allowed := range s.Dialects {
		if allowed == d {
			return true
		}
		// 方言继承：目标方言的底层协议在白名单内也算认识。
		if allowed == BaseProtocol(d) {
			return true
		}
	}
	return false
}

// rejects 报告目标方言是否明确拒绝该字段。
func (s *FieldSpec) rejects(d Dialect) bool {
	if d == DialectUnknown {
		return false
	}
	for _, bad := range s.RejectedBy {
		if bad == d {
			return true
		}
	}
	return false
}

// normalizeKey 归一化字段名以便查表：小写 + 去首尾空白。
//
// 不做下划线/驼峰互转 —— Gemini 的 topK 与 OpenAI 的 top_k 是不同线格式上的
// 不同字段，混淆会导致错误还原。跨形态映射由 Aliases 显式登记。
func normalizeKey(k string) string {
	return strings.ToLower(strings.TrimSpace(k))
}
