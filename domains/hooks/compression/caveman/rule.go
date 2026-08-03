// Package caveman 实现 omni-ref2 GW-07 的 Caveman 文本压缩引擎。
//
// 全量翻译自 OmniRoute 的 open-sse/services/compression/caveman.ts
// （commit c8f1d62de），含 cavemanRules.ts、preservation.ts、languageDetector.ts、
// validation.ts、messageContent.ts、ruleLoader.ts 及 rules/ 下 8 语言包（306 条规则）。
//
// SOURCE-VERIFIED 关键点：
//   - 规则引擎：~40-50 条正则规则/语言，replacement（字符串）或 replacementMap（按
//     match 小写归一化查表）双形态。
//   - 保护块系统：sentinel 占位符保护代码块/URL/数学公式等，规则跑完再还原。
//   - 语言检测：CJK 脚本 + Latin 关键词打分，返回 8 语言（de/en/es/fr/id/ja/pt-BR/zh）。
//   - validation：12 类受保护结构的精确存在性检查（手写字符扫描，ReDoS-immune）。
//
// RE2 不兼容点（仅 3 处，全部已改写，见 data/en/filler.json 的 _note 与
// preservation.go 的 math_inline）：
//   - en/filler.json pleasantries: (?<!make\s)(?<!be\s) → 捕获组1 + guardGroup
//   - en/filler.json filler_adverbs: (?<![a-z]) → 捕获组1 + guardGroup
//   - preservation math_inline: (?<!\$)(?<!\s) → 捕获组 + 抽取时校验
//
// 设计约束（README §5 C2 门禁 + 矩阵 GW-07）：
//   - RE2 兼容（所有 pattern 在 Go regexp 下编译通过，rules_json_test 锁定）。
//   - 规则可审计（guardGroup/replacementMap 字面量来源 data/*.json）。
//   - 无正文日志（Result 只含 counts/rules，不含 prompt/response 内容）。
//   - fail-open：任何错误返回原 body，不 panic。
//   - feature-flagged，默认关闭。
//   - 不碰执行器实时路径（执行器接线是 GW-08/Pipeline.Apply）。
package caveman

import "regexp"

// Intensity 是 Caveman 规则的强度档位。对齐 types.ts CavemanIntensity。
// rank 越高包含越多规则（lite < full < ultra）。
type Intensity string

const (
	IntensityLite  Intensity = "lite"
	IntensityFull  Intensity = "full"
	IntensityUltra Intensity = "ultra"
)

// intensityRank 返回强度的数值序（用于 minIntensity 过滤）。未知→lite。
func intensityRank(i Intensity) int {
	switch i {
	case IntensityLite:
		return 0
	case IntensityFull:
		return 1
	case IntensityUltra:
		return 2
	}
	return 0
}

// RuleContext 是规则适用的消息角色上下文。对齐 types.ts CavemanRuleContext。
type RuleContext string

const (
	CtxAll       RuleContext = "all"
	CtxUser      RuleContext = "user"
	CtxSystem    RuleContext = "system"
	CtxAssistant RuleContext = "assistant"
)

// ReplaceFn 接收完整 match 文本，返回替换值。
// 固定 replacement 字符串、replacementMap、guardGroup 三种形态都编译成这种闭包。
type ReplaceFn func(match string) string

// Rule 是一条预编译的 Caveman 规则。对齐 types.ts CavemanRule。
type Rule struct {
	Name         string
	Pattern      *regexp.Regexp // 预编译（含 (?i) 等 flag）
	Replace      ReplaceFn      // 替换闭包
	Context      RuleContext
	Category     string
	MinIntensity Intensity
	Description  string
}

// staticReplace 把固定字符串包成 ReplaceFn。
func staticReplace(s string) ReplaceFn {
	return func(string) string { return s }
}

// mapReplace 把 replacementMap 编译成 ReplaceFn。
// key 归一化：trim + 折叠内部空白 + lowercase（对齐 ruleLoader.ts:39-41）。
// miss 时返回 fallback（replacement），fallback 空则返回原 match（对齐 ruleLoader.ts:53）。
func mapReplace(m map[string]string, fallback string) ReplaceFn {
	normalized := make(map[string]string, len(m))
	for k, v := range m {
		normalized[normalizeReplacementKey(k)] = v
	}
	return func(match string) string {
		if v, ok := normalized[normalizeReplacementKey(match)]; ok {
			return v
		}
		if fallback != "" {
			return fallback
		}
		return match
	}
}

// normalizeReplacementKey 对齐 ruleLoader.ts:39-41：trim + 折叠连续空白为单空格 + lowercase。
func normalizeReplacementKey(s string) string {
	out := make([]rune, 0, len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		// lowercase（ASCII）
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		out = append(out, r)
	}
	// trim
	start, end := 0, len(out)
	for start < end && out[start] == ' ' {
		start++
	}
	for end > start && out[end-1] == ' ' {
		end--
	}
	return string(out[start:end])
}

// guardReplace 包装一个 ReplaceFn：当指定的捕获组（1-based）在 match 中非空时，
// 返回原 match（不替换）；否则委托给 inner。
//
// 这是 GW-07 RE2 改写的核心机制：TS 的 (?<!prefix) lookbehind 改写成
// (prefix)?<match> 捕获组 + guardGroup，由本函数在替换时检查前导字符。
// 取捕获组用 Pattern.FindStringSubmatchIndex + ExpandString 还原完整 match。
func guardReplace(pat *regexp.Regexp, groupIdx int, inner ReplaceFn) ReplaceFn {
	return func(match string) string {
		// 用 pat 重新匹配 match 取捕获组（match 已是完整匹配文本）。
		sub := pat.FindStringSubmatch(match)
		// sub[0]=完整 match，sub[groupIdx]=捕获组。若组存在且非空 → 保留。
		if groupIdx < len(sub) && sub[groupIdx] != "" {
			return match
		}
		return inner(match)
	}
}

// lookaheadWriteback 包装一个 ReplaceFn：在 inner 返回的替换结果后追加
// 指定捕获组的文本。用于 GW-07 lookahead 改写——lookahead 吸收的字符必须保留。
//
// 例：TS `very\s+(?=[a-z])` 删 "very" 但保留 lookahead 处的字母。
// 改写后 `very\s+([a-z])`，匹配 "very big" 时 group1="b"，替换函数返回 ""
// （删 very），本函数追加 "b" → 最终 "big"。
func lookaheadWriteback(pat *regexp.Regexp, groupIdx int, inner ReplaceFn) ReplaceFn {
	return func(match string) string {
		sub := pat.FindStringSubmatch(match)
		replaced := inner(match)
		if groupIdx < len(sub) && sub[groupIdx] != "" {
			return replaced + sub[groupIdx]
		}
		return replaced
	}
}
