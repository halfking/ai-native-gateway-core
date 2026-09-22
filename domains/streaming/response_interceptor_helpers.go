package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation
)

// MaxFollowUpDepth is the default maximum recursion depth for follow-up
// requests. This prevents infinite loops where a follow-up triggers another
// handoff or goal continue, which would otherwise amplify cost/load
// indefinitely. The effective value can be raised/lowered at runtime via
// SetFollowUpLimits (e.g. from goal.max_follow_up_depth).
//
// 15 accommodates the default goal loop budget (max_auto_continue_count=3 ×
// (max_model_switch_count+1=4) = 12, plus audit margin) without truncating
// budget-exhaustion model switching. A smaller value would silently cut off
// the goal continue chain before the continue budget is spent.
const MaxFollowUpDepth = 15

// MaxFollowUpsPerSession is the default hard ceiling on total follow-up
// invocations for a single session, regardless of depth. Defense in depth
// against runaway cost amplification. Overridable via SetFollowUpLimits.
const MaxFollowUpsPerSession = 50

// effectiveMaxFollowUpDepth / effectiveMaxFollowUpsPerSession hold the
// currently-active limits. They start at the constants above and are updated
// atomically by SetFollowUpLimits so hot-reloaded settings take effect without
// a restart. Reads use atomic load to stay lock-free on the hot path.
var (
	effectiveMaxFollowUpDepth       atomic.Int64
	effectiveMaxFollowUpsPerSession atomic.Int64
)

func init() {
	effectiveMaxFollowUpDepth.Store(MaxFollowUpDepth)
	effectiveMaxFollowUpsPerSession.Store(MaxFollowUpsPerSession)
}

// SetFollowUpLimits overrides the follow-up engine's hard limits. Pass 0 to
// keep the built-in default for that limit. Called by the goal control wiring
// (cmd/gateway/goal_control.go) from the goal.max_follow_up_depth /
// goal.max_follow_ups_per_session settings so operators can tune the loop
// guardrails without a redeploy.
func SetFollowUpLimits(maxDepth, maxPerSession int) {
	if maxDepth > 0 {
		effectiveMaxFollowUpDepth.Store(int64(maxDepth))
	}
	if maxPerSession > 0 {
		effectiveMaxFollowUpsPerSession.Store(int64(maxPerSession))
	}
}

// followUpDepthKey is the context key for follow-up depth tracking.
type followUpDepthKey struct{}

// withFollowUpDepth returns a child context with the depth counter.
func withFollowUpDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, followUpDepthKey{}, depth)
}

// FollowUpDepthFromContext returns the current follow-up depth (0 for new requests).
func FollowUpDepthFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(followUpDepthKey{}).(int); ok {
		return v
	}
	return 0
}

// sessionFollowUpCounts tracks per-session follow-up invocations.
// Stores *atomic.Int64 per sessionID so Add() is lock-free and race-free.
var sessionFollowUpCounts sync.Map // map[string]*atomic.Int64

// recordSessionFollowUp atomically increments the per-session counter.
// Returns true if the new count is within the effective per-session limit.
//
// Race-free: LoadOrStore guarantees the same *atomic.Int64 pointer for a
// given sessionID, and atomic.Int64.Add is a single atomic RMW.
func recordSessionFollowUp(sessionID string) bool {
	actual, _ := sessionFollowUpCounts.LoadOrStore(sessionID, new(atomic.Int64))
	counter := actual.(*atomic.Int64)
	limit := effectiveMaxFollowUpsPerSession.Load()
	return counter.Add(1) <= limit
}

// cleanupSessionFollowUps removes the counter for a session, freeing memory.
// Called when a session ends to prevent unbounded map growth.
//
//nolint:unused // Reserved for future session cleanup logic
func cleanupSessionFollowUps(sessionID string) {
	sessionFollowUpCounts.Delete(sessionID)
}

// injectFollowUpRequest asynchronously sends a follow-up request to the LLM.
// Used by response interceptors for automatic handoff and goal-mode continuation.
//
// SAFETY: This function is intentionally conservative. It enforces:
//  1. A maximum recursion depth (MaxFollowUpDepth) to prevent infinite loops
//  2. A per-session invocation ceiling (MaxFollowUpsPerSession)
//  3. Panic recovery so a single misbehaving follow-up doesn't kill the worker
//
// The 100ms sleep at the start is a cheap per-call rate limit.
func (h *ChatHandler) injectFollowUpRequest(ctx context.Context, sessionID string, followUpBody []byte, action string, parentRequestID string, parentAuthHeader string) {
	if len(followUpBody) == 0 {
		return
	}

	// 1. Depth check: prevent recursive loops.
	depth := FollowUpDepthFromContext(ctx)
	if depth >= int(effectiveMaxFollowUpDepth.Load()) {
		slog.Warn("follow_up_max_depth_exceeded",
			"session_id", sessionID,
			"depth", depth,
			"max_depth", effectiveMaxFollowUpDepth.Load(),
			"action", action,
		)
		return
	}

	// 2. Per-session invocation ceiling.
	if !recordSessionFollowUp(sessionID) {
		slog.Warn("follow_up_per_session_limit",
			"session_id", sessionID,
			"action", action,
		)
		return
	}

	slog.Info("injecting_follow_up_request",
		"session_id", sessionID,
		"action", action,
		"depth", depth,
		"body_size", len(followUpBody),
	)

	// 2026-07-11 handoff self-call fix: dispatch via the seam so tests can
	// stub it without spinning up the full ChatHandler pipeline.
	dispatch := h.dispatchFollowUpRequest
	if dispatch == nil {
		dispatch = defaultDispatchFollowUp
	}

	status, bodySnippet := dispatch(h, ctx, sessionID, followUpBody, action, parentRequestID, parentAuthHeader, 1)
	if status >= 400 {
		// Test stubs may return unbounded snippets; the production seam already
		// truncates via bodySnippetPrefix — re-apply the same rune-safe cut so
		// the log line stays bounded without slicing mid-rune.
		bodySnippet = bodySnippetPrefix(bodySnippet)
		slog.Warn("follow_up_request_failed",
			"session_id", sessionID,
			"action", action,
			"status_code", status,
			"body", bodySnippet,
		)
	} else {
		slog.Info("follow_up_request_completed",
			"session_id", sessionID,
			"action", action,
			"status_code", status,
		)
	}
}

// defaultDispatchFollowUp is the production dispatch seam: it builds a
// synthetic /v1/chat/completions request with the supplied Authorization
// header and loops it back through ChatHandler.ServeHTTP (the same path a
// normal client request hits, including auth + routing + upstream).
//
// The synthesized request carries the same correlation headers the auto-title
// loopback uses (X-Gw-Source-Actor / X-Gw-Parent-Request-Id), so the shadow
// turn is persisted with origin_actor='goal-*' and a joinable parent_request_id
// in request_logs and session_turns (方案：docs/03-design/02-feature-design/
// 会话优化v4/18-Goal影子指令与续跑优化方案.md §3). It is deliberately NOT
// marked X-Gw-Is-Auto: that header routes the entry into isInternalAutoEntry
// and would silently drop the shadow turn from the session mirror, breaking
// the GLOBAL_G2 reconciliation invariant. (Header entry is not the only
// IsAutoRequest source — a body model="auto" audit shadow turn still trips
// the TaskType fallback; see applyAutoRouteFields on the success path, which
// keeps business auto turns mirrored by carrying TaskType.)
//
// Wave 1 A5 (2026-09-22 设计 §5.12"影子指令不进入会话上下文")：影子轮按
// origin_actor 前缀 'goal-%' 被会话拼装型读者排除（sessionsummary 两个
// MessageSource），镜像/对账行本身保留——打标不排除、过滤权在查询侧
// （方案 18 §3 的既定语义，本波把它落到了装配查询上）。
//
// Returns (statusCode, bodySnippet). Body is truncated to 256 bytes so log
// lines stay bounded on bad upstream payloads.
func defaultDispatchFollowUp(
	h *ChatHandler,
	ctx context.Context,
	sessionID string,
	body []byte,
	action string,
	parentRequestID string,
	authHeader string,
	attempt int,
) (int, string) {
	time.Sleep(100 * time.Millisecond)

	childCtx := withFollowUpDepth(ctx, FollowUpDepthFromContext(ctx)+1)
	req, err := buildFollowUpRequest(childCtx, sessionID, body, action, parentRequestID, authHeader, attempt)
	if err != nil {
		slog.Error("follow_up_request_create_failed", "error", err, "session_id", sessionID)
		return 0, ""
	}

	rr := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("follow_up_request_panic", "error", r, "session_id", sessionID)
		}
	}()
	h.ServeHTTP(rr, req)

	snippet := rr.Body.String()
	return rr.Code, bodySnippetPrefix(snippet)
}

// buildFollowUpRequest constructs the synthetic follow-up request with its
// session/action/correlation/auth headers. Split out of
// defaultDispatchFollowUp so tests can assert the header contract without
// spinning up the full ServeHTTP pipeline.
//
// R37 (R35-R8): X-Gw-Follow-Up-Depth now carries the REAL follow-up depth
// (it was hardcoded "1" while the true depth lives in the context, 1..15);
// X-Gw-Follow-Up-Attempt was deleted — it was always "1" with zero readers
// anywhere in the repo (the `attempt` param stays only for dispatch-seam
// signature stability; the auth-retry machinery it belonged to was never
// wired and its residual helpers were removed in this round).
func buildFollowUpRequest(ctx context.Context, sessionID string, body []byte, action string, parentRequestID string, authHeader string, attempt int) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", sessionID)
	req.Header.Set("X-Gw-Follow-Up-Action", action)
	req.Header.Set("X-Gw-Follow-Up-Depth", strconv.Itoa(FollowUpDepthFromContext(ctx)))
	if actor := followUpSourceActor(action); actor != "" {
		req.Header.Set(autoSourceActorHeader, actor)
	}
	if parentRequestID != "" {
		req.Header.Set(autoParentRequestIDHeader, parentRequestID)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req, nil
}

// followUpSourceActor maps a follow-up action to the origin_actor value
// persisted on the shadow turn (request_logs.origin_actor and
// session_turns.origin_actor, via the X-Gw-Source-Actor header). Only
// goal-family actions get a goal-% actor: the audit family uses the same
// HasPrefix(action, "audit") semantics as the goal hook's own skip check
// (mode_hook decideAndContinue), covering both "audit" and "audit_auto_fix".
// Unknown actions return "" — the request then carries no X-Gw-Source-Actor
// header and the row keeps origin_actor NULL, the pre-2026-09-17 behavior.
// This matters for reconciliation: a future non-goal dispatch through this
// seam (e.g. a response-side handoff follow-up) must not be silently
// attributed to the goal shadow-turn budget (方案 18 §8 对账口径).
func followUpSourceActor(action string) string {
	a := strings.TrimSpace(action)
	switch {
	case a == "goal_continue":
		return "goal-continue"
	case a == "goal_model_switch":
		return "goal-model-switch"
	case strings.HasPrefix(a, "audit"):
		return "goal-audit"
	default:
		return ""
	}
}

// bodySnippetPrefix truncates to 256 bytes on a rune boundary for log lines.
func bodySnippetPrefix(s string) string {
	const max = 256
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}

// R37 (R35-R8): isFollowUpAuthFailure / followUpAuthCandidates / authKindLabel
// deleted — a multi-credential sequential auth-retry mechanism whose retry
// loop was never wired since the file's initial release (zero callers, none
// in tests). If auth-retry for follow-ups is ever designed, start from the
// dispatch seam (dispatchFollowUpRequest), not from resurrected fragments.

// extractMessageCount counts messages in a chat request body.
func extractMessageCount(body []byte) int {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	return len(req.Messages)
}

// extractTotalTokens extracts total token count from response or stream capture.
func extractTotalTokens(responseBody []byte, capture *audit.StreamCapture) int {
	if capture != nil {
		m := capture.SummaryAsMap()
		if total, ok := m["total_tokens"].(int); ok && total > 0 {
			return total
		}
		prompt, _ := m["prompt_tokens"].(int)
		completion, _ := m["completion_tokens"].(int)
		if sum := prompt + completion; sum > 0 {
			return sum
		}
	}
	var resp struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &resp); err == nil {
		return resp.Usage.TotalTokens
	}
	return 0
}

// extractFinishReason extracts the finish_reason from a response.
func extractFinishReason(body []byte) string {
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Choices) > 0 {
		return resp.Choices[0].FinishReason
	}
	return ""
}

// reassembleStreamBody rebuilds a minimal OpenAI-style chat completion
// response body from a stream capture so that stream-end response interceptors
// (goal completion detection, audit) can run the same JSON inspection logic as
// the non-streaming path.
//
// It embeds the accumulated assistant text (stream_text_content), the upstream
// finish_reason, and any structured tool_calls the capture observed. Returns
// nil when there is no capture or no content, leaving the caller to fall back
// to length-based continuation.
func reassembleStreamBody(capture *audit.StreamCapture) []byte {
	if capture == nil {
		return nil
	}
	m := capture.SummaryAsMap()
	text, _ := m["stream_text_content"].(string)
	finish, _ := m["upstream_finish_reason"].(string)
	tools := capture.ToolCalls
	if text == "" && len(tools) == 0 {
		return nil
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	if len(tools) > 0 {
		msg["tool_calls"] = tools
	}
	body := map[string]any{
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finish,
			},
		},
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return out
}

// reassembleFinishReason returns the upstream finish_reason recorded by the
// stream capture, or "" when no capture / no finish was observed.
func reassembleFinishReason(capture *audit.StreamCapture) string {
	if capture == nil {
		return ""
	}
	finish, _ := capture.SummaryAsMap()["upstream_finish_reason"].(string)
	return finish
}
