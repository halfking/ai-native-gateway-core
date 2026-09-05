package sessionmeta

import (
	"strings"
	"sync"
)

// Expert type classification. Each type represents a domain specialty the
// assistant claims to operate in. Detection is rule-based: the assistant's
// self-description in the system prompt is matched against a priority-ordered
// pattern registry. Detection runs after agent identification so that generic
// engineering phrases ("you are an engineer") never beat the more specific
// role phrases ("you are a security engineer").
//
// The list is deliberately curated — adding new entries requires a
// code change AND a regression test. For long-tail coverage use
// RegisterExpertPattern at startup.
const (
	ExpertSoftwareEngineering = "software_engineering"
	ExpertDataScience         = "data_science"
	ExpertDevOps              = "devops"
	ExpertSecurity            = "security"
	ExpertTesting             = "testing"
	ExpertDesign              = "design"
	ExpertResearch            = "research"
	ExpertProductManagement   = "product_management"
	ExpertDocumentation       = "documentation"
	ExpertCustomerSupport     = "customer_support"
	ExpertFinance             = "finance"
	ExpertLegal               = "legal"
	ExpertMarketing           = "marketing"
	ExpertTranslation         = "translation"
	ExpertGeneralPurpose      = "general"
	ExpertUnknown             = "unknown"
)

// expertPatternEntry binds a canonical expert type to the substrings that
// trigger a match. Patterns are lower-cased at match time.
type expertPatternEntry struct {
	typ      string
	patterns []string
}

var (
	expertPatternsMu sync.RWMutex
	expertPatterns   = defaultExpertPatterns()
)

func defaultExpertPatterns() []expertPatternEntry {
	return []expertPatternEntry{
		// ── Order matters: more-specific roles FIRST ──
		// "security engineer" / "ML engineer" must win over generic
		// "you are an engineer". We register narrow roles before the
		// catch-all software_engineering entry.
		{ExpertSecurity, []string{
			"security researcher", "security engineer", "security analyst",
			"penetration tester", "ethical hacker", "red team", "blue team",
			"cybersecurity expert", "infosec specialist", "offensive security",
			"defensive security", "appsec engineer", "soc analyst",
			"you are a security", "you specialize in security",
			"安全研究员", "安全工程师", "安全审计", "渗透测试",
			"安全专家", "信息安全",
		}},
		{ExpertDataScience, []string{
			"data scientist", "machine learning engineer", "ml engineer",
			"data engineer", "data analyst", "ai engineer",
			"deep learning", "neural network researcher",
			"research scientist", "applied scientist",
			"数据科学家", "机器学习工程师", "数据分析师", "算法工程师",
			"you are a data scientist", "you are an ml engineer",
			"you are an ai engineer",
		}},
		{ExpertDevOps, []string{
			"devops engineer", "sre engineer", "site reliability engineer",
			"platform engineer", "infrastructure engineer",
			"cloud engineer", "kubernetes administrator", "k8s engineer",
			"release engineer", "build engineer",
			"you are a devops", "you are an sre",
			"运维工程师", "运维开发", "SRE 工程师", "devops 工程师",
			"基础设施工程师", "云工程师",
		}},
		{ExpertTesting, []string{
			"qa engineer", "test engineer", "quality assurance",
			"sdet", "test automation engineer", "software tester",
			"performance engineer", "load testing engineer",
			"测试工程师", "QA 工程师", "测试开发", "质量保障",
		}},
		{ExpertDesign, []string{
			"ui designer", "ux designer", "product designer",
			"graphic designer", "interaction designer",
			"frontend designer", "visual designer", "design lead",
			"design system engineer", "design technologist",
			// "ui engineer" / "ux engineer" must be here to beat the
			// catch-all "you are an engineer" software_engineering match.
			"ui engineer", "ux engineer", "frontend engineer",
			"design engineer",
			"UI 设计师", "UX 设计师", "产品设计师", "视觉设计师",
			"交互设计师", "设计工程师",
		}},
		{ExpertResearch, []string{
			"researcher", "academic researcher", "research engineer",
			"research assistant", "investigative journalist",
			"scientific researcher", "literature reviewer",
			"调研员", "研究员", "学术研究", "科研人员",
		}},
		{ExpertProductManagement, []string{
			"product manager", "product owner", "program manager",
			"technical product manager", "product lead",
			"产品经理", "产品负责人", "项目经理",
		}},
		{ExpertDocumentation, []string{
			"technical writer", "documentation engineer",
			"documentation specialist", "content writer",
			"copywriter", "api documentation",
			"技术写作", "文档工程师", "技术文档工程师", "技术作者",
		}},
		{ExpertCustomerSupport, []string{
			"customer support", "support agent", "helpdesk",
			"support specialist", "customer service",
			"technical support engineer",
			"客服", "客户支持", "技术支持", "客服专员",
		}},
		{ExpertFinance, []string{
			"financial analyst", "accountant", "finance manager",
			"financial advisor", "controller", "treasurer",
			"财务分析师", "会计", "财务经理", "财务顾问",
		}},
		{ExpertLegal, []string{
			"legal advisor", "lawyer", "attorney", "paralegal",
			"compliance officer", "contract reviewer", "legal counsel",
			"法务", "律师", "合规", "法务顾问",
		}},
		{ExpertMarketing, []string{
			"marketing manager", "seo specialist", "growth marketer",
			"digital marketer", "content marketer", "brand manager",
			"营销经理", "市场营销", "增长营销", "SEO 专员",
		}},
		{ExpertTranslation, []string{
			"translator", "translation specialist", "interpreter",
			"professional translator", "localization specialist",
			"翻译", "译者", "口译员", "本地化",
		}},
		// ── Software engineering (catch-all, AFTER specific roles) ──
		{ExpertSoftwareEngineering, []string{
			"software engineer", "software developer",
			"coding assistant", "code assistant", "coding agent",
			"cursor", "claude code", "codex", "opencode", "copilot",
			"interactive coding", "developer tool",
			"build software", "write code", "code review",
			"you are a developer", "you are an engineer",
			"software engineering", "code generation",
			"programmer", "developer", "engineer",
			"软件工程师", "软件开发", "软件工程", "程序员", "开发者",
			"编程助手", "代码助手", "写代码",
		}},
		// ── General purpose (lowest priority fallback) ──
		{ExpertGeneralPurpose, []string{
			"helpful assistant", "ai assistant", "general purpose",
			"general-purpose assistant",
			"通用助手", "智能助手", "AI 助手", "通用 AI",
		}},
	}
}

// RegisterExpertPattern prepends expert detection patterns for a type at
// runtime. Patterns are placed BEFORE the default registry so user-registered
// matches always win over the catch-all software_engineering entry.
//
// Safe for concurrent access. Patterns are lower-cased internally; empty /
// whitespace-only patterns are skipped (a literal "" matches every prompt).
// Duplicate registrations for the same type are additive.
//
// Example:
//
//	telemetry.RegisterExpertPattern("blockchain", "blockchain engineer", "smart contract developer")
func RegisterExpertPattern(typ string, patterns ...string) {
	if typ == "" || len(patterns) == 0 {
		return
	}
	lower := make([]string, 0, len(patterns))
	for _, p := range patterns {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		lower = append(lower, strings.ToLower(trimmed))
	}
	if len(lower) == 0 {
		return
	}
	expertPatternsMu.Lock()
	defer expertPatternsMu.Unlock()
	// Prepend so registered patterns take precedence over built-in defaults
	// (including the catch-all software_engineering).
	entry := expertPatternEntry{typ: typ, patterns: lower}
	expertPatterns = append([]expertPatternEntry{entry}, expertPatterns...)
}

// ResetExpertPatterns restores the default built-in expert patterns.
// Used in tests to isolate registrations.
func ResetExpertPatterns() {
	expertPatternsMu.Lock()
	defer expertPatternsMu.Unlock()
	expertPatterns = defaultExpertPatterns()
}

// DetectExpertFromSystemPrompt attempts to identify the expert specialty
// from the system prompt content. Returns the canonical expert type, or
// ExpertUnknown when no specialty is identified. Detection is order-sensitive:
// the FIRST matching pattern wins, so register narrow roles before broad ones.
//
// This is a lightweight heuristic — it lowercases the prompt and checks for
// known identity substrings. It is not a full NLP classifier.
func DetectExpertFromSystemPrompt(systemPrompt string) string {
	if systemPrompt == "" {
		return ExpertUnknown
	}
	lower := strings.ToLower(systemPrompt)
	expertPatternsMu.RLock()
	defer expertPatternsMu.RUnlock()
	for _, entry := range expertPatterns {
		for _, p := range entry.patterns {
			if strings.Contains(lower, p) {
				return entry.typ
			}
		}
	}
	return ExpertUnknown
}

// extractExpert builds the ExpertIdentity from input + system prompt.
// Explicit Input.Expert always wins over text heuristics; unresolved
// signals stay "unknown" rather than guessing.
func extractExpert(in Input, system string) ExpertIdentity {
	explicit := strings.ToLower(strings.TrimSpace(in.Expert))
	if explicit != "" && explicit != ExpertUnknown {
		return ExpertIdentity{Type: explicit, Source: "header", Confidence: 1}
	}
	// DetectExpertFromSystemPrompt returns either a non-empty canonical
	// expert constant or ExpertUnknown — never "" — so a single comparison
	// against ExpertUnknown is sufficient (audit simplification 2026-08-25).
	if detected := DetectExpertFromSystemPrompt(system); detected != ExpertUnknown {
		return ExpertIdentity{Type: detected, Source: "system_prompt", Confidence: 0.85}
	}
	return ExpertIdentity{Type: ExpertUnknown, Source: "unknown", Confidence: 0}
}
