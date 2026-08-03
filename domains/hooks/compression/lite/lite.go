// Package lite 实现 omni-ref2 GW-05 的 Lite 压缩 stage。
//
// 翻译自 OmniRoute 的 open-sse/services/compression/lite.ts（commit c8f1d62de）。
// SOURCE-VERIFIED: lite.ts 是纯 charCode 循环，零正则、零 lookbehind（RE2-safe
// 原样翻译，不需改写）。preservation.ts 的保护块抽取不在本包——那是 Caveman
// （GW-07）的职责，lite.ts 不 import 它。
//
// 5 个 stage 顺序与 lite.ts:248-266 一致：
//  1. whitespace          — 折叠连续换行（≤2）、清理行尾水平空白
//  2. system-dedup        — 删除与前一条前 200 字符 trim key 重复的 system 消息
//  3. tool-compress       — 截断 >2000 字符的 tool result 到词边界 + "...[truncated]"
//  4. redundant-remove    — 删除与紧邻前一条同 role 同 content 的消息
//  5. image-placeholder   — 非 vision 模型时，data:image/ url → "[image: fmt]"
//
// 设计约束（README §5 C1 门禁）：
//   - fail-open：任何解析/处理错误返回原 body，不抛 panic 到调用方。
//   - 纯函数、无副作用，可在同 request scope 内安全 retry（不重复消耗配额）。
//   - 不碰执行器实时路径（执行器接线是 GW-08/Pipeline.Apply，本轮不做）。
package lite

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Options 对齐 lite.ts:16-20 的 LiteCompressionOptions。
type Options struct {
	Model                string // 用于 vision 检测；若 SupportsVision 已知则不用
	SupportsVision       *bool  // nil=未知；false 才触发 image 占位（与 TS !== false 一致）
	PreserveSystemPrompt bool   // true 时跳过 system 消息的 whitespace/dedup
}

// Result 是 Apply 返回的 stage 应用结果。
type Result struct {
	Techniques []string // 应用的 stage 名（whitespace|system-dedup|...），有序
	InputChars int
	OutputChars int
}

// Apply 对一个 OpenAI/Anthropic chat body 字节跑 5 个 Lite stage。
// 返回 (newBody, result, applied)。applied=false 表示无 stage 改动（newBody==body）。
//
// fail-open：如果 body 不是合法 JSON 或没有 messages 字段，返回原 body, 零值 Result, false。
func Apply(body []byte, opts Options) ([]byte, Result, bool) {
	res := Result{InputChars: len(body), OutputChars: len(body)}
	if len(body) == 0 {
		return body, res, false
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		// fail-open：非合法 JSON，原样返回。
		return body, res, false
	}
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, res, false
	}

	current := msgs
	techniques := []string{}

	// Stage 1: whitespace
	next, applied := collapseWhitespace(current, opts)
	if applied {
		techniques = append(techniques, "whitespace")
		current = next
	}
	// Stage 2: system-dedup
	next, applied = dedupSystemPrompt(current, opts)
	if applied {
		techniques = append(techniques, "system-dedup")
		current = next
	}
	// Stage 3: tool-compress
	next, applied = compressToolResults(current)
	if applied {
		techniques = append(techniques, "tool-compress")
		current = next
	}
	// Stage 4: redundant-remove
	next, applied = removeRedundantContent(current, opts)
	if applied {
		techniques = append(techniques, "redundant-remove")
		current = next
	}
	// Stage 5: image-placeholder
	next, applied = replaceImageUrls(current, opts)
	if applied {
		techniques = append(techniques, "image-placeholder")
		current = next
	}

	if len(techniques) == 0 {
		return body, res, false
	}
	doc["messages"] = current
	out, err := json.Marshal(doc)
	if err != nil {
		// fail-open
		return body, res, false
	}
	res.Techniques = techniques
	res.OutputChars = len(out)
	return out, res, true
}

// ---- helpers shared by stages ----

// msgRole 取消息的 role（安全）。
func msgRole(m map[string]any) string {
	if r, ok := m["role"].(string); ok {
		return r
	}
	return ""
}

// contentString 取消息 content 的字符串形式（string 直接返回；array/object 走 JSON）。
// 对齐 lite.ts:185 的 typeof === "string" ? content : JSON.stringify(content)。
func contentString(c any) (string, bool) {
	if s, ok := c.(string); ok {
		return s, true
	}
	// 非 string：用 JSON 表示做比较（与 TS 行为一致）。
	b, err := json.Marshal(c)
	if err != nil {
		return "", false
	}
	return string(b), false
}

// ---- Stage 1: whitespace (lite.ts:22-82) ----

// trimTrailingHorizontalWhitespace 去掉行尾的空格(32)和制表符(9)。
func trimTrailingHorizontalWhitespace(line string) string {
	// 按 rune 处理；TS 按 charCodeAt（UTF-16 码元）。对 ASCII 空白二者一致。
	runes := []rune(line)
	end := len(runes)
	for end > 0 {
		r := runes[end-1]
		if r != ' ' && r != '\t' {
			break
		}
		end--
	}
	if end == len(runes) {
		return line
	}
	return string(runes[:end])
}

// collapseNewlineRuns 把连续 >2 个 \n 折叠为最多 2 个。
func collapseNewlineRuns(content string) string {
	var b strings.Builder
	b.Grow(len(content))
	newlineRun := 0
	for _, r := range content {
		if r == '\n' {
			newlineRun++
			if newlineRun <= 2 {
				b.WriteRune(r)
			}
			continue
		}
		newlineRun = 0
		b.WriteRune(r)
	}
	return b.String()
}

// normalizeMessageWhitespace = collapseNewlineRuns then per-line trim trailing。
func normalizeMessageWhitespace(content string) string {
	collapsed := collapseNewlineRuns(content)
	lines := strings.Split(collapsed, "\n")
	for i, ln := range lines {
		lines[i] = trimTrailingHorizontalWhitespace(ln)
	}
	return strings.Join(lines, "\n")
}

// collapseWhitespace 对每条 string-content 消息做空白归一（system 可选保留）。
func collapseWhitespace(msgs []any, opts Options) ([]any, bool) {
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		if opts.PreserveSystemPrompt && msgRole(msg) == "system" {
			out[i] = m
			continue
		}
		c, ok := msg["content"].(string)
		if !ok {
			out[i] = m
			continue
		}
		normalized := normalizeMessageWhitespace(c)
		if normalized != c {
			applied = true
			nm := copyMsg(msg)
			nm["content"] = normalized
			out[i] = nm
			continue
		}
		out[i] = m
	}
	return out, applied
}

// ---- Stage 2: system-dedup (lite.ts:84-106) ----

func dedupSystemPrompt(msgs []any, opts Options) ([]any, bool) {
	if opts.PreserveSystemPrompt {
		return msgs, false
	}
	seen := make(map[string]struct{})
	applied := false
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msgRole(msg) != "system" {
			out = append(out, m)
			continue
		}
		c, isStr := msg["content"].(string)
		if !isStr {
			out = append(out, m)
			continue
		}
		key := first200Trimmed(c)
		if _, dup := seen[key]; dup {
			applied = true
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out, applied
}

func first200Trimmed(s string) string {
	t := strings.TrimSpace(s)
	// TS slice(0,200) 按 UTF-16 码元；Go 按 rune。对 ASCII 一致；多字节字符可能略短，
	// 但 dedup key 只需稳定，不需与 TS 字节一致。
	rs := []rune(t)
	if len(rs) > 200 {
		return string(rs[:200])
	}
	return t
}

// ---- Stage 3: tool-compress (lite.ts:114-167) ----

const (
	maxToolLength          = 2000
	toolTruncationLookback = 80
	truncationMarker       = "\n...[truncated]"
)

// isWordChar 对齐 TS /\S/.test(char)：非空白即 word char。
func isWordChar(r rune) bool {
	return !unicode.IsSpace(r)
}

func compressToolResults(msgs []any) ([]any, bool) {
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msgRole(msg) != "tool" {
			out[i] = m
			continue
		}
		c, ok := msg["content"].(string)
		if !ok || len([]rune(c)) <= maxToolLength {
			out[i] = m
			continue
		}
		applied = true
		cut := backOffToWordBoundary(c, maxToolLength)
		// TS slice(0, cutIndex) 按 UTF-16；这里按 rune 切到 cut（rune 索引）。
		rs := []rune(c)
		if cut > len(rs) {
			cut = len(rs)
		}
		nm := copyMsg(msg)
		nm["content"] = string(rs[:cut]) + truncationMarker
		out[i] = nm
	}
	return out, applied
}

// backOffToWordBoundary 把硬切点调到最近的词边界。
// 翻译 lite.ts:136-147，按 rune 索引。
func backOffToWordBoundary(content string, cutIndex int) int {
	rs := []rune(content)
	if cutIndex < 0 {
		return 0
	}
	if cutIndex > len(rs) {
		cutIndex = len(rs)
	}
	onBoundary := cutIndex == 0 || cutIndex == len(rs) ||
		!isWordChar(rs[cutIndex-1]) || !isWordChar(rs[cutIndex])
	if onBoundary {
		return cutIndex
	}
	if b := findWhitespaceBackward(rs, cutIndex); b != -1 {
		return b
	}
	if f := findWhitespaceForward(rs, cutIndex); f != -1 {
		return f
	}
	return cutIndex
}

func findWhitespaceBackward(rs []rune, cutIndex int) int {
	windowStart := cutIndex - toolTruncationLookback
	if windowStart < 0 {
		windowStart = 0
	}
	for i := cutIndex; i > windowStart; i-- {
		if !isWordChar(rs[i-1]) {
			return i - 1
		}
	}
	return -1
}

func findWhitespaceForward(rs []rune, cutIndex int) int {
	windowEnd := cutIndex + toolTruncationLookback
	if windowEnd > len(rs) {
		windowEnd = len(rs)
	}
	for i := cutIndex; i < windowEnd; i++ {
		if !isWordChar(rs[i]) {
			return i
		}
	}
	return -1
}

// ---- Stage 4: redundant-remove (lite.ts:169-198) ----

func removeRedundantContent(msgs []any, opts Options) ([]any, bool) {
	applied := false
	out := make([]any, 0, len(msgs))
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		if opts.PreserveSystemPrompt && msgRole(msg) == "system" {
			out = append(out, m)
			continue
		}
		curStr, _ := contentString(msg["content"])
		if i > 0 {
			prev, pok := msgs[i-1].(map[string]any)
			if pok && msgRole(prev) == msgRole(msg) {
				prevC, prevIsStr := prev["content"].(string)
				if prevIsStr && prevC == curStr {
					applied = true
					continue
				}
			}
		}
		out = append(out, m)
	}
	return out, applied
}

// ---- Stage 5: image-placeholder (lite.ts:200-238) ----

func replaceImageUrls(msgs []any, opts Options) ([]any, bool) {
	// supportsVision !== false 才短路。nil/true 都不运行。
	if opts.SupportsVision == nil || *opts.SupportsVision {
		return msgs, false
	}
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			out[i] = m
			continue
		}
		newContent := make([]any, len(content))
		changed := false
		for j, part := range content {
			p, pok := part.(map[string]any)
			if !pok || p["type"] != "image_url" {
				newContent[j] = part
				continue
			}
			imgURL, uok := p["image_url"].(map[string]any)
			if !uok {
				newContent[j] = part
				continue
			}
			url, ustr := imgURL["url"].(string)
			if !ustr || !strings.HasPrefix(url, "data:image/") {
				newContent[j] = part
				continue
			}
			applied = true
			changed = true
			newContent[j] = map[string]any{
				"type": "text",
				"text": "[image: " + extractImageFormat(url) + "]",
			}
		}
		if changed {
			nm := copyMsg(msg)
			nm["content"] = newContent
			out[i] = nm
		} else {
			out[i] = m
		}
	}
	return out, applied
}

// extractImageFormat 从 "data:image/<fmt>;..." 抽 <fmt>。对齐 lite.ts:229。
func extractImageFormat(dataURL string) string {
	slash := strings.Index(dataURL, "/")
	if slash < 0 {
		return "unknown"
	}
	rest := dataURL[slash+1:]
	semi := strings.Index(rest, ";")
	if semi < 0 {
		return rest
	}
	if rest[:semi] == "" {
		return "unknown"
	}
	return rest[:semi]
}

// copyMsg 浅拷贝消息 map，避免改动原消息（stage 间不可变）。
func copyMsg(m map[string]any) map[string]any {
	nm := make(map[string]any, len(m))
	for k, v := range m {
		nm[k] = v
	}
	return nm
}
