package context

import (
	"strings"
	"unicode/utf8"
)

// Window 按 token 预算裁剪上下文片段。
//
// 为什么要单独一层：记忆后端返回的是"按相似度排序的事实片段"，
// 直接全量塞进提示词会撑爆上下文窗口并稀释重点。提示词优化属于
// "上下文工程"环节 —— 必须显式有预算，否则优化后的提示词反而更差。
//
// token 估算口径：英文约 4 字符/token，中文约 1.5 字符/token。
// 这里用保守的加权估算（中文按 1 字符 = 1 token 计），宁可低估可用空间，
// 也不要把窗口撑爆。
type Window struct {
	maxTokens int
	used      int
}

// NewWindow 构造 token 预算窗口。
func NewWindow(maxTokens int) *Window {
	if maxTokens <= 0 {
		maxTokens = DefaultConfig().MaxContextTokens
	}
	return &Window{maxTokens: maxTokens}
}

// Add 尝试加入一段文本。
//
// 返回 true 表示已加入；false 表示超出预算，调用方应停止继续加入。
// 空文本一律视为已加入（不消耗预算）且返回 true。
func (w *Window) Add(text string) bool {
	if text == "" {
		return true
	}
	cost := EstimateTokens(text)
	if w.used+cost > w.maxTokens {
		return false
	}
	w.used += cost
	return true
}

// Used 返回已使用的 token 数。
func (w *Window) Used() int { return w.used }

// Remaining 返回剩余预算。
func (w *Window) Remaining() int {
	if w.maxTokens <= 0 {
		return 0
	}
	return w.maxTokens - w.used
}

// MaxTokens 返回预算上限。
func (w *Window) MaxTokens() int { return w.maxTokens }

// EstimateTokens 估算文本的 token 数。
//
// 口径：ASCII 字符按 4 字符/token，多字节字符（中文等）按 1 字符/token。
// 这是保守估算 —— 真实 tokenizer 对中文通常约 1 字 = 1~1.5 token。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	nonASCII := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	tokens := ascii/4 + nonASCII
	if ascii%4 != 0 {
		tokens++
	}
	return tokens
}

// Truncate 按 token 预算截断文本，尾部补省略号标记。
func Truncate(text string, maxTokens int) string {
	if maxTokens <= 0 || text == "" {
		return ""
	}
	if EstimateTokens(text) <= maxTokens {
		return text
	}

	var sb strings.Builder
	used := 0
	for _, r := range text {
		cost := 1
		if r < utf8.RuneSelf {
			cost = 1
		}
		if used+cost > maxTokens-4 { // 预留 "...\n" 的预算
			break
		}
		sb.WriteRune(r)
		used += cost
	}
	return sb.String() + "...\n"
}
