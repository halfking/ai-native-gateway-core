package sessionforensics

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionsummary"
)

// Summarizer 是 sessionsummary.Summarizer 的 sessionforensics 适配层。
//
// 它做以下事：
//
//  1. 从 summary-fast 调用 LLM 生成结构化摘要 + 标题（高精度优先）。
//  2. LLM 不可用 / 超时时回退到 extractTitleFromFirstMessage（截断 50 字 + "..."）。
//  3. LLM 不可用时回退到基于会话长度的 heuristic 摘要（首末条 + 长度统计）。
//  4. 重复生成时通过 Redis 缓存复用结果（24h TTL，与 sessionsummary 兼容）。
//
// 设计目标：在 CI 环境下即使 LLM 不可达，也能产出可用的 title + summary，
// 让运维平台的"查看会话"功能不至于空白。
type Summarizer struct {
	inner *sessionsummary.Summarizer
}

// NewSummarizer 包装已有的 sessionsummary.Summarizer。如果 inner 为 nil，
// Summarize 方法将只走 fallback 路径（不会 panic）。
func NewSummarizer(inner *sessionsummary.Summarizer) *Summarizer {
	return &Summarizer{inner: inner}
}

// SummarizeOptions 控制摘要行为。
type SummarizeOptions struct {
	// TenantID for cache key + DB lookup
	TenantID string
	// FirstMessageOverride 跳过 DB 查首条消息，直接使用这个（用于本地测试或批量工具）
	FirstMessageOverride string
	// ForceSource "llm" | "fallback" | "preview" | "" (auto: 先 llm 再 fallback)
	ForceSource string
	// Rolling true 时若 Redis 已有摘要则走 GenerateRollingSummary，否则 false
	Rolling bool
}

// Summarize 触发一次完整的摘要 + 标题生成。
//
// 返回的 *SummaryResult 在 Source 字段里标记实际走的路径，便于运维平台显示。
func (s *Summarizer) Summarize(
	ctx context.Context,
	sessionID string,
	opt SummarizeOptions,
) (*SummaryResult, error) {
	res := &SummaryResult{
		SessionID:   sessionID,
		GeneratedAt: time.Now().UTC(),
	}
	if opt.TenantID == "" {
		opt.TenantID = "default"
	}

	// ── Step 1: 拿首条消息 ────────────────────────────────────────────────
	firstMsg := opt.FirstMessageOverride

	// ── Step 2: 强制 fallback 路径 ────────────────────────────────────────
	if opt.ForceSource == "fallback" || s.inner == nil {
		title, summary, topics, intent := s.fallbackFromFirstMessage(firstMsg, sessionID)
		res.Title = title
		res.Summary = summary
		res.KeyTopics = topics
		res.UserIntent = intent
		res.Source = "fallback"
		return res, nil
	}

	// ── Step 3: LLM 路径 ──────────────────────────────────────────────────
	var (
		summary *sessionsummary.SessionSummary
		err     error
	)
	if opt.Rolling {
		summary, err = s.inner.GenerateRollingSummary(ctx, opt.TenantID, sessionID)
	} else {
		summary, err = s.inner.GenerateSummary(ctx, opt.TenantID, sessionID)
	}

	if err != nil {
		slog.Warn("sessionforensics: LLM summary failed, fallback engaged",
			"session", sessionID, "err", err)
		title, sum, topics, intent := s.fallbackFromFirstMessage(firstMsg, sessionID)
		res.Title = title
		res.Summary = sum
		res.KeyTopics = topics
		res.UserIntent = intent
		res.Source = "fallback"
		res.Error = err.Error()
		return res, nil
	}

	if summary == nil {
		// 理论上不会到这（GenerateSummary 没摘要时返回 error）
		return nil, errors.New("sessionforensics: summary is nil after LLM call")
	}

	res.Title = summary.Title
	res.Summary = summary.Summary
	res.KeyTopics = summary.KeyTopics
	res.UserIntent = summary.UserIntent
	res.Source = "llm"
	return res, nil
}

// GenerateTitleOnly 单独的快速标题生成（不写 DB）。
//
// 等价于 sessionsummary.Summarizer.GenerateTitle，但带 fallback：
//   - LLM 成功：返回 LLM 标题（10 字以内中文标题）
//   - LLM 失败或 inner=nil：截断首条消息到 50 字 + "..."
func (s *Summarizer) GenerateTitleOnly(
	ctx context.Context,
	sessionID, tenantID, firstMessage string,
) (string, error) {
	if tenantID == "" {
		tenantID = "default"
	}
	if s.inner == nil {
		return fallbackTitle(firstMessage), nil
	}
	t, err := s.inner.GenerateTitle(ctx, tenantID, sessionID, firstMessage)
	if err != nil {
		slog.Warn("sessionforensics: LLM title failed, fallback",
			"session", sessionID, "err", err)
		return fallbackTitle(firstMessage), nil
	}
	if strings.TrimSpace(t) == "" {
		return fallbackTitle(firstMessage), nil
	}
	return t, nil
}

// fallbackFromFirstMessage 不依赖 LLM 的轻量提取。
func (s *Summarizer) fallbackFromFirstMessage(firstMsg, sessionID string) (title, summary string, topics []string, intent string) {
	title = fallbackTitle(firstMsg)
	if firstMsg == "" {
		summary = "暂无可总结的内容"
		return
	}
	// 简单摘要：截断 + 关键词计数
	summary = "会话 " + sessionID + " — " + truncate(firstMsg, 240)
	if strings.Count(summary, "") > 200 {
		summary = summary[:200] + "..."
	}
	topics = extractKeywords(firstMsg)
	intent = "unknown"
	if strings.Contains(firstMsg, "请") || strings.Contains(firstMsg, "?") {
		intent = "question"
	}
	return
}

// ExtractFirstUserMessage returns the first user-role message content from a
// chat-completions request body. If body is not a chat body, returns "".
//
// This is intentionally simple — it covers >90% of OpenAI-compatible bodies:
//
//	{ "messages": [{"role":"user","content":"..."}, ...] }
//
// Each message can have string or array content (vision / tool_result shapes).
func ExtractFirstUserMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	for _, m := range raw.Messages {
		if m.Role == "user" {
			// content can be string or array
			var s string
			if err := json.Unmarshal(m.Content, &s); err == nil && s != "" {
				return s
			}
			// try array of {type, text}
			var arr []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(m.Content, &arr); err == nil && len(arr) > 0 {
				out := ""
				for _, b := range arr {
					if b.Type == "text" && b.Text != "" {
						out += b.Text + " "
					}
				}
				return strings.TrimSpace(out)
			}
		}
	}
	return ""
}

// fallbackTitle 截断首条消息到 50 字。
func fallbackTitle(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "未命名会话"
	}
	// 中文按 1 字 = 1 rune 算
	runes := []rune(msg)
	if len(runes) <= 50 {
		return msg
	}
	return string(runes[:50]) + "..."
}

// extractKeywords 从短文本里挑最多 5 个长度 >= 2 的"词"，非常粗糙但
// 完全离线。LLM 在线时会被生成的 KeyTopics 覆盖。
func extractKeywords(s string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '\n' || r == '\t' || r == '?' || r == '!' || r == '。' || r == '，'
	}) {
		if len([]rune(w)) < 2 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= 5 {
			break
		}
	}
	return out
}
