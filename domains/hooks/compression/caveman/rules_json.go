package caveman

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// rewriteLookahead 把 JS 的 lookahead (?=...) / (?!=...) 改写成 RE2 兼容形式。
//
// 关键背景（GW-07）：Go 的 regexp（RE2）**完全不支持** lookahead 和 lookbehind
// （不像 JS RegExp）。嵌入的 8 语言规则里有 18 处 lookahead（无 lookbehind——
// lookbehind 已在 en/filler.json + preservation.go 手动处理）。
//
// 全部 18 处 lookahead 是两种机械形态，本函数自动识别：
//
//  1. `<prefix>\s+(?=[charset])` → `<prefix>\s+([charset])`
//     lookahead 单字符集吸收成捕获组 N（N = 已有捕获组数 + 1）。
//     替换函数须回写 group N（见 compileRule 的 lookaheadGroup 处理）。
//     17 处（强调词/冠词/leader 规则）。
//
//  2. `(?:...)(?=[punct\s]|$)` → `(?:...)([punct\s])?`
//     lookahead 字符集 + 可选结尾。吸收成可选捕获组。1 处（zh 句末语气词）。
//
// 不匹配这两种形态的 lookahead 会返回错误（避免静默丢规则）。
// 返回 (改写后pattern, 捕获组序号用于回写, 错误)。
func rewriteLookahead(pattern string) (string, int, error) {
	if !strings.Contains(pattern, "(?=") {
		return pattern, 0, nil // 无 lookahead
	}

	// 形态1: \s+(?=[...]) — lookahead 紧跟在 \s+ 后，内容是单字符集 [...]。
	// 改写: \s+([...]) 捕获组。
	if newPat, ok, g := rewriteWhitespaceLookahead(pattern); ok {
		return newPat, g, nil
	}

	// 形态2: (?=[...]|$) — lookahead 含字符集和 $ 结尾。
	// 改写: ([...])? 可选捕获组。
	if newPat, ok, g := rewriteTrailingLookahead(pattern); ok {
		return newPat, g, nil
	}

	return pattern, 0, fmt.Errorf("unsupported lookahead in pattern (only \\s+(?=[...]) and (?=[...]|$) supported): %s", pattern)
}

// rewriteWhitespaceLookahead 处理 `\s+(?=[charset])` 形态。
// 把 `(?=` 前的 `\s+` 后接的 `(?=[...])` 改成 `([...])`。
func rewriteWhitespaceLookahead(pattern string) (string, bool, int) {
	// 找 \s+(?=[ 开始的片段。lookahead 字符集形如 [abc...]。
	re := regexp.MustCompile(`\\s\+\(\?=\[([^\]]+)\]\)`)
	loc := re.FindStringSubmatchIndex(pattern)
	if loc == nil {
		return pattern, false, 0
	}
	charset := pattern[loc[2]:loc[3]]
	// 计算改写前 pattern 里已有的捕获组数（用 `(` 计数，忽略 (?: 和 (?=。
	groupCount := countCaptureGroups(pattern[:loc[0]])
	newGroup := groupCount + 1
	// 替换 \s+(?=[charset]) → \s+([charset])
	rewritten := pattern[:loc[0]] + `\s+([` + charset + `])` + pattern[loc[1]:]
	return rewritten, true, newGroup
}

// rewriteTrailingLookahead 处理 `(?=[punct]|$)` 结尾形态。
func rewriteTrailingLookahead(pattern string) (string, bool, int) {
	re := regexp.MustCompile(`\(\?=\[([^\]]+)\]\|\$\)`)
	loc := re.FindStringSubmatchIndex(pattern)
	if loc == nil {
		return pattern, false, 0
	}
	charset := pattern[loc[2]:loc[3]]
	groupCount := countCaptureGroups(pattern[:loc[0]])
	newGroup := groupCount + 1
	// (?=[charset]|$) → ([charset])?
	rewritten := pattern[:loc[0]] + `([` + charset + `])?` + pattern[loc[1]:]
	return rewritten, true, newGroup
}

// countCaptureGroups 数 pattern 里裸捕获组 `(` 的数量，忽略 (?: (?= (?! (?<= (?<!。
func countCaptureGroups(pattern string) int {
	count := 0
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '(' && i+1 < len(pattern) && pattern[i+1] != '?' {
			count++
		}
		// (?: 也算（非捕获组不算），只数裸 (
	}
	return count
}

// fileRule 是 rules/*.json 的磁盘形态。对齐 ruleLoader.ts FileRule。
// 额外字段：
//   - guardGroup (int): GW-07 RE2 改写用的捕获组索引（1-based）。非零时，
//     该组非空则保留原 match（替代 TS lookbehind 语义）。
//   - _note (string): 仅供人类阅读，加载器忽略。
type fileRule struct {
	Name           string            `json:"name"`
	Pattern        string            `json:"pattern"`
	Replacement    string            `json:"replacement,omitempty"`
	ReplacementMap map[string]string `json:"replacementMap,omitempty"`
	Flags          string            `json:"flags,omitempty"`
	GuardGroup     int               `json:"guardGroup,omitempty"`
	Context        string            `json:"context,omitempty"`
	Category       string            `json:"category,omitempty"`
	MinIntensity   string            `json:"minIntensity,omitempty"`
	Description    string            `json:"description,omitempty"`
}

// rulePack 是一个 .json 文件的形态。对齐 ruleLoader.ts RulePack。
type rulePack struct {
	Language string     `json:"language"`
	Category string     `json:"category"`
	Rules    []fileRule `json:"rules"`
}

// 校验白名单（对齐 ruleLoader.ts:33-35）。
var (
	validContexts    = map[string]bool{"all": true, "user": true, "system": true, "assistant": true}
	validCategories  = map[string]bool{"filler": true, "context": true, "structural": true, "dedup": true, "terse": true, "ultra": true}
	validIntensities = map[string]bool{"lite": true, "full": true, "ultra": true}
)

// validateRulePack 校验一个 pack 的结构合法性。对齐 ruleLoader.ts:110-185。
// 返回 error 时 pack 不可用。
func validateRulePack(pack rulePack) error {
	if strings.TrimSpace(pack.Language) == "" {
		return fmt.Errorf("rule pack language is empty")
	}
	if strings.TrimSpace(pack.Category) == "" {
		return fmt.Errorf("rule pack category is empty")
	}
	if pack.Rules == nil {
		return fmt.Errorf("rule pack %s/%s: rules is not an array", pack.Language, pack.Category)
	}
	for i, r := range pack.Rules {
		src := fmt.Sprintf("%s/%s[%d]", pack.Language, pack.Category, i)
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("%s: rule name is empty", src)
		}
		if strings.TrimSpace(r.Pattern) == "" {
			return fmt.Errorf("%s: rule %s pattern is empty", src, r.Name)
		}
		// pattern 必须能在 RE2 下编译（这是 GW-07 核心门禁）。
		// 先过 lookahead 改写，再编译改写后版本。
		rewritten, _, rerr := rewriteLookahead(r.Pattern)
		if rerr != nil {
			return fmt.Errorf("%s: rule %s lookahead rewrite: %w", src, r.Name, rerr)
		}
		if _, err := compilePattern(rewritten, r.Flags); err != nil {
			return fmt.Errorf("%s: rule %s pattern invalid: %w", src, r.Name, err)
		}
		// replacement 与 replacementMap 关系（对齐 ruleLoader.ts:43-55 compileReplacement）：
		//   - 有 replacementMap：replacement 作 fallback（miss 时用），两者共存合法。
		//   - 无 replacementMap：replacement 是固定替换（可空，默认 ""）。
		//   - 都没有：替换为 ""。
		if len(r.ReplacementMap) > 0 {
			for k := range r.ReplacementMap {
				if strings.TrimSpace(k) == "" {
					return fmt.Errorf("%s: rule %s replacementMap has empty key", src, r.Name)
				}
			}
		}
		if r.Context != "" && !validContexts[r.Context] {
			return fmt.Errorf("%s: rule %s invalid context %q", src, r.Name, r.Context)
		}
		if r.Category != "" && !validCategories[r.Category] {
			return fmt.Errorf("%s: rule %s invalid category %q", src, r.Name, r.Category)
		}
		if r.MinIntensity != "" && !validIntensities[r.MinIntensity] {
			return fmt.Errorf("%s: rule %s invalid minIntensity %q", src, r.Name, r.MinIntensity)
		}
	}
	return nil
}

// compilePattern 把 JSON pattern + flags 编译成 Go *regexp.Regexp。
// JS 用 "gi"/"i" 等 flags 参数，Go 用 (?i) 等前缀。
// 转换：'g' 忽略（Go ReplaceAllString 默认全局），'i' → (?i) 前缀，
// 'm' → (?m)，'s' → (?s)。多 flag 合并如 (?im)。
//
// 默认 flags：对齐 ruleLoader.ts:57-59 getRuleFlags，空 flags 默认 "gi"。
// 故 flags=="" 时加 (?i)（Go 默认已全局）。
// 若 pattern 已有 (?i) 前缀（改写后的 en/filler.json），不重复加。
func compilePattern(pattern, flags string) (*regexp.Regexp, error) {
	if flags == "" {
		flags = "gi" // 对齐 TS 默认
	}
	prefix := flagsToPrefix(flags)
	src := pattern
	if !strings.HasPrefix(src, "(?") && prefix != "" {
		src = prefix + src
	}
	return regexp.Compile(src)
}

// flagsToPrefix 把 JS flags 串转成 Go inline flag 前缀。
func flagsToPrefix(flags string) string {
	var b strings.Builder
	for _, f := range flags {
		switch f {
		case 'i', 'm', 's':
			b.WriteRune(f)
		case 'g':
			// Go 无 'g' 概念，忽略。
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "(?" + b.String() + ")"
}

// compileRule 把 fileRule 编译成预编译 Rule。对齐 ruleLoader.ts:92-108。
// source 用于错误信息。
//
// GW-07 RE2 适配：自动改写 lookahead（见 rewriteLookahead）。改写产生的捕获组
// 需在替换时回写（lookaheadGroup 回写该组内容，避免 lookahead 吸收的字符被删）。
func compileRule(fr fileRule, source string) (Rule, error) {
	// 先改写 lookahead（在 flags→前缀转换之前，因为改写依赖原始 pattern 结构）。
	rewrittenPattern, lookaheadGroup, err := rewriteLookahead(fr.Pattern)
	if err != nil {
		return Rule{}, fmt.Errorf("%s:%s %w", source, fr.Name, err)
	}
	// guardGroup + lookaheadGroup 可能同时存在；先处理 guardGroup 的 pattern（fr.Pattern），
	// 再叠加 lookahead 改写。但 guardGroup 的 pattern 已在 en/filler.json 里手动改写
	// （含捕获组1），lookahead 改写会正确计算已有捕获组数。这里统一用 rewrittenPattern。
	pat, err := compilePattern(rewrittenPattern, fr.Flags)
	if err != nil {
		return Rule{}, fmt.Errorf("%s:%s invalid pattern: %w", source, fr.Name, err)
	}
	ctx := CtxAll
	if fr.Context != "" {
		ctx = RuleContext(fr.Context)
	}
	intensity := IntensityLite
	if fr.MinIntensity != "" {
		intensity = Intensity(fr.MinIntensity)
	}

	// 编译替换：replacementMap → mapReplace；否则 replacement → staticReplace。
	var inner ReplaceFn
	switch {
	case len(fr.ReplacementMap) > 0:
		inner = mapReplace(fr.ReplacementMap, fr.Replacement)
	default:
		inner = staticReplace(fr.Replacement)
	}
	// guardGroup 包装（GW-07 RE2 改写：en/filler.json 的 lookbehind）。
	replace := inner
	if fr.GuardGroup > 0 {
		replace = guardReplace(pat, fr.GuardGroup, inner)
	}
	// lookaheadGroup 回写（GW-07 RE2 改写：18 处 lookahead）。
	// lookahead 吸收的字符必须保留——replacement 之后回写该组内容。
	if lookaheadGroup > 0 {
		replace = lookaheadWriteback(pat, lookaheadGroup, replace)
	}

	return Rule{
		Name:         fr.Name,
		Pattern:      pat,
		Replace:      replace,
		Context:      ctx,
		Category:     fr.Category,
		MinIntensity: intensity,
		Description:  fr.Description,
	}, nil
}

// --- 缓存的规则加载（对齐 ruleLoader.ts 的 cache + loadAllRulesForLanguage）---

var (
	ruleCacheMu sync.RWMutex
	ruleCache   = map[string][]Rule{} // key = language
)

// loadPack 解析一个 pack 的 JSON 字节并编译。
func loadPack(raw []byte, source string) ([]Rule, error) {
	var pack rulePack
	if err := json.Unmarshal(raw, &pack); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := validateRulePack(pack); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	rules := make([]Rule, 0, len(pack.Rules))
	for _, fr := range pack.Rules {
		r, err := compileRule(fr, fmt.Sprintf("%s/%s", pack.Language, pack.Category))
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// loadAllRulesForLanguage 从嵌入的 JSON 按文件名字母序加载某语言的所有 pack 并合并。
// 对齐 ruleLoader.ts:219-240。结果缓存。
// files 是 {filename: json bytes} 映射，由 rules_data.go 提供。
func loadAllRulesForLanguage(language string, files map[string][]byte) []Rule {
	ruleCacheMu.RLock()
	if cached, ok := ruleCache[language]; ok {
		ruleCacheMu.RUnlock()
		return cached
	}
	ruleCacheMu.RUnlock()

	// 按文件名字母序（对齐 ruleLoader.ts:228 sort）。
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var all []Rule
	for _, name := range names {
		raw := files[name]
		rules, err := loadPack(raw, fmt.Sprintf("%s/%s", language, name))
		if err != nil {
			// 嵌入的规则在构建时已校验（TestAllRulesCompile）；运行时不应出错。
			// fail-open：跳过该 pack，记录不到错误日志（避免 prompt 泄露）。
			continue
		}
		all = append(all, rules...)
	}

	ruleCacheMu.Lock()
	ruleCache[language] = all
	ruleCacheMu.Unlock()
	return all
}

// getRulesForContext 按角色 + 强度 + 语言过滤规则。对齐 cavemanRules.ts:394-412。
// 返回 context=="all" 或 ==role 且 minIntensity<=intensity 的规则。
func getRulesForContext(role string, intensity Intensity, language string, allRules []Rule, skipRules map[string]bool) []Rule {
	rank := intensityRank(intensity)
	var out []Rule
	for _, r := range allRules {
		if r.Context != CtxAll && r.Context != RuleContext(role) {
			continue
		}
		if intensityRank(r.MinIntensity) > rank {
			continue
		}
		if skipRules[r.Name] {
			continue
		}
		out = append(out, r)
	}
	return out
}
