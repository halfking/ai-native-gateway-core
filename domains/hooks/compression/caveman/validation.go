package caveman

import (
	"fmt"
	"strings"
)

// ValidationResult 是压缩后校验结果。对齐 validation.ts:3-8。
type ValidationResult struct {
	Valid           bool
	Errors          []string
	Warnings        []string
	FallbackApplied bool
}

// validateCompression 校验压缩后文本是否保留了所有受保护结构。
// 对齐 validation.ts:94-142。纯字符扫描，ReDoS-immune。
//
// 检查 12 类结构在 compressed 中仍存在：fenced code/inline code/url/markdown link/
// frontmatter/heading/table row/math block/inline math/latex block/version/const_case。
// fenced code 还查数量不下降。length > original 只是 warning。
func validateCompression(original, compressed string) ValidationResult {
	if compressed == "" {
		// 类型守卫：TS 对非 string 返回 invalid；Go 这里 compressed 是 string。
		// 空压缩输出（original 非空）→ error。
		if len(original) > 0 && strings.TrimSpace(compressed) == "" {
			return ValidationResult{Valid: false, Errors: []string{"compressed text is empty"}, FallbackApplied: true}
		}
	}

	var errors []string
	var warnings []string

	// 12 类精确存在性检查（对齐 validation.ts:111-122）。
	requireExactPresence("fenced code block", findFencedCodeBlocks(original), compressed, &errors)
	requireExactPresence("inline code", collectInlineCode(original), compressed, &errors)
	requireExactPresence("URL", collectUrls(original), compressed, &errors)
	requireExactPresence("markdown link", collectMarkdownLinks(original), compressed, &errors)
	requireExactPresence("frontmatter", collectFrontmatter(original), compressed, &errors)
	requireExactPresence("heading", collectHeadings(original), compressed, &errors)
	requireExactPresence("table row", collectTableRows(original), compressed, &errors)
	requireExactPresence("math block", collectMathBlocks(original), compressed, &errors)
	requireExactPresence("inline math", collectInlineMath(original), compressed, &errors)
	requireExactPresence("latex block", collectLatexBlocks(original), compressed, &errors)
	requireExactPresence("version", collectVersions(original), compressed, &errors)
	requireExactPresence("CONST_CASE", collectConstCase(original), compressed, &errors)

	// fenced code 数量不下降（对齐 validation.ts:124-130）。
	origFenced := len(findFencedCodeBlocks(original))
	compFenced := len(findFencedCodeBlocks(compressed))
	if compFenced < origFenced {
		errors = append(errors, fmt.Sprintf("fenced code block count dropped: %d -> %d", origFenced, compFenced))
	}

	// length warning（对齐 validation.ts:132-134）。只是 warning。
	if len(compressed) > len(original) {
		warnings = append(warnings, "compressed text is longer than original")
	}

	return ValidationResult{
		Valid:           len(errors) == 0,
		Errors:          errors,
		Warnings:        warnings,
		FallbackApplied: len(errors) > 0,
	}
}

// requireExactPresence 断言 compressed 仍含 items 的每一项。对齐 validation.ts:10-22。
func requireExactPresence(label string, items []string, compressed string, errors *[]string) {
	for _, item := range items {
		if !strings.Contains(compressed, item) {
			preview := item
			if len(preview) > 80 {
				preview = preview[:80]
			}
			*errors = append(*errors, fmt.Sprintf("%s changed or missing: %s", label, preview))
		}
	}
}

// ---- collectors（手写字符扫描，对齐 validation.ts:144-432）----

// collectInlineCode 收集 `code` 形式的 inline code。对齐 validation.ts:144-160。
func collectInlineCode(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '`' {
			continue
		}
		// 找下一个 backtick（同行）。
		start := i
		j := i + 1
		for j < len(runes) && runes[j] != '`' && runes[j] != '\n' {
			j++
		}
		if j < len(runes) && runes[j] == '`' && j > start+1 {
			out = append(out, string(runes[start:j+1]))
			i = j
		}
	}
	return out
}

// collectUrls 收集 http(s):// URL。对齐 validation.ts:162-183。
func collectUrls(text string) []string {
	var out []string
	lower := strings.ToLower(text)
	idx := 0
	for {
		i := strings.Index(lower[idx:], "http")
		if i == -1 {
			break
		}
		i += idx
		rest := text[i:]
		if !strings.HasPrefix(strings.ToLower(rest), "https://") && !strings.HasPrefix(strings.ToLower(rest), "http://") {
			idx = i + 4
			continue
		}
		// URL 持续到空白或结束符。
		end := 0
		for end < len(rest) {
			c := rest[end]
			if c == ' ' || c == '\t' || c == '\n' || c == ')' || c == ']' || c == '"' || c == '\'' || c == '>' {
				break
			}
			end++
		}
		if end > 0 {
			out = append(out, rest[:end])
		}
		idx = i + end
		if end == 0 {
			idx = i + 4
		}
	}
	return out
}

// collectMarkdownLinks 收集 [label](target)。对齐 validation.ts:185-214。
func collectMarkdownLinks(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		close := strings.IndexByte(text[i:], ']')
		if close == -1 {
			continue
		}
		close += i
		if close+1 >= len(text) || text[close+1] != '(' {
			continue
		}
		// label 长度限制 1000（对齐 validation.ts:192）。
		if close-i > 1000 {
			continue
		}
		targetClose := strings.IndexByte(text[close+2:], ')')
		if targetClose == -1 {
			continue
		}
		target := text[close+2 : close+2+targetClose]
		if len(target) > 2000 {
			continue
		}
		out = append(out, text[i:close+2+targetClose+1])
		i = close + 2 + targetClose
	}
	return out
}

// collectFrontmatter 收集 ---\n...\n---。对齐 validation.ts:216-223。
func collectFrontmatter(text string) []string {
	if !strings.HasPrefix(text, "---\n") {
		return nil
	}
	close := strings.Index(text[4:], "\n---")
	if close == -1 {
		return nil
	}
	closeEnd := strings.Index(text[4+close+4:], "\n")
	var end int
	if closeEnd == -1 {
		end = len(text)
	} else {
		end = 4 + close + 4 + closeEnd + 1
	}
	return []string{text[:end]}
}

// collectHeadings 收集 1-6 个 # 开头的标题行。对齐 validation.ts:225-239。
func collectHeadings(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		n := 0
		for n < len(trimmed) && trimmed[n] == '#' {
			n++
		}
		if n >= 1 && n <= 6 && n < len(trimmed) && trimmed[n] == ' ' {
			out = append(out, line)
		}
	}
	return out
}

// collectTableRows 收集 | 分隔的表格行。对齐 validation.ts:241-255。
func collectTableRows(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		// 至少 2 个 |（对齐 validation.ts:249）。
		if strings.Count(line, "|") >= 2 && strings.Contains(line, "|") {
			out = append(out, line)
		}
	}
	return out
}

// collectMathBlocks 收集 $$...$$。对齐 validation.ts:271-290。
func collectMathBlocks(text string) []string {
	var out []string
	idx := 0
	for {
		i := strings.Index(text[idx:], "$$")
		if i == -1 {
			break
		}
		i += idx
		close := strings.Index(text[i+2:], "$$")
		if close == -1 {
			break
		}
		close += i + 2
		block := text[i : close+2]
		if len(block) <= 10000 { // 对齐 validation.ts:281
			out = append(out, block)
		}
		idx = close + 2
	}
	return out
}

// collectInlineMath 收集 $...$。对齐 validation.ts:292-321。
func collectInlineMath(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '$' {
			continue
		}
		// 前后不能是 $ 或空白（对齐 validation.ts:302）。
		if i > 0 && (runes[i-1] == '$') {
			continue
		}
		j := i + 1
		for j < len(runes) && runes[j] != '$' && runes[j] != '\n' {
			j++
		}
		if j < len(runes) && runes[j] == '$' && j > i+1 {
			block := string(runes[i : j+1])
			if len(block) <= 160 { // 对齐 validation.ts:302
				out = append(out, block)
			}
			i = j
		}
	}
	return out
}

// collectLatexBlocks 收集 \begin{env}...\end{env}。对齐 validation.ts:323-361。
func collectLatexBlocks(text string) []string {
	var out []string
	idx := 0
	for {
		i := strings.Index(text[idx:], `\begin{`)
		if i == -1 {
			break
		}
		i += idx
		envClose := strings.IndexByte(text[i+7:], '}')
		if envClose == -1 {
			break
		}
		env := text[i+7 : i+7+envClose]
		endMarker := `\end{` + env + "}"
		end := strings.Index(text[i:], endMarker)
		if end == -1 {
			break
		}
		end += i + len(endMarker)
		block := text[i:end]
		if len(block) <= 10000 { // 对齐 validation.ts:351
			out = append(out, block)
		}
		idx = end
	}
	return out
}

// collectVersions 收集 N.N[.N[.N]][-suffix] 形式的版本号。对齐 validation.ts:363-391。
func collectVersions(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if !isDigit(runes[i]) {
			continue
		}
		// 至少一个点（N.N）。
		j := i
		dotCount := 0
		for j < len(runes) {
			if isDigit(runes[j]) {
				j++
			} else if runes[j] == '.' && j+1 < len(runes) && isDigit(runes[j+1]) {
				dotCount++
				j++
			} else {
				break
			}
		}
		if dotCount >= 1 {
			// 可选 -suffix / +suffix。
			if j < len(runes) && (runes[j] == '-' || runes[j] == '+') {
				k := j + 1
				for k < len(runes) && (isAlnum(runes[k]) || runes[k] == '.' || runes[k] == '-') {
					k++
				}
				j = k
			}
			out = append(out, string(runes[i:j]))
			i = j - 1
		}
	}
	return out
}

// collectConstCase 收集 UPPER_SNAKE_CASE（至少含一个下划线）。对齐 validation.ts:405-432。
func collectConstCase(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if !isUpperOrDigit(runes[i]) {
			continue
		}
		j := i
		hasUnder := false
		for j < len(runes) && (isUpperOrDigit(runes[j]) || runes[j] == '_') {
			if runes[j] == '_' {
				hasUnder = true
			}
			j++
		}
		if hasUnder && j > i+1 {
			out = append(out, string(runes[i:j]))
			i = j - 1
		}
	}
	return out
}

func isDigit(r rune) bool        { return r >= '0' && r <= '9' }
func isAlnum(r rune) bool        { return isDigit(r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
func isUpperOrDigit(r rune) bool { return (r >= 'A' && r <= 'Z') || isDigit(r) }
