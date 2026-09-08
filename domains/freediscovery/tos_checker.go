package freediscovery

import "strings"

// ToSChecker 基于关键词规则的 ToS 合规初判.
// MVP 采用保守策略: 无法判定 → ambiguous (需人工审查), 绝不猜 ok.
type ToSChecker struct {
	rules map[string]*ToSRule
}

// ToSRule 单个提供商的 ToS 判定规则.
type ToSRule struct {
	ProviderCode string
	// ModelID 关键词分类 (小写匹配)
	AvoidKeywords   []string // 命中 → avoid (如 vision model ToS 禁止自动化)
	CautionKeywords []string // 命中 → caution (如 vision/experimental)
	AllowKeywords   []string // 命中 → ok (如 :free / free 官方标记)
	// ProviderVerdict 提供商级兜底判定 (模板 tos_verdict 优先于此)
	ProviderVerdict string
	Notes           string
}

// NewToSChecker 构造检查器, 自动从内置预设装载规则.
func NewToSChecker() *ToSChecker {
	c := &ToSChecker{rules: make(map[string]*ToSRule)}
	for code, p := range builtinPresets {
		c.rules[code] = &ToSRule{
			ProviderCode:    code,
			AllowKeywords:   []string{":free", "-free", "free-"},
			CautionKeywords: []string{"vision", "preview", "experimental", "beta"},
			AvoidKeywords:   []string{"discontinued", "deprecated"},
			ProviderVerdict: p.TosVerdict,
			Notes:           p.TosNotes,
		}
	}
	return c
}

// Check 对单个模型做 ToS 初判. 模板已有 verdict 时优先采用模板值.
func (c *ToSChecker) Check(tpl *ProviderTemplate, modelID string) (verdict, notes string) {
	if tpl != nil && tpl.TosVerdict != "" && tpl.TosVerdict != "unknown" {
		// 模板级判定优先 (人工审查结论), 但 avoid 关键词仍可降级
		verdict = tpl.TosVerdict
		notes = tpl.TosNotes
	}

	id := strings.ToLower(modelID)
	rule := c.rules[tplProviderCode(tpl)]
	if rule == nil {
		if verdict == "" {
			return "unknown", "no ToS rule for provider"
		}
		return verdict, notes
	}

	if containsAny(id, rule.AvoidKeywords) {
		return "avoid", "model id contains " + strings.Join(rule.AvoidKeywords, "/") + " keyword"
	}
	if containsAny(id, rule.CautionKeywords) {
		// caution 关键词命中: 有模板级 ok 时降级为 caution
		if verdict == "ok" {
			return "caution", "downgraded from template verdict: model id contains caution keyword"
		}
		if verdict == "" {
			return "caution", "model id contains caution keyword"
		}
	}
	if verdict == "" {
		if containsAny(id, rule.AllowKeywords) {
			if rule.ProviderVerdict == "avoid" || rule.ProviderVerdict == "caution" {
				// 提供商级判定更保守时, 采用提供商级
				return rule.ProviderVerdict, rule.Notes
			}
			return "ok", "official free marking in model id"
		}
		// 无任何关键词命中: 走提供商级兜底, 再兜底 ambiguous (保守)
		if rule.ProviderVerdict == "ok" || rule.ProviderVerdict == "caution" {
			return rule.ProviderVerdict, rule.Notes
		}
		return "ambiguous", "no explicit free marking; manual review required"
	}
	return verdict, notes
}

func tplProviderCode(tpl *ProviderTemplate) string {
	if tpl == nil {
		return ""
	}
	return tpl.ProviderCode
}

func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if kw != "" && strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
