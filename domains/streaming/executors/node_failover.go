package executors

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
)

var globalHotCfg atomic.Pointer[hotconfig.Config]

func SetHotConfig(cfg *hotconfig.Config) {
	globalHotCfg.Store(cfg)
}

func LoadHotConfig() *hotconfig.Config {
	return globalHotCfg.Load()
}

func LoadRetryKeywords(hotCfg *hotconfig.Config) (continueKeywords, retryKeywords []string) {
	defaultContinue := []string{"继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说"}
	defaultRetry := []string{"重试", "retry", "请重试", "再试一次", "try again", "重新回答", "再来"}

	if hotCfg == nil {
		return defaultContinue, defaultRetry
	}

	ck := hotCfg.GetString("llmgw_continue_keywords", "")
	if ck == "" {
		continueKeywords = defaultContinue
	} else {
		if err := json.Unmarshal([]byte(ck), &continueKeywords); err != nil {
			slog.Warn("failed to parse llmgw_continue_keywords, using defaults", "error", err)
			continueKeywords = defaultContinue
		}
	}

	rk := hotCfg.GetString("llmgw_retry_keywords", "")
	if rk == "" {
		retryKeywords = defaultRetry
	} else {
		if err := json.Unmarshal([]byte(rk), &retryKeywords); err != nil {
			slog.Warn("failed to parse llmgw_retry_keywords, using defaults", "error", err)
			retryKeywords = defaultRetry
		}
	}

	return continueKeywords, retryKeywords
}

// NodeTimeout returns the max execution time per node. Default 180s
// (2026-08-15, aligned with FirstByteTimeout; must stay >= FirstByteTimeout
// or the per-node timer cuts the request before the first-byte deadline can
// fire — docs/会话优化v3/29 §A4). The old NodeFailoverConfig/NodeTracker
// machinery went with the legacy sync candidate loop (AUDIT_24H B2b).
func NodeTimeout(hotCfg *hotconfig.Config) time.Duration {
	if hotCfg == nil {
		return 180 * time.Second
	}
	v := hotCfg.GetInt("llmgw_node_timeout_seconds", 180)
	if v < 10 {
		v = 10
	} else if v > 600 {
		v = 600
	}
	return time.Duration(v) * time.Second
}

// continuationMaxRuneLen bounds how long the trailing user message may be
// before it stops being treated as a bare "continue" / "retry" nudge.
//
// 2026-08-06 incident: the previous implementation ran a case-insensitive
// SUBSTRING match over the whole trailing user message. In agent sessions
// that is catastrophic — the default keyword list contains "go" and
// "continue", so ordinary content matched constantly ("golang", "go test",
// "django", "algorithm", "good", a pasted `continue` statement, a log line
// containing "continue keyword detected", ...). Every hit dropped the last
// user+assistant turn from the upstream body (trimOneMessageFromBody), so
// the model silently lost the most recent exchange and answered from a
// truncated history. Production measured 187 trims in a 2-hour window.
//
// A genuine nudge is short ("继续", "请继续", "continue", "keep going").
// 32 runes leaves room for light punctuation and bare phrasing, while
// excluding real instructions ("继续修复这个 bug", "请继续下一步") that
// must not be dropped along with the previous turn.
const continuationMaxRuneLen = 32

func IsContinuationOrRetry(body []byte, hotCfg *hotconfig.Config) (isContinue bool, isRetry bool) {
	continueKw, retryKw := LoadRetryKeywords(hotCfg)

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false, false
	}

	var lastUserContent string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUserContent = req.Messages[i].Content
			break
		}
	}

	trimmed := strings.TrimSpace(lastUserContent)
	if trimmed == "" {
		return false, false
	}

	// Length gate: anything longer than a bare nudge is real content, never
	// a continuation marker. This alone kills the agent-session false
	// positives, because tool transcripts and code are always far longer.
	if len([]rune(trimmed)) > continuationMaxRuneLen {
		return false, false
	}

	// Exact match after normalisation. Substring containment — and even
	// word-boundary containment — is unsafe here: "go" is a default
	// keyword, so "go test ./..." tokenises "go" as a standalone word and
	// would still be trimmed. Only a message that is *nothing but* a nudge
	// may trigger the trim, because trimming discards both the nudge and
	// the previous assistant turn. "继续修复这个 bug" and "请继续下一步"
	// carry real instructions, so dropping them loses information.
	normalised := normaliseNudge(trimmed)
	if normalised == "" {
		return false, false
	}

	for _, kw := range continueKw {
		if normalised == normaliseNudge(kw) {
			return true, false
		}
	}

	for _, kw := range retryKw {
		if normalised == normaliseNudge(kw) {
			return false, true
		}
	}

	return false, false
}

// normaliseNudge lowercases and strips surrounding whitespace plus trailing
// or leading punctuation, so "Continue.", "继续。" and "  continue  " all
// reduce to the bare keyword. Interior content is preserved, which is what
// keeps "go test" distinct from "go".
func normaliseNudge(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), nudgeCutset)
}

// nudgeCutset is the punctuation allowed to surround a bare nudge.
const nudgeCutset = " \t\r\n.,!?;:~-—…。，！？；：、"
