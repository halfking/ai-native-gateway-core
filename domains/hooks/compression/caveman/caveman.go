package caveman

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Result 是 Compress 的返回结果。不含 prompt/response 正文（redaction 门禁）。
type Result struct {
	Techniques      []string // ["caveman-rules"] 当有规则应用
	RulesApplied    []string // 应用的规则名（去重）
	InputChars      int
	OutputChars     int
	PreservedBlocks int
	FallbackApplied bool
	ValidationErrs  []string // validation 失败原因（低敏）
}

// Compress 对一个 chat body 跑 Caveman 压缩。
// 翻译自 caveman.ts:445-602 cavemanCompress。
//
// 返回 (newBody, result, applied)。applied=false 表示无改动（newBody==body）。
// fail-open：非法 JSON / 无 messages / 任何错误 → 返回原 body。
func Compress(body []byte, cfg Config) ([]byte, Result, bool) {
	res := Result{InputChars: len(body), OutputChars: len(body)}
	if !cfg.Enabled || len(body) == 0 {
		return body, res, false
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, res, false // fail-open
	}
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, res, false
	}

	compressRoles := toStringSet(cfg.CompressRoles)
	skipSet := toStringSet(cfg.SkipRules)

	// 预编译用户保护模式（对齐 caveman.ts:478）。
	userPatterns := compileUserPreservePatterns(cfg.PreservePatterns)

	allAppliedRules := map[string]bool{}
	var validationErrs []string
	fallbackApplied := false
	totalPreserved := 0

	compressedMsgs := make([]any, len(msgs))
	changed := false

	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			compressedMsgs[i] = m
			continue
		}
		role, _ := msg["role"].(string)

		// 跑 mapTextContent，对每个 text 部分压缩。
		newMsg, msgChanged := mapTextContent(msg, func(textPart string, _ int) string {
			if len(textPart) < cfg.MinMessageLength {
				return textPart
			}
			compressed, appliedRules, preserved, fellBack, errs := compressTextPart(
				textPart, role, cfg, compressRoles, skipSet, userPatterns,
			)
			for _, r := range appliedRules {
				allAppliedRules[r] = true
			}
			totalPreserved += preserved
			if fellBack {
				fallbackApplied = true
			}
			validationErrs = append(validationErrs, errs...)
			return compressed
		})
		if msgChanged {
			changed = true
		}
		compressedMsgs[i] = newMsg
	}

	if !changed || len(allAppliedRules) == 0 {
		return body, res, false
	}

	doc["messages"] = compressedMsgs
	out, err := json.Marshal(doc)
	if err != nil {
		return body, res, false
	}

	rulesList := sortedKeys(allAppliedRules)
	techniques := []string{}
	if len(rulesList) > 0 {
		techniques = []string{"caveman-rules"}
	}
	return out, Result{
		Techniques:      techniques,
		RulesApplied:    rulesList,
		InputChars:      len(body),
		OutputChars:     len(out),
		PreservedBlocks: totalPreserved,
		FallbackApplied: fallbackApplied,
		ValidationErrs:  validationErrs,
	}, true
}

// compressTextPart 压缩单段文本。返回 (compressed, appliedRules, preservedCount, fallback, validationErrs)。
// 对齐 caveman.ts:509-558 compressTextPart 闭包。
func compressTextPart(text, role string, cfg Config, compressRoles, skipSet map[string]bool, userPatterns []*regexp.Regexp) (string, []string, int, bool, []string) {
	if len(text) < cfg.MinMessageLength {
		return text, nil, 0, false, nil
	}
	if !compressRoles[role] {
		return text, nil, 0, false, nil
	}

	// 保护块抽取（对齐 caveman.ts:512-518）。
	var blocks []PreservedBlock
	extracted := text
	shouldPreserve := len(userPatterns) > 0 || hasProtectedStructure(text)
	if shouldPreserve {
		extracted, blocks = extractPreservedBlocks(text, PreservationOptions{PreservePatterns: userPatterns})
	}

	// 语言 + 规则（对齐 caveman.ts:521-538）。
	detected := "en"
	if cfg.AutoDetectLanguage {
		detected = detectLanguage(text)
	} else if cfg.Language != "" {
		detected = cfg.Language
	}
	lang := resolveLanguage(detected, cfg.AutoDetectLanguage, cfg.EnabledLanguagePacks)
	allRules := LoadAllRulesForLanguage(lang)
	rules := getRulesForContext(role, cfg.Intensity, lang, allRules, skipSet)

	// 应用规则（对齐 caveman.ts:539-540）。
	ruleResult, appliedRules := applyRulesToText(extracted, rules)

	// cleanup + recapitalize（对齐 caveman.ts:542）。
	normalized := recapitalizeSentences(cleanupArtifacts(ruleResult))

	// 还原保护块 + 再 cleanup + validation（对齐 caveman.ts:543-555）。
	cleaned := normalized
	if len(blocks) > 0 {
		cleaned = cleanupArtifacts(restorePreservedBlocks(normalized, blocks))
	}
	if shouldPreserve || len(blocks) > 0 {
		vr := validateCompression(text, cleaned)
		if !vr.Valid {
			return text, nil, len(blocks), true, vr.Errors // fallback 回原文
		}
	}
	return cleaned, appliedRules, len(blocks), false, nil
}

// hasProtectedStructure 快速判文本是否含受保护结构。对齐 caveman.ts:435-443。
// 先用 prefilter 快速排除，再跑完整 pattern。
func hasProtectedStructure(text string) bool {
	if !protectedStructurePrefilterRE.MatchString(text) {
		return false
	}
	for _, re := range protectedStructureREs {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// protectedStructurePrefilterRE 是快速预过滤（对齐 caveman.ts:437）。
// 含任一这些字符才可能含受保护结构。
var protectedStructurePrefilterRE = regexp.MustCompile("[`~\\[\\]\\|$#\\\\/:_()0-9]")

// protectedStructureRE 是受保护结构的完整检测（对齐 caveman.ts:436 的语义）。
// 用于 hasProtectedStructure 的二级确认。这是检测用，不是抽取用
// （抽取在 preservation.go 的 builtinProtectedPatterns 里逐项做）。
//
// GW-07 RE2 适配：TS 原文把所有检测模式拼成一个巨型 alternation，含
// {1,2000}/{0,1000}/嵌套 {1,100} 等大重复，Go RE2 编译会超 size 上限。
// 这里拆成独立的子模式切片，避免单正则过大；hasProtectedStructure 任一命中即真。
var protectedStructureREs = []*regexp.Regexp{
	regexp.MustCompile("```|~~~|`"),
	regexp.MustCompile(`https?://`),
	regexp.MustCompile(`\[[^\]\n]+\]\([^) \t\n]+(?:[ \t]+"[^"]*")?\)`),
	regexp.MustCompile(`(?m)^#{1,6}\s+`),
	regexp.MustCompile(`(?m)^[ \t]*\|.*\|`),
	regexp.MustCompile(`\$\$`),
	regexp.MustCompile(`\\\[`),
	regexp.MustCompile(`\\begin\{`),
	regexp.MustCompile(`(?m)^\s*#(?:set|show|let|import|include)\b`),
	regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`),
	regexp.MustCompile(`\bprocess\.env\.[A-Za-z_][A-Za-z0-9_]*\b`),
	regexp.MustCompile(`\$[A-Z_][A-Z0-9_]*\b`),
	regexp.MustCompile(`\b\d+(?:\.\d+){1,3}(?:[-+][A-Za-z0-9.-]+)?\b`),
	regexp.MustCompile(`\b[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)+\(\)?`),
	regexp.MustCompile(`(?:^|\s)(?:\.{0,2}/[A-Za-z0-9_@./-]+|[A-Za-z]:\\[A-Za-z0-9_.\\/-]+)`),
	regexp.MustCompile(`\b(?:TypeError|ReferenceError|SyntaxError|RangeError|URIError|EvalError|Error|Exception):[^\n]*`),
}

// compileUserPreservePatterns 编译用户保护模式源串。对齐 caveman.ts:418-433。
// 无效模式忽略（不 panic）。
func compileUserPreservePatterns(patterns []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		out = append(out, re)
	}
	return out
}

// toStringSet 把 slice 转 set。
func toStringSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// sortedKeys 返回 map keys 的排序切片。
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单排序（避免引入 sort 到本文件；用 strings 比较不够，用冒泡）。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// 编译期防止 fmt 未用（caveman 用 fmt 在 Result 构造未来扩展）。
var _ = fmt.Sprintf
