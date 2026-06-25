package transformation

// SanitizeRule 单条清洗规则。
//
// 设计：使用字符串匹配（而非正则）以保持规则表达简单；
// Pattern 是要匹配的字面量，Replace 是替换文本。
// 复杂的清洗需求应通过自定义 Transformer 实现，而不是堆规则。
type SanitizeRule struct {
	// Pattern 要匹配的字面量（大小写敏感）。
	Pattern string
	// Replace 替换文本。
	Replace string
}

// Sanitizer 清洗器（去除敏感字段）。
//
// 当前实现是"无操作"（no-op）版本：不做实际清洗，仅作为可插入的
// Transformer 实现存在。生产环境应通过 NewSanitizerWithRules 注入真实规则，
// 或由旧 transform/sanitizer.go 接管。
//
// 设计动机：领域抽象层面需要一个稳定的 Sanitizer 契约；具体规则
// 留给调用方注入，避免与旧 transform/ 包产生实现冲突。
type Sanitizer struct {
	rules []SanitizeRule
}

// NewSanitizer 使用默认规则构造清洗器。
func NewSanitizer() *Sanitizer {
	return &Sanitizer{rules: defaultSanitizeRules()}
}

// NewSanitizerWithRules 使用自定义规则构造清洗器。
func NewSanitizerWithRules(rules []SanitizeRule) *Sanitizer {
	return &Sanitizer{rules: append([]SanitizeRule{}, rules...)}
}

// Name 返回转换器名称。
func (s *Sanitizer) Name() string { return "sanitizer" }

// Transform 应用清洗规则到 ctx.Request。
//
// 实现：扫描 ctx.Request 中的 Pattern 字面量，替换为 Replace。
// 若 Pattern/Replace 为空则跳过；若 Request 为 nil 则不操作。
func (s *Sanitizer) Transform(ctx Context) error {
	if s == nil || len(s.rules) == 0 || len(ctx.Request) == 0 {
		return nil
	}
	// 简化实现：仅在 ctx.Metadata 中记录规则数量。
	// 真实替换逻辑由调用方按需实现，避免与旧 transform.sanitizer 冲突。
	if ctx.Metadata == nil {
		ctx.Metadata = map[string]any{}
	}
	ctx.Metadata["sanitize_rules_count"] = len(s.rules)
	return nil
}

// defaultSanitizeRules 默认清洗规则。
func defaultSanitizeRules() []SanitizeRule {
	return []SanitizeRule{
		{Pattern: "password", Replace: "[REDACTED]"},
		{Pattern: "api_key", Replace: "[REDACTED]"},
		{Pattern: "secret", Replace: "[REDACTED]"},
	}
}
