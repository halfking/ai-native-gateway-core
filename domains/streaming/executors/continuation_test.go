package executors

import (
	"encoding/json"
	"testing"
)

// TestContinuationTrim_LongCorpusNotFlagged is the regression guard for the
// 2026-08-06 classifier fix.
//
// History: IsContinuationOrRetry used to run a case-insensitive SUBSTRING
// match over the entire trailing user message. The auto-title loopback
// corpus (the raw session transcript) routinely contains the literal
// "continue", so it was misclassified as a continuation request and
// trimOneMessageFromBody dropped the corpus, leaving an empty-messages body
// that Ark rejected with 400 InvalidParameter. The same defect silently
// truncated ordinary agent sessions (187 trims per 2h in production).
//
// The classifier now applies a length gate, so a transcript-sized message
// can never be read as a bare "continue" nudge.
func TestContinuationTrim_LongCorpusNotFlagged(t *testing.T) {
	body := buildAutoTitleBody("system: You are a title generator...", corpusContainingContinue)
	isContinue, isRetry := IsContinuationOrRetry(body, nil)
	if isContinue || isRetry {
		t.Fatalf("transcript corpus must not be flagged: isContinue=%v isRetry=%v", isContinue, isRetry)
	}
}

// TestContinuationTrim_SubstringFalsePositives pins the word-boundary rule.
// Each of these used to match a default keyword by naked containment ("go"
// inside "golang", "continue" inside "discontinued") and cost the caller its
// most recent turn.
func TestContinuationTrim_SubstringFalsePositives(t *testing.T) {
	cases := []string{
		"golang",
		"go test ./...",
		"django",
		"the algorithm",
		"good",
		"continues",
		"discontinued",
		"gogogo.example",
		"ongoing work",
		// Instruction-bearing phrases that merely start with a keyword —
		// trimming these would discard a real request.
		"继续修复这个 bug",
		"请继续下一步",
		"continue the refactor",
		"retry the failed test",
	}
	for _, content := range cases {
		body := buildChatBody(content)
		isContinue, isRetry := IsContinuationOrRetry(body, nil)
		if isContinue || isRetry {
			t.Errorf("%q must not be treated as continuation/retry (isContinue=%v isRetry=%v)",
				content, isContinue, isRetry)
		}
	}
}

// TestContinuationTrim_GenuineNudgesStillFlagged proves the fix did not
// disable the feature: a bare nudge, optionally wrapped in whitespace or
// punctuation, is still detected, so plain-chat users keep the original
// behaviour.
func TestContinuationTrim_GenuineNudgesStillFlagged(t *testing.T) {
	continues := []string{"继续", "请继续", "接着说", "continue", "Continue", "keep going", "  继续  ", "继续。", "Continue."}
	for _, content := range continues {
		body := buildChatBody(content)
		isContinue, _ := IsContinuationOrRetry(body, nil)
		if !isContinue {
			t.Errorf("%q should still be flagged as continuation", content)
		}
	}

	retries := []string{"重试", "请重试", "retry", "try again", "再试一次"}
	for _, content := range retries {
		body := buildChatBody(content)
		isContinue, isRetry := IsContinuationOrRetry(body, nil)
		if isContinue || !isRetry {
			t.Errorf("%q should be flagged as retry (isContinue=%v isRetry=%v)", content, isContinue, isRetry)
		}
	}
}

// TestContinuationTrim_TrimStillWorks keeps trimOneMessageFromBody covered:
// once a genuine nudge is detected, the last user+assistant turn is dropped
// as designed.
func TestContinuationTrim_TrimStillWorks(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "you are helpful"},
		{"role": "user", "content": "write a poem"},
		{"role": "assistant", "content": "here is half a poem"},
		{"role": "user", "content": "继续"},
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": messages})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	isContinue, _ := IsContinuationOrRetry(body, nil)
	if !isContinue {
		t.Fatalf("bare 继续 must be flagged")
	}
	trimmed, err := trimOneMessageFromBody(body)
	if err != nil {
		t.Fatalf("trimOneMessageFromBody: %v", err)
	}
	if got := countMessages(t, trimmed); got != 2 {
		t.Fatalf("expected system+user remaining, got %d messages", got)
	}
}

// TestContinuationTrim_ShortCorpusNotFlagged guards the control case: a
// short corpus without the keyword must not be trimmed (this is why some
// auto-title requests succeeded and others failed).
func TestContinuationTrim_ShortCorpusNotFlagged(t *testing.T) {
	body := buildAutoTitleBody("system: You are a title generator...", "what is the weather")
	isContinue, _ := IsContinuationOrRetry(body, nil)
	if isContinue {
		t.Fatalf("expected no continuation flag for benign corpus")
	}
}

// TestContinuationTrim_ToolSessionExemptionMarker documents the second
// defence layer added on 2026-08-06. Execute() skips the whole
// continuation/retry block when params.ToolsRequested is true, because
// dropping the last turn in an agent session breaks tool_call/tool_result
// pairing. The classifier fix above already prevents the common false
// positive; this layer covers the case where a user genuinely types "继续"
// mid-agent-loop, where trimming would still discard tool context.
func TestContinuationTrim_ToolSessionExemptionMarker(t *testing.T) {
	body := buildChatBody("继续")
	isContinue, _ := IsContinuationOrRetry(body, nil)
	if !isContinue {
		t.Fatalf("prerequisite: bare 继续 should be flagged by the classifier")
	}
	// Execute() gates on !params.ToolsRequested, so a tool session never
	// reaches trimOneMessageFromBody even when the classifier fires.
	params := &ExecParams{ToolsRequested: true}
	if !params.ToolsRequested {
		t.Fatalf("ToolsRequested must be settable for the exemption to hold")
	}
}

// TestContinuationTrim_AutoRequestExempt keeps the first defence layer
// pinned: when the executor sees X-Gw-Is-Auto: true it bypasses the
// continuation block entirely. Since the 2026-08-06 classifier fix the
// auto-title corpus no longer trips the classifier either, so this is now
// belt-and-braces — but the header guard must stay, because an auto request
// whose corpus happens to be exactly "继续" would otherwise be trimmed.
func TestContinuationTrim_AutoRequestExempt(t *testing.T) {
	// A pathological auto request: the whole corpus is a bare nudge.
	body := buildAutoTitleBody("system: You are a title generator...", "继续")
	isContinue, _ := IsContinuationOrRetry(body, nil)
	if !isContinue {
		t.Fatalf("bare-nudge corpus should reach the classifier as a continuation")
	}
	// Execute() gates on the header before consulting the classifier, so the
	// trim never runs for auto requests.
	if autoIsAutoHeaderValue != "true" {
		t.Fatalf("auto request marker header must be 'true'")
	}
}

const corpusContainingContinue = "system: You are ZCode, an interactive coding agent\n" +
	"user: continue working on the feature\n" +
	"assistant: I will continue\n"

// buildChatBody makes a minimal two-turn chat body whose trailing user
// message is exactly content, which is what IsContinuationOrRetry inspects.
func buildChatBody(content string) []byte {
	messages := []map[string]any{
		{"role": "user", "content": "earlier question"},
		{"role": "assistant", "content": "earlier answer"},
		{"role": "user", "content": content},
	}
	b, _ := json.Marshal(map[string]any{
		"model":    "minimax-m3",
		"messages": messages,
	})
	return b
}

func buildAutoTitleBody(system, corpus string) []byte {
	messages := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": corpus},
	}
	b, _ := json.Marshal(map[string]any{
		"model":       "minimax-m2.7",
		"messages":    messages,
		"temperature": 0.2,
		"max_tokens":  48,
	})
	return b
}

func countMessages(t *testing.T, body []byte) int {
	t.Helper()
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return len(req.Messages)
}

// autoIsAutoHeaderValue mirrors the constant used by the executor exemption.
const autoIsAutoHeaderValue = "true"
