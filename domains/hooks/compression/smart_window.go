// Package compressor - smart_window.go
//
// Smart sliding window analyzer: inspects the conversation structure and
// picks the optimal cut point that minimises information loss while
// satisfying the target token budget.
//
// Unlike the mechanical trim (transformation.CompressMessagesIfNeeded) which is
// a pure byte-size sliding window, the smart analyzer understands:
//
//  1. System messages are ALWAYS retained (they carry persona/instructions).
//  2. The first user message is ALWAYS retained (it carries task intent).
//  3. tool_use ↔ tool_result pairs are ATOMIC — never split them.
//  4. Assistant messages with tool_calls anchor the preceding context.
//  5. Recent turns carry more weight than old ones (recency bias).
//  6. Messages with code/error/URL content are weighted higher (information
//     density) than pleasantries.
//
// The output is a CutPlan that describes exactly which messages to
// summarise, which to keep verbatim, and where the boundary lies — so the
// caller can build [summary + retained tail] and persist the cut index for
// incremental compression on the next request.

package compression

import (
	"encoding/json"
	"math"
	"strings"
)

// CutPlan describes the result of smart window analysis.
type CutPlan struct {
	// CutIndex is the message index boundary: messages [0, CutIndex) in the
	// non-system portion will be summarised/dropped; messages [CutIndex, end)
	// are retained verbatim. -1 means no compression needed.
	CutIndex int `json:"cut_index"`

	// SystemCount is the number of leading system messages (always retained).
	SystemCount int `json:"system_count"`

	// FirstUserKept is true when the first non-system user message is in the
	// retained tail (not summarised). This preserves task intent.
	FirstUserKept bool `json:"first_user_kept"`

	// SummariseCount is how many messages will be collapsed into a summary.
	SummariseCount int `json:"summarise_count"`

	// RetainCount is how many messages are kept verbatim (excluding system).
	RetainCount int `json:"retain_count"`

	// EstTokensBefore is the estimated token count of all messages.
	EstTokensBefore int `json:"est_tokens_before"`

	// EstTokensAfter is the estimated token count after compression
	// (summary placeholder + retained tail).
	EstTokensAfter int `json:"est_tokens_after"`

	// Reason is a human-readable explanation of why this cut was chosen.
	Reason string `json:"reason"`
}

// MessageInfo is the per-message analysis result used by the analyzer.
type MessageInfo struct {
	Index    int    `json:"index"`
	Role     string `json:"role"`
	EstToken int    `json:"est_token"`
	// IsToolRoundMember is true if this message is part of a tool_use ↔
	// tool_result exchange (must not be split across the cut boundary).
	IsToolRoundMember bool `json:"is_tool_round_member"`
	// ToolRoundID groups tool-related messages that must stay together.
	// Messages sharing the same ToolRoundID must all be on the same side
	// of the cut. -1 = not part of a tool round.
	ToolRoundID int `json:"tool_round_id"`
	// InfoWeight is a 0.0–1.0 score: higher = more information-dense
	// (code, errors, URLs, numbers) and should preferentially be retained.
	InfoWeight float64 `json:"info_weight"`
}

// smartWindowSummaryPrefix is the prefix injected before LLM-generated
// summary text in the rebuilt body. Mirrors streaming.compactionSummaryPrefix.
const smartWindowSummaryPrefix = "[Gateway compacted conversation summary — prior turns collapsed to fit context]\n"

// smartWindowSummaryTokenBudget is the approximate tokens consumed by the
// injected summary message placeholder. This leaves room for the LLM-generated
// summary text itself.
const smartWindowSummaryTokenBudget = 2048

// minRetainedMessages is the minimum number of non-system messages to keep
// verbatim after compression. Even if the token budget allows fewer, we never
// drop below this to ensure the model has enough recent context.
const minRetainedMessages = 4

// AnalyzeConversation parses a message array and returns per-message
// metadata used by the smart window analyzer.
func AnalyzeConversation(messages []json.RawMessage) []MessageInfo {
	infos := make([]MessageInfo, 0, len(messages))
	toolRoundID := 0
	for i, raw := range messages {
		var probe struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  json.RawMessage `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
		}
		_ = json.Unmarshal(raw, &probe)

		text := rawJSONTextContent(probe.Content)
		mi := MessageInfo{
			Index:       i,
			Role:        probe.Role,
			EstToken:    estimateTextTokens(text),
			InfoWeight:  computeInfoWeight(text, probe.Role),
			ToolRoundID: -1,
		}

		// Detect tool round membership.
		hasToolCalls := len(probe.ToolCalls) > 0 && string(probe.ToolCalls) != "null"
		isToolResult := probe.Role == "tool" || probe.ToolCallID != ""
		hasAnthropicToolUse := detectToolUseInContent(probe.Content)
		hasAnthropicToolResult := detectToolResultInContent(probe.Content)

		if hasToolCalls || hasAnthropicToolUse {
			mi.IsToolRoundMember = true
			mi.ToolRoundID = toolRoundID
			toolRoundID++
		}
		if isToolResult || hasAnthropicToolResult {
			mi.IsToolRoundMember = true
			if toolRoundID > 0 {
				mi.ToolRoundID = toolRoundID - 1
			}
		}

		infos = append(infos, mi)
	}

	// Propagate tool round IDs backward: if a tool_result references an
	// earlier tool_use, ensure both share the same round ID (they must not
	// be split).
	for i := len(infos) - 1; i > 0; i-- {
		if infos[i].IsToolRoundMember && infos[i].ToolRoundID >= 0 {
			// Walk backward to find the matching tool_use and unify IDs.
			for j := i - 1; j >= 0; j-- {
				if infos[j].IsToolRoundMember && infos[j].ToolRoundID == infos[i].ToolRoundID {
					break
				}
				if infos[j].Role == "assistant" && (detectToolCalls(infos, j) || infos[j].ToolRoundID >= 0) {
					infos[j].ToolRoundID = infos[i].ToolRoundID
					infos[j].IsToolRoundMember = true
					break
				}
			}
		}
	}

	return infos
}

// FindOptimalCutPoint analyzes messages and returns a CutPlan that describes
// the best compression strategy for the given token budget.
//
// Parameters:
//   - messages: the full message array (system + user + assistant + tool).
//   - contextWindow: the target model's context window in tokens.
//   - targetUtilization: the fraction of contextWindow to aim for (e.g. 0.7).
//     The compressed result should fit within contextWindow × targetUtilization.
func FindOptimalCutPoint(
	messages []json.RawMessage,
	contextWindow int,
	targetUtilization float64,
) CutPlan {
	infos := AnalyzeConversation(messages)

	// Calculate total tokens.
	totalTokens := 0
	for _, mi := range infos {
		totalTokens += mi.EstToken
	}

	// Determine system message count.
	systemCount := 0
	for _, mi := range infos {
		if mi.Role == "system" {
			systemCount++
		} else {
			break
		}
	}

	// Non-system messages.
	nonSystem := infos[systemCount:]
	if len(nonSystem) == 0 {
		return CutPlan{
			CutIndex:        -1,
			SystemCount:     systemCount,
			SummariseCount:  0,
			RetainCount:     0,
			EstTokensBefore: totalTokens,
			EstTokensAfter:  totalTokens,
			Reason:          "no non-system messages to compress",
		}
	}

	// Token budget for retained tail.
	targetBudget := int(float64(contextWindow)*targetUtilization) - smartWindowSummaryTokenBudget
	if targetBudget < 0 {
		targetBudget = contextWindow - smartWindowSummaryTokenBudget
	}
	if targetBudget <= 0 {
		return CutPlan{
			CutIndex:        -1,
			SystemCount:     systemCount,
			SummariseCount:  len(nonSystem),
			RetainCount:     0,
			EstTokensBefore: totalTokens,
			EstTokensAfter:  totalTokens,
			Reason:          "context window too small for smart compression",
		}
	}

	// Walk from the tail backward, accumulating tokens until we hit the budget.
	// This finds the minimum number of messages to retain.
	retainFromIdx := len(nonSystem) // index into nonSystem; all retained
	accumulatedTokens := 0

	for i := len(nonSystem) - 1; i >= 0; i-- {
		t := nonSystem[i].EstToken
		if accumulatedTokens+t > targetBudget {
			break
		}
		accumulatedTokens += t
		retainFromIdx = i
	}

	// Enforce minimum retained messages.
	if len(nonSystem)-retainFromIdx < minRetainedMessages {
		retainFromIdx = len(nonSystem) - minRetainedMessages
		if retainFromIdx < 0 {
			retainFromIdx = 0
		}
	}

	// If retaining everything, no compression needed.
	if retainFromIdx <= 0 {
		return CutPlan{
			CutIndex:        -1,
			SystemCount:     systemCount,
			SummariseCount:  0,
			RetainCount:     len(nonSystem),
			EstTokensBefore: totalTokens,
			EstTokensAfter:  totalTokens,
			Reason:          "all messages fit within budget",
		}
	}

	// Ensure tool round integrity: if the cut boundary splits a tool round,
	// move the boundary backward to include the entire round in the retained tail.
	retainFromIdx = adjustForToolIntegrity(nonSystem, retainFromIdx)

	// Ensure the first user message is retained (task intent preservation).
	firstUserIdx := -1
	for i, mi := range nonSystem {
		if mi.Role == "user" {
			firstUserIdx = i
			break
		}
	}
	firstUserKept := firstUserIdx < 0 || firstUserIdx >= retainFromIdx

	// Calculate final counts.
	summariseCount := retainFromIdx
	retainCount := len(nonSystem) - retainFromIdx

	// Estimate post-compression tokens.
	tokensAfter := smartWindowSummaryTokenBudget + accumulatedTokens

	reason := buildCutReason(retainFromIdx, summariseCount, retainCount, firstUserKept)

	return CutPlan{
		CutIndex:        retainFromIdx,
		SystemCount:     systemCount,
		FirstUserKept:   firstUserKept,
		SummariseCount:  summariseCount,
		RetainCount:     retainCount,
		EstTokensBefore: totalTokens,
		EstTokensAfter:  tokensAfter,
		Reason:          reason,
	}
}

// SmartCompress applies a CutPlan to a message body and returns the rebuilt
// body with [system + summary_placeholder + retained_tail].
//
// The summaryPlaceholder is injected as a user message so the model knows prior
// context was collapsed. The actual LLM-generated summary text should be
// spliced in by the caller (recovery_coordinator.go) after calling this.
func SmartCompress(body []byte, plan CutPlan, protocol string, summaryText string) ([]byte, error) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, err
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	systemMsgs := req.Messages[:plan.SystemCount]
	nonSystem := req.Messages[plan.SystemCount:]

	// Build the retained tail.
	var tail []json.RawMessage
	if plan.CutIndex >= 0 && plan.CutIndex < len(nonSystem) {
		tail = nonSystem[plan.CutIndex:]
	} else {
		tail = nonSystem
	}

	// Build summary message.
	summaryContent := smartWindowSummaryPrefix + summaryText
	var summaryMsg json.RawMessage
	if protocol == "anthropic-messages" {
		summaryMsg, _ = json.Marshal(map[string]string{
			"role":    "user",
			"content": summaryContent,
		})
	} else {
		summaryMsg, _ = json.Marshal(map[string]string{
			"role":    "user",
			"content": summaryContent,
		})
	}

	// Assemble: [system...] + [summary] + [tail...]
	out := make([]json.RawMessage, 0, len(systemMsgs)+1+len(tail))
	out = append(out, systemMsgs...)
	out = append(out, summaryMsg)
	out = append(out, tail...)

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	generic["messages"] = raw
	return json.Marshal(generic)
}

// adjustForToolIntegrity moves the cut boundary backward if it would split
// a tool_use ↔ tool_result pair. The boundary must always fall between
// complete tool rounds.
func adjustForToolIntegrity(infos []MessageInfo, cutIdx int) int {
	if cutIdx <= 0 || cutIdx >= len(infos) {
		return cutIdx
	}
	// Check if the message just before the cut is part of a tool round.
	for cutIdx > 0 {
		msgBefore := infos[cutIdx-1]
		msgAfter := infos[cutIdx]
		// If either side of the boundary is a tool round member and the
		// other side has a matching round ID, extend backward.
		if msgBefore.IsToolRoundMember && msgAfter.IsToolRoundMember {
			if msgBefore.ToolRoundID == msgAfter.ToolRoundID {
				cutIdx--
				continue
			}
		}
		// tool_result without a preceding tool_use in the tail → extend.
		if msgAfter.Role == "tool" || msgAfter.ToolRoundID >= 0 {
			cutIdx--
			continue
		}
		break
	}
	return cutIdx
}

// computeInfoWeight scores a message's information density on a 0.0–1.0 scale.
// Higher scores indicate content that is more critical to retain verbatim.
func computeInfoWeight(text, role string) float64 {
	if text == "" {
		return 0.1
	}
	score := 0.3 // baseline

	// Code blocks / inline code.
	if strings.Contains(text, "```") || strings.Contains(text, "`") {
		score += 0.2
	}
	// Error messages / stack traces.
	lower := strings.ToLower(text)
	if strings.Contains(lower, "error") || strings.Contains(lower, "exception") ||
		strings.Contains(lower, "traceback") || strings.Contains(lower, "failed") {
		score += 0.15
	}
	// URLs / file paths.
	if strings.Contains(text, "http") || strings.Contains(text, "/") ||
		strings.Contains(text, ".py") || strings.Contains(text, ".go") ||
		strings.Contains(text, ".ts") || strings.Contains(text, ".js") {
		score += 0.1
	}
	// Numbers / IDs (configuration values, API keys, etc.).
	if hasDigit(text) {
		score += 0.05
	}
	// Assistant messages with substantive content.
	if role == "assistant" && len(text) > 200 {
		score += 0.1
	}
	// User messages with clear intent (questions, instructions).
	if role == "user" && (strings.Contains(text, "?") || strings.Contains(lower, "please") ||
		strings.Contains(lower, "help") || strings.Contains(lower, "需要") || strings.Contains(lower, "请")) {
		score += 0.1
	}

	return math.Min(score, 1.0)
}

func hasDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// estimateTextTokens provides a rough char→token estimate.
// Uses the same 3.5 chars/token heuristic as the rest of the codebase.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return len(text) * 10 / 35
}

func detectToolCalls(infos []MessageInfo, idx int) bool {
	if idx < 0 || idx >= len(infos) {
		return false
	}
	return infos[idx].IsToolRoundMember
}

func detectToolUseInContent(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_use" {
			return true
		}
	}
	return false
}

func detectToolResultInContent(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "tool_result" {
			return true
		}
	}
	return false
}

func buildCutReason(cutIdx, summariseCount, retainCount int, firstUserKept bool) string {
	parts := []string{
		"smart_window_cut",
	}
	if summariseCount > 0 {
		parts = append(parts, "summarise="+itoa(summariseCount))
	}
	if retainCount > 0 {
		parts = append(parts, "retain="+itoa(retainCount))
	}
	if !firstUserKept {
		parts = append(parts, "first_user_in_summary")
	} else {
		parts = append(parts, "first_user_retained")
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// (All shared helpers — rawMsg, extractMessages, min512, spliceBodyMessages,
// estimateBodyTokens — are defined in diff.go / estimator.go and reused.)
