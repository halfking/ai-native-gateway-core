// Package toolfocused 实现 omni-ref2 GW-09 的 Tool-Focused 压缩 stage。
//
// 翻译自 OmniRoute 的 open-sse/services/compression/toolResultCompressor.ts。
// 5 种 per-type 工具结果压缩策略（按顺序尝试，第一个命中生效）：
//  1. fileContent  — 代码文件：保留前 20 行 + 后 5 行，中间折行为 "[N lines elided]"
//  2. grepSearch   — grep/搜索输出：保留前 30 条匹配 + 文件清单去重
//
// 3. shellOutput   — shell 输出：剥离 ANSI 转义码，保留去重后的末 50 行
//  4. json         — JSON > 2000 字符：数组取头 5 尾 2，对象取前 20 key 摘要
//  5. errorMessage — 错误/堆栈：首行 + 头 10 帧 + 尾 3 帧，中间折行
//
// 设计约束（对齐 lite/caveman 的 GW-05/GW-07 门禁）：
//   - fail-open：任何解析/处理错误返回原 body，不抛 panic 到调用方。
//   - 纯函数、无副作用。
//   - [COMPRESSED: 前缀幂等：已压缩的内容不再二次压缩。
//   - 不碰执行器实时路径（接线走 Compressor.ToolFocusedStageEnabled feature flag）。
package toolfocused

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Strategies 对齐 OmniRoute ToolStrategiesConfig：逐策略开关。
// 全部 true 时与 aggressive.ts 默认行为一致。
type Strategies struct {
	FileContent  bool
	GrepSearch   bool
	ShellOutput  bool
	JSON         bool
	ErrorMessage bool
}

// DefaultStrategies 返回 OmniRoute aggressive 默认全开配置。
func DefaultStrategies() Strategies {
	return Strategies{
		FileContent:  true,
		GrepSearch:   true,
		ShellOutput:  true,
		JSON:         true,
		ErrorMessage: true,
	}
}

// Result 是 Apply 返回的 stage 应用结果。
type Result struct {
	Techniques  []string // 命中的策略名（fileContent|grepSearch|...），按消息顺序去重
	InputChars  int
	OutputChars int
	// SavedTokens 对齐 TS saved 字段：sum(estimateTokens(before)-estimateTokens(after))，
	// 仅统计真实压缩的 message。
	SavedTokens int
}

// compressMarkerPrefix 对齐 TS COMPRESSED_MARKER_RE = /^\[COMPRESSED:/
func compressMarkerPrefix(s string) bool {
	return strings.HasPrefix(s, "[COMPRESSED:")
}

// estimateTokens 对齐 TS estimateTokens = ceil(len/4)。
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// ── 策略 1: fileContent ─────────────────────────────────────────────

// codeLikeLinePrefixes 对齐 TS isCodeLikeLine 的 startsWith 集合。
var codeLikeLinePrefixes = []string{
	"import ", "export ", "function ", "class ", "const ", "let ", "var ",
	"return ", "if(", "if (", "for(", "for (", "while(", "while (",
}

// isCodeLikeLine 对齐 TS isCodeLikeLine：trimStart 后以代码关键字开头。
func isCodeLikeLine(rawLine string) bool {
	line := strings.TrimLeft(rawLine, " \t\v\f\r")
	for _, p := range codeLikeLinePrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// compressFileContent 对齐 TS compressFileContent：
// ≥3 行且含代码行才处理；≤ keep+tail 行原样返回；否则保头 20 行 + "[N lines elided]" + 尾 5 行。
func compressFileContent(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return "", false
	}
	anyCode := false
	for _, l := range lines {
		if isCodeLikeLine(l) {
			anyCode = true
			break
		}
	}
	if !anyCode {
		return "", false
	}
	const keep = 20
	const tail = 5
	if len(lines) <= keep+tail {
		return content, false // TS 返回 content 本身（saved=0），调用方按未命中处理
	}
	head := strings.Join(lines[:keep], "\n")
	tailLines := strings.Join(lines[len(lines)-tail:], "\n")
	elided := len(lines) - keep - tail
	return fmt.Sprintf("%s\n… [%d lines elided] …\n%s", head, elided, tailLines), true
}

// ── 策略 2: grepSearch ──────────────────────────────────────────────

// parseGrepLinePath 对齐 TS parseGrepLinePath：`path:line:...` 形态解析文件路径。
// 返回 ("", false) 表示该行不是 grep 输出行。
func parseGrepLinePath(line string) (string, bool) {
	firstColon := strings.IndexByte(line, ':')
	if firstColon <= 0 {
		return "", false
	}
	secondColon := strings.IndexByte(line[firstColon+1:], ':')
	if secondColon == -1 {
		return "", false
	}
	secondColon += firstColon + 1
	lineNumber := line[firstColon+1 : secondColon]
	if lineNumber == "" {
		return "", false
	}
	for _, c := range lineNumber {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	filePath := line[:firstColon]
	if filePath == "" || strings.ContainsAny(filePath, " \t") {
		return "", false
	}
	return filePath, true
}

// compressGrepSearch 对齐 TS compressGrepSearch：
// 过滤出 grep 行；无则不命中；否则保前 30 条 + 去重文件清单。
func compressGrepSearch(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	var grepLines []string
	paths := map[string]bool{}
	var pathOrder []string
	for _, line := range lines {
		if p, ok := parseGrepLinePath(line); ok {
			grepLines = append(grepLines, line)
			if !paths[p] {
				paths[p] = true
				pathOrder = append(pathOrder, p)
			}
		}
	}
	if len(grepLines) == 0 {
		return "", false
	}
	const top = 30
	if len(grepLines) <= top {
		// TS 无 remaining 分支时结果可能长于原文（追加 Files: 行），由 NeverWorse 兜底；
		// 这里保语义一致：仍返回拼接结果。
		return strings.Join(grepLines, "\n") + "\nFiles: " + strings.Join(pathOrder, ", "), true
	}
	result := strings.Join(grepLines[:top], "\n")
	result += fmt.Sprintf("\n… [%d more matches]", len(grepLines)-top)
	result += "\nFiles: " + strings.Join(pathOrder, ", ")
	return result, true
}

// ── 策略 3: shellOutput ─────────────────────────────────────────────

// hasANSI 对齐 TS ANSI_RE = /\x1b\[[0-9;]*[a-zA-Z]/ 的检测（不含全局替换语义）。
func hasANSI(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == 0x1b && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j > i+2 && j < len(s) && isASCIILetter(s[j]) {
				return true
			}
		}
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// stripANSI 对齐 TS content.replace(ANSI_RE, "")：删除所有 CSI 序列。
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j > i+2 && j < len(s) && isASCIILetter(s[j]) {
				i = j // 跳过整个序列（含终止字母）
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hasShellPrompt 对齐 TS SHELL_PROMPT_RE = /\$\s/。
func hasShellPrompt(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && (s[i+1] == ' ' || s[i+1] == '\t' || s[i+1] == '\n' || s[i+1] == '\r' || s[i+1] == '\v' || s[i+1] == '\f') {
			return true
		}
	}
	return false
}

// compressShellOutput 对齐 TS compressShellOutput：
// 必须含 ANSI 或 shell prompt 才命中；剥离 ANSI 后保留末 50 行并去除连续重复行。
func compressShellOutput(content string) (string, bool) {
	if !hasANSI(content) && !hasShellPrompt(content) {
		return "", false
	}
	cleaned := stripANSI(content)
	lines := strings.Split(cleaned, "\n")
	start := len(lines) - 50
	if start < 0 {
		start = 0
	}
	last50 := lines[start:]
	deduped := last50[:0]
	for _, line := range last50 {
		if len(deduped) == 0 || line != deduped[len(deduped)-1] {
			deduped = append(deduped, line)
		}
	}
	return strings.Join(deduped, "\n"), true
}

// ── 策略 4: json ────────────────────────────────────────────────────

// compressJSON 对齐 TS compressJson：>2000 字符且可解析为 JSON 才处理。
// 数组 ≤7 或对象 ≤20 key 时 TS 返回原 content（未命中）。
func compressJSON(content string) (string, bool) {
	if len(content) <= 2000 {
		return "", false
	}
	trimmed := strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	var parsed any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return "", false
	}
	switch v := parsed.(type) {
	case []any:
		if len(v) <= 7 {
			return "", false
		}
		head := v[:5]
		tail := v[len(v)-2:]
		out := map[string]any{
			"type":   "array",
			"total":  len(v),
			"first5": head,
			"last2":  tail,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return "", false
		}
		return string(b), true
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// Object.keys 顺序 = 插入顺序；Go map 无序，这里按 key 排序保证确定性。
		sortStrings(keys)
		summary := map[string]any{}
		for _, k := range keys {
			if len(summary) >= 20 {
				break
			}
			val := v[k]
			if m, ok := val.(map[string]any); ok {
				summary[k] = fmt.Sprintf("{…%d keys}", len(m))
			} else {
				summary[k] = val
			}
		}
		if len(keys) > 20 {
			summary[fmt.Sprintf("_remaining_%d_keys", len(keys)-20)] = true
		}
		b, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

// sortStrings 保持与 TS 相同的字典序，但用库排序替代插入排序：
// 工具结果可以是数十万 key 的巨型 JSON 对象，O(n²) 会把请求卡死在这里。
func sortStrings(s []string) {
	sort.Strings(s)
}

// ── 策略 5: errorMessage ────────────────────────────────────────────

// hasErrorLikeOutput 对齐 TS hasErrorLikeOutput。
func hasErrorLikeOutput(content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range []string{
		"error:", "error ", "[error]", "exception:", "exception ", "[exception]", "traceback",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// compressErrorMessage 对齐 TS compressErrorMessage：
// 首行 + 头 10 帧 + "[N frames elided]" + 尾 3 帧。
func compressErrorMessage(content string) (string, bool) {
	if !hasErrorLikeOutput(content) {
		return "", false
	}
	lines := strings.Split(content, "\n")
	errorLine := ""
	if len(lines) > 0 {
		errorLine = lines[0]
	}
	stackLines := lines[1:]
	head := stackLines
	if len(head) > 10 {
		head = head[:10]
	}
	var tail []string
	if len(stackLines) > 10 {
		tail = stackLines[len(stackLines)-3:]
	}
	var middle []string
	if len(stackLines) > 13 {
		middle = []string{fmt.Sprintf("… [%d frames elided] …", len(stackLines)-13)}
	}
	parts := make([]string, 0, 1+len(head)+len(middle)+len(tail))
	parts = append(parts, errorLine)
	parts = append(parts, head...)
	parts = append(parts, middle...)
	parts = append(parts, tail...)
	return strings.Join(parts, "\n"), true
}

// ── 组合入口 ────────────────────────────────────────────────────────

// CompressToolResult 对齐 TS compressToolResult：按 fileContent → grepSearch →
// shellOutput → json → errorMessage 顺序尝试，第一个命中返回。
// 返回 (压缩后文本, 策略名, 节省 token 数, 是否命中)。
func CompressToolResult(content string, opts Strategies) (string, string, int, bool) {
	if opts.FileContent {
		if out, ok := compressFileContent(content); ok {
			return out, "fileContent", estimateTokens(content) - estimateTokens(out), true
		}
	}
	if opts.GrepSearch {
		if out, ok := compressGrepSearch(content); ok {
			return out, "grepSearch", estimateTokens(content) - estimateTokens(out), true
		}
	}
	if opts.ShellOutput {
		if out, ok := compressShellOutput(content); ok {
			return out, "shellOutput", estimateTokens(content) - estimateTokens(out), true
		}
	}
	if opts.JSON {
		if out, ok := compressJSON(content); ok {
			return out, "json", estimateTokens(content) - estimateTokens(out), true
		}
	}
	if opts.ErrorMessage {
		if out, ok := compressErrorMessage(content); ok {
			return out, "errorMessage", estimateTokens(content) - estimateTokens(out), true
		}
	}
	return content, "none", 0, false
}

// Apply 对一个 OpenAI/Anthropic chat body 字节跑 tool-focused 压缩。
// 遍历 messages：
//   - OpenAI shape：role=tool/function 且 content 为 string 的消息
//   - Anthropic shape：content 数组里 type=tool_result 的 block（string 或
//     []{type:"text",text} 双形态，对齐 compressAnthropicToolResultBlock）
//
// [COMPRESSED: 前缀的内容跳过（幂等）。system 消息不碰（preserveSystemPrompt 语义）。
// fail-open：body 非合法 JSON / 无 messages 时返回原 body。
func Apply(body []byte, opts Strategies) ([]byte, Result, bool) {
	res := Result{InputChars: len(body), OutputChars: len(body)}
	if len(body) == 0 {
		return body, res, false
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, res, false
	}
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, res, false
	}

	seenTechniques := map[string]bool{}
	changed := false
	nextMsgs := make([]any, 0, len(msgs))

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			nextMsgs = append(nextMsgs, m)
			continue
		}
		role, _ := msg["role"].(string)

		// OpenAI shape: tool/function 消息，content 为 string。
		if role == "tool" || role == "function" {
			if content, ok := msg["content"].(string); ok && content != "" {
				if compressMarkerPrefix(content) {
					nextMsgs = append(nextMsgs, m)
					continue
				}
				if out, strategy, saved, hit := CompressToolResult(content, opts); hit && saved > 0 {
					nextMsgs = append(nextMsgs, cloneWithContent(msg, out))
					seenTechniques[strategy] = true
					res.SavedTokens += saved
					changed = true
					continue
				}
			}
			nextMsgs = append(nextMsgs, m)
			continue
		}

		// Anthropic shape: content 数组里的 tool_result block。
		if blocks, ok := msg["content"].([]any); ok {
			blockChanged := false
			nextBlocks := make([]any, 0, len(blocks))
			for _, b := range blocks {
				block, ok := b.(map[string]any)
				if !ok || block["type"] != "tool_result" {
					nextBlocks = append(nextBlocks, b)
					continue
				}
				nb, saved, hit, strategy := compressAnthropicBlock(block, opts)
				if hit {
					nextBlocks = append(nextBlocks, nb)
					res.SavedTokens += saved
					seenTechniques[strategy] = true
					blockChanged = true
				} else {
					nextBlocks = append(nextBlocks, b)
				}
			}
			if blockChanged {
				nm := make(map[string]any, len(msg))
				for k, v := range msg {
					nm[k] = v
				}
				nm["content"] = nextBlocks
				nextMsgs = append(nextMsgs, nm)
				changed = true
				continue
			}
		}

		nextMsgs = append(nextMsgs, m)
	}

	if !changed {
		return body, res, false
	}

	// 按 Apply 内命中顺序输出策略名（保持与 lite.Result.Techniques 语义一致）。
	res.Techniques = make([]string, 0, len(seenTechniques))
	for _, t := range []string{"fileContent", "grepSearch", "shellOutput", "json", "errorMessage"} {
		if seenTechniques[t] {
			res.Techniques = append(res.Techniques, t)
		}
	}

	doc["messages"] = nextMsgs
	out, err := json.Marshal(doc)
	if err != nil {
		return body, res, false // fail-open
	}
	res.OutputChars = len(out)
	return out, res, true
}

// cloneWithContent 浅拷贝 message 并替换 content 字段（保持其他字段原样）。
func cloneWithContent(msg map[string]any, content string) map[string]any {
	nm := make(map[string]any, len(msg)+1)
	for k, v := range msg {
		nm[k] = v
	}
	nm["content"] = content
	return nm
}

// compressAnthropicBlock 对齐 TS compressAnthropicToolResultBlock：
// content 为 string 时压缩文本；为 []{type:"text"} 时逐 part 压缩。
// tool_use_id 与 block 结构保持不变，只动内部文本。
// 返回 (新 block, saved, 是否命中, 命中的策略名)。
func compressAnthropicBlock(block map[string]any, opts Strategies) (map[string]any, int, bool, string) {
	switch content := block["content"].(type) {
	case string:
		if content == "" || compressMarkerPrefix(content) {
			return block, 0, false, ""
		}
		out, strategy, saved, hit := CompressToolResult(content, opts)
		if !hit || saved <= 0 {
			return block, 0, false, ""
		}
		nb := cloneMap(block)
		nb["content"] = out
		return nb, saved, true, strategy
	case []any:
		saved := 0
		changed := false
		firstStrategy := ""
		nextContent := make([]any, 0, len(content))
		for _, part := range content {
			pm, ok := part.(map[string]any)
			if !ok || pm["type"] != "text" {
				nextContent = append(nextContent, part)
				continue
			}
			text, ok := pm["text"].(string)
			if !ok || text == "" || compressMarkerPrefix(text) {
				nextContent = append(nextContent, part)
				continue
			}
			out, strategy, s, hit := CompressToolResult(text, opts)
			if !hit || s <= 0 {
				nextContent = append(nextContent, part)
				continue
			}
			if firstStrategy == "" {
				firstStrategy = strategy
			}
			npm := cloneMap(pm)
			npm["text"] = out
			nextContent = append(nextContent, npm)
			saved += s
			changed = true
		}
		if !changed {
			return block, 0, false, ""
		}
		nb := cloneMap(block)
		nb["content"] = nextContent
		return nb, saved, true, firstStrategy
	}
	return block, 0, false, ""
}

func cloneMap(m map[string]any) map[string]any {
	nm := make(map[string]any, len(m))
	for k, v := range m {
		nm[k] = v
	}
	return nm
}
