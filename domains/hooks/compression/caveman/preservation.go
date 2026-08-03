package caveman

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// PreservedBlock 是一个被保护块的占位符 + 原文。对齐 preservation.ts PreservedBlock。
type PreservedBlock struct {
	Placeholder string
	Content     string
	Kind        string
}

// sentinelPrefix 对齐 preservation.ts:16。占位符用 NUL 字符避免与正文冲突。
const sentinelPrefix = "\u0000OMNI_CAVEMAN"

// PreservationOptions 是保护块抽取选项。对齐 preservation.ts PreservationOptions。
type PreservationOptions struct {
	PreservePatterns []*regexp.Regexp // 用户自定义保护模式
}

func randomSentinelSeed() string {
	// 对齐 preservation.ts:18-26：8 字节随机 hex。
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// fail-open：用固定 seed（极少触发；crypto.rand 失败几乎不可能）。
		return "r0000000000000000"
	}
	return "r" + hex.EncodeToString(b)
}

// extractPreservedBlocks 抽取保护块，用占位符替换，返回替换后文本 + 块列表。
// 对齐 preservation.ts:72-133。
// 顺序：frontmatter → fenced code → 内置模式（含 math_inline RE2 改写）→ 用户模式。
func extractPreservedBlocks(text string, opts PreservationOptions) (string, []PreservedBlock) {
	blocks := []PreservedBlock{}
	seed := randomSentinelSeed()
	counter := 0

	addBlock := func(content, kind string) string {
		placeholder := sentinelPrefix + "_" + seed + "_" + itoa(counter) + "\u0000"
		blocks = append(blocks, PreservedBlock{Placeholder: placeholder, Content: content, Kind: kind})
		counter++
		return placeholder
	}

	result := text
	result = extractFrontmatter(result, addBlock)
	result = extractFencedCodeBlocks(result, func(content string) string {
		return addBlock(content, "fenced_code")
	})

	for _, bp := range builtinProtectedPatterns {
		result = replacePattern(result, bp.pattern, bp.kind, addBlock)
	}
	for _, p := range opts.PreservePatterns {
		result = replacePattern(result, p, "custom", addBlock)
	}
	return result, blocks
}

// compiledPattern 是一个内置保护模式 + kind 标签。
type compiledPattern struct {
	pattern *regexp.Regexp
	kind    string
}

// builtinProtectedPatterns 对齐 preservation.ts:92-126 的 builtIns 列表。
// 全部 RE2 兼容（已审计）。math_inline 已改写：去掉两个 lookbehind，改用
// 捕获组前后字符 + replacePattern 里的校验（见 replacePatternMathInline）。
var builtinProtectedPatterns = []compiledPattern{
	{regexp.MustCompile(`\$\$[\s\S]*?\$\$`), "math_block"},
	{regexp.MustCompile(`\\\[[\s\S]*?\\\]`), "math_block"},
	// math_inline：TS 原 `(?<!\$)\$(?![\s$\d])(?:\\.|[^$\n\\]){1,160}?(?<!\s)\$(?!\$)`
	// RE2 不支持 (?<!\$) 和 (?<!\s)。改写：捕获前导字符 $ 和后随字符，
	// replacePatternMathInline 校验后不是 $ 且后不是空白。
	{mathInlineRE, "math_inline"},
	{regexp.MustCompile(`\\begin\{[A-Za-z*]+\}[\s\S]*?\\end\{[A-Za-z*]+\}`), "latex_block"},
	{regexp.MustCompile(`(?m)^#{1,6}\s+.+$`), "markdown_heading"},
	{regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`), "markdown_table"},
	{regexp.MustCompile(`(?m)^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$`), "markdown_table"},
	{regexp.MustCompile(`(?m)^\s*#(?:set|show|let|import|include)\b.+$`), "typst_directive"},
	{regexp.MustCompile("`[^`\n]+`"), "inline_code"},
	{regexp.MustCompile(`\[[^\]\n]+\]\([^) \n]+(?:\s+"[^"]*")?\)`), "markdown_link"},
	{regexp.MustCompile(`(?i)\bhttps?://[^\s)\]"'>]+`), "url"},
	{regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`), "const_case"},
	{regexp.MustCompile(`\bprocess\.env\.[A-Za-z_][A-Za-z0-9_]*\b`), "env_var"},
	{regexp.MustCompile(`\$[A-Z_][A-Z0-9_]*\b`), "env_var"},
	{regexp.MustCompile(`\b\d+(?:\.\d+){1,3}(?:[-+][A-Za-z0-9.-]+)?\b`), "version"},
	{regexp.MustCompile(`\b[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)+\(\)?`), "dotted_identifier"},
	{regexp.MustCompile(`\b[A-Za-z_$][\w$]*\s*\([^()\n]*\)`), "function_call"},
	{regexp.MustCompile(`(?:^|\s)(?:\.{0,2}/[A-Za-z0-9_@./-]+|[A-Za-z]:\\[A-Za-z0-9_.\\/-]+)`), "file_path"},
	{regexp.MustCompile(`\b(?:TypeError|ReferenceError|SyntaxError|RangeError|URIError|EvalError|Error|Exception):[^\n]+`), "error_message"},
}

// mathInlineRE 是 math_inline 的 RE2 改写版本。
// 原 TS: (?<!\$)\$(?![\s$\d])(?:\\.|[^$\n\\]){1,160}?(?<!\s)\$(?!\$)
//
// GW-07 RE2 适配：Go RE2 不支持 lookahead/lookbehind。原 pattern 含 4 个
// 零宽断言（2 lookbehind + 2 lookahead），全部去除，改用捕获组 + replacePatternMathInline
// 的运行时校验补偿语义：
//   - 捕获组1 (\$?) 前导 $：非空 → 跳过（display math 残余）
//   - 捕获组3 (\s?) 结尾 $ 前字符：空格 → 跳过
//   - 开头 $ 后字符类 [^\s$\d] 替代 (?![\s$\d])：直接要求非空白/$/数字
//   - 结尾 $ 后无约束（原 (?!\$) 省略；display math 由 $$ math_block 先抽走，残留风险低）
var mathInlineRE = regexp.MustCompile(`(\$?)\$([^\s$\d])(?:\\.|[^$\n\\]){0,159}?(\s?)\$`)

// replacePattern 用 addBlock 替换所有 match 为占位符。
// 对齐 preservation.ts:49-61。保护块内已含 sentinel 的不二次替换。
func replacePattern(text string, pattern *regexp.Regexp, kind string, addBlock func(content, kind string) string) string {
	if kind == "math_inline" {
		return replacePatternMathInline(text, pattern, addBlock)
	}
	return pattern.ReplaceAllStringFunc(text, func(m string) string {
		if m == "" || strings.Contains(m, sentinelPrefix) {
			return m
		}
		return addBlock(m, kind)
	})
}

// replacePatternMathInline 处理 math_inline 的 RE2 改写语义：
// 捕获组1非空（前有$）或捕获组2非空（后随空格）→ 不是合法 inline math，保留原 match。
// 否则保护。对齐 TS lookbehind (?<!\$)...(?<!\s) 的语义。
func replacePatternMathInline(text string, pattern *regexp.Regexp, addBlock func(content, kind string) string) string {
	return pattern.ReplaceAllStringFunc(text, func(m string) string {
		if m == "" || strings.Contains(m, sentinelPrefix) {
			return m
		}
		sub := pattern.FindStringSubmatch(m)
		// sub[1]=前导$，sub[3]=结尾$前空格。
		if len(sub) > 1 && sub[1] != "" {
			return m // 前有 $（display math 残余）→ 不保护
		}
		if len(sub) > 3 && sub[3] != "" {
			return m // $ 前是空格 → 不保护
		}
		return addBlock(m, "math_inline")
	})
}

// restorePreservedBlocks 把占位符还原为原文。对齐 preservation.ts:221-227。
func restorePreservedBlocks(text string, blocks []PreservedBlock) string {
	result := text
	for _, b := range blocks {
		result = strings.ReplaceAll(result, b.Placeholder, b.Content)
	}
	return result
}

// extractFrontmatter 抽取 YAML frontmatter（---\n...\n---）。对齐 preservation.ts:163-173。
func extractFrontmatter(text string, addBlock func(content, kind string) string) string {
	if !strings.HasPrefix(text, "---\n") {
		return text
	}
	close := strings.Index(text[4:], "\n---")
	if close == -1 {
		return text
	}
	closeEnd := strings.Index(text[4+close+4:], "\n")
	var end int
	if closeEnd == -1 {
		end = len(text)
	} else {
		end = 4 + close + 4 + closeEnd + 1
	}
	return addBlock(text[:end], "frontmatter") + text[end:]
}

// extractFencedCodeBlocks 逐行扫描 fence 开闭，整块抽取。对齐 preservation.ts:175-219。
func extractFencedCodeBlocks(text string, addBlock func(content string) string) string {
	lines := splitLines(text)
	var b strings.Builder
	i := 0
	fenceOpenRE := regexp.MustCompile(`^([ \t]{0,3})(` + "`{3,}" + `|~{3,})[^\n]*(?:\n|$)`)
	fenceCloseRE := regexp.MustCompile(`^([ \t]{0,3})(` + "`{3,}" + `|~{3,})\s*(?:\n|$)`)

	for i < len(lines) {
		line := lines[i]
		if line == "" && i == len(lines)-1 {
			break
		}
		opening := fenceOpenRE.FindStringSubmatch(line)
		if opening == nil {
			b.WriteString(line)
			i++
			continue
		}
		fence := opening[2]
		fenceChar := string(fence[0])
		minLen := len(fence)
		block := line
		j := i + 1
		closed := false
		for j < len(lines) {
			cand := lines[j]
			block += cand
			closeMatch := fenceCloseRE.FindStringSubmatch(cand)
			if closeMatch != nil && string(closeMatch[2][0]) == fenceChar && len(closeMatch[2]) >= minLen {
				closed = true
				break
			}
			j++
		}
		if !closed {
			b.WriteString(line)
			i++
			continue
		}
		b.WriteString(addBlock(block))
		i = j + 1
	}
	return b.String()
}

// splitLines 把文本按行切分，保留行尾换行（对齐 TS /[^\n]*(?:\n|$)/g）。
func splitLines(text string) []string {
	if text == "" {
		return []string{""}
	}
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// findFencedCodeBlocks 公开 helper（validation.go 用）。对齐 preservation.ts:63-70。
func findFencedCodeBlocks(text string) []string {
	var blocks []string
	extractFencedCodeBlocks(text, func(content string) string {
		blocks = append(blocks, content)
		return content
	})
	return blocks
}

// itoa 是不引入 strconv 的极简整数→字符串（占位符 counter 用）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
