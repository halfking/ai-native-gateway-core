package executors

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
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
		// llmgw_node_timeout_seconds: Max execution time per node (default 180s, range 10-600s).
		// 2026-08-15: raised 120→180 to stay aligned with FirstByteTimeout (which
		// itself went 120→180 on 2026-08-04 for long-thinking models). The node
		// timeout must be >= FirstByteTimeout, otherwise the per-node timer cuts
		// the request before the first-byte deadline can fire (see
		// docs/会话优化v3/29 §A4).
		// Note: This is per-node timeout; total retry time = NodeTimeout * RetryCount.
		NodeTimeoutSeconds:          clampInt(hotCfg.GetInt("llmgw_node_timeout_seconds", 180), 10, 600),
		RetryCount:                  clampInt(hotCfg.GetInt("llmgw_retry_count", 2), 0, 5),
		SingleNodeRetryDelaySeconds: clampInt(hotCfg.GetInt("llmgw_single_node_retry_delay_seconds", 10), 5, 60),
	}
}

func DefaultNodeFailoverConfig() NodeFailoverConfig {
	return NodeFailoverConfig{
		// 2026-07-23: 30→60→120s (two-step), aligned with FirstByteTimeout.
		// 2026-08-15: 120→180s, aligned with the current FirstByteTimeout
		// (stream_runtime.go, 180s since 2026-08-04). Long-thinking
		// Claude/NVIDIA models exceed 60s before first byte.
		NodeTimeoutSeconds:          180,
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

// emitNodeSwitch (2026-08-15, V3.3-OBS OBS-B1) 发射 node_switch 动作事件
// （24 号 §2: from/to/reason/retry/retry_seq；retry=true 时前端打特别标）。
//
// 它取代了旧的 SendNodeJumpEvent / SendRetrySSE（docs/会话优化v3/25 §8 判定
// 为死代码，仓库内零调用点）：那两个函数假设"执行器手里有客户端的
// http.ResponseWriter"并直接向响应流写 node_jump/node_retry SSE 事件，但
// 节点切换的调用点在候选循环深处，拿到的 w 与真正的 SSE 出口
// （admin/live_stream_sse.go 单出口原则，ADR-V3-101）完全脱节，所以从未被
// 接线。动作事件走独立旁路（internal/liveactions → Redis 回放队列 → SSE
// request_lifecycle），不需要也不应该碰 ResponseWriter，因此删除死代码、
// 由本发射器直接复活其调用语义（from/to/reason/attempt）。
func (e *Executor) emitNodeSwitch(params *ExecParams, fromCred, toCred int, reason string, attempt int) {
	if e == nil || params == nil {
		return
	}
	ctx := context.Background()
	if params.R != nil {
		ctx = params.R.Context()
	}
	e.liveActions.Emit(ctx, liveactions.ActionEvent{
		RequestID:    params.RequestID,
		Action:       liveactions.ActionNodeSwitch,
		Model:        params.ClientModel,
		CredentialID: toCred,
		Retry:        fromCred == toCred,
		RetrySeq:     attempt,
		Detail: map[string]string{
			"from_credential_id": strconv.Itoa(fromCred),
			"to_credential_id":   strconv.Itoa(toCred),
			"reason":             reason,
		},
	})
}

// nextCandidateCredentialID returns the credential id of the candidate after
// idx. 0 when idx is the last — the switch target is then the sync-retry /
// model-fallback path rather than a sibling node (to_credential_id=0).
func nextCandidateCredentialID(candidates []provider.Candidate, idx int) int {
	if idx+1 < len(candidates) {
		return candidates[idx+1].CredentialID
	}
	return 0
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
