package executors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var globalHotCfg atomic.Pointer[hotconfig.Config]

func SetHotConfig(cfg *hotconfig.Config) {
	globalHotCfg.Store(cfg)
}

func LoadHotConfig() *hotconfig.Config {
	return globalHotCfg.Load()
}

type NodeFailoverConfig struct {
	NodeTimeoutSeconds          int
	RetryCount                  int
	SingleNodeRetryDelaySeconds int
}

type NodeJumpEvent struct {
	Type         string `json:"type"`
	FromCredID   int    `json:"from_credential_id"`
	FromProvider int    `json:"from_provider_id"`
	ToCredID     int    `json:"to_credential_id"`
	ToProvider   int    `json:"to_provider_id"`
	Reason       string `json:"reason"`
	Attempt      int    `json:"attempt"`
}

type NodeTracker struct {
	triedCredentials map[int]bool
	triedProviders   map[int]bool
	attempts         int
	startTime        time.Time
	maxRetries       int
	hotCfg           *hotconfig.Config
}

func NewNodeTracker(hotCfg *hotconfig.Config) *NodeTracker {
	cfg := DefaultNodeFailoverConfig()
	if hotCfg != nil {
		cfg = LoadNodeFailoverConfig(hotCfg)
	}
	return &NodeTracker{
		triedCredentials: make(map[int]bool),
		triedProviders:   make(map[int]bool),
		startTime:        time.Now(),
		maxRetries:       cfg.RetryCount,
		hotCfg:           hotCfg,
	}
}

func (nt *NodeTracker) Record(cand provider.Candidate) {
	nt.triedCredentials[cand.CredentialID] = true
	nt.triedProviders[cand.ProviderID] = true
	nt.attempts++
}

func (nt *NodeTracker) HasTriedCredential(cand provider.Candidate) bool {
	return nt.triedCredentials[cand.CredentialID]
}

func (nt *NodeTracker) ShouldFailover(currentCredID int) bool {
	return nt.triedCredentials[currentCredID]
}

func (nt *NodeTracker) Attempts() int {
	return nt.attempts
}

func (nt *NodeTracker) IsSingleNode(candidates []provider.Candidate) bool {
	uniqueCreds := make(map[int]bool)
	for _, c := range candidates {
		uniqueCreds[c.CredentialID] = true
	}
	return len(uniqueCreds) <= 1
}

func (nt *NodeTracker) SingleNodeRetryDelay() time.Duration {
	cfg := DefaultNodeFailoverConfig()
	if nt.hotCfg != nil {
		cfg = LoadNodeFailoverConfig(nt.hotCfg)
	}
	if cfg.SingleNodeRetryDelaySeconds < 5 {
		return 10 * time.Second
	}
	return time.Duration(cfg.SingleNodeRetryDelaySeconds) * time.Second
}

func (nt *NodeTracker) MaxRetries() int {
	cfg := DefaultNodeFailoverConfig()
	if nt.hotCfg != nil {
		cfg = LoadNodeFailoverConfig(nt.hotCfg)
	}
	return cfg.RetryCount
}

func LoadNodeFailoverConfig(hotCfg *hotconfig.Config) NodeFailoverConfig {
	if hotCfg == nil {
		return DefaultNodeFailoverConfig()
	}
	return NodeFailoverConfig{
		// llmgw_node_timeout_seconds: Max execution time per node (default 120s, range 10-600s).
		// Aligned with FirstByteTimeout (120s) so upstream read deadline
		// doesn't cancel the request before first byte can arrive.
		// Increase if "upstream first-byte timeout" occurs frequently for long-running requests.
		// Note: This is per-node timeout; total retry time = NodeTimeout * RetryCount.
		NodeTimeoutSeconds:          clampInt(hotCfg.GetInt("llmgw_node_timeout_seconds", 120), 10, 600),
		RetryCount:                  clampInt(hotCfg.GetInt("llmgw_retry_count", 2), 0, 5),
		SingleNodeRetryDelaySeconds: clampInt(hotCfg.GetInt("llmgw_single_node_retry_delay_seconds", 10), 5, 60),
	}
}

func DefaultNodeFailoverConfig() NodeFailoverConfig {
	return NodeFailoverConfig{
		// 2026-07-23: 30→60→120s (two-step), aligned with FirstByteTimeout.
		// Long-thinking Claude/NVIDIA models exceed 60s before first byte.
		NodeTimeoutSeconds:          120,
		RetryCount:                  2,
		SingleNodeRetryDelaySeconds: 10,
	}
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
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

func SendNodeJumpEvent(w http.ResponseWriter, fromCred, fromProv, toCred, toProv int, reason string, attempt int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	event := NodeJumpEvent{
		Type:         "node_jump",
		FromCredID:   fromCred,
		FromProvider: fromProv,
		ToCredID:     toCred,
		ToProvider:   toProv,
		Reason:       reason,
		Attempt:      attempt,
	}

	data, err := json.Marshal(event)
	if err != nil {
		slog.Warn("failed to marshal node_jump event", "error", err)
		return
	}

	fmt.Fprintf(w, "event: node_jump\ndata: %s\n\n", data)
	flusher.Flush()

	slog.Info("sent node_jump SSE event",
		"from_credential", fromCred,
		"from_provider", fromProv,
		"to_credential", toCred,
		"to_provider", toProv,
		"reason", reason,
		"attempt", attempt,
	)
}

func SendRetrySSE(w http.ResponseWriter, credID, provID int, attempt int, delayMs int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	payload := map[string]interface{}{
		"type":          "node_retry",
		"credential_id": credID,
		"provider_id":   provID,
		"attempt":       attempt,
		"delay_ms":      delayMs,
	}

	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: node_retry\ndata: %s\n\n", data)
	flusher.Flush()
}

func NodeTimeout(hotCfg *hotconfig.Config) time.Duration {
	cfg := DefaultNodeFailoverConfig()
	if hotCfg != nil {
		cfg = LoadNodeFailoverConfig(hotCfg)
	}
	return time.Duration(cfg.NodeTimeoutSeconds) * time.Second
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
