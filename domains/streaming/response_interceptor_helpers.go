package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation
)

// MaxFollowUpDepth is the maximum recursion depth for follow-up requests.
// This prevents infinite loops where a follow-up triggers another handoff
// or goal continue, which would otherwise amplify cost/load indefinitely.
const MaxFollowUpDepth = 5

// MaxFollowUpsPerSession is the hard ceiling on total follow-up invocations
// for a single session, regardless of depth. Defense in depth against runaway
// cost amplification.
const MaxFollowUpsPerSession = 50

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
// Returns true if the new count is within MaxFollowUpsPerSession.
//
// Race-free: LoadOrStore guarantees the same *atomic.Int64 pointer for a
// given sessionID, and atomic.Int64.Add is a single atomic RMW.
func recordSessionFollowUp(sessionID string) bool {
	actual, _ := sessionFollowUpCounts.LoadOrStore(sessionID, new(atomic.Int64))
	counter := actual.(*atomic.Int64)
	return counter.Add(1) <= int64(MaxFollowUpsPerSession)
}

// cleanupSessionFollowUps removes the counter for a session, freeing memory.
// Called when a session ends to prevent unbounded map growth.
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
func (h *ChatHandler) injectFollowUpRequest(ctx context.Context, sessionID string, followUpBody []byte, action string) {
	if len(followUpBody) == 0 {
		return
	}

	// 1. Depth check: prevent recursive loops.
	depth := FollowUpDepthFromContext(ctx)
	if depth >= MaxFollowUpDepth {
		slog.Warn("follow_up_max_depth_exceeded",
			"session_id", sessionID,
			"depth", depth,
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

	// Light rate limit.
	time.Sleep(100 * time.Millisecond)

	// Create a synthetic HTTP request with incremented depth.
	childCtx := withFollowUpDepth(ctx, depth+1)
	req, err := http.NewRequestWithContext(childCtx, "POST", "/v1/chat/completions", bytes.NewReader(followUpBody))
	if err != nil {
		slog.Error("follow_up_request_create_failed", "error", err, "session_id", sessionID)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", sessionID)
	req.Header.Set("X-Gw-Follow-Up-Action", action)
	req.Header.Set("X-Gw-Follow-Up-Depth", "1")

	// Response recorder captures the result for logging.
	rr := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("follow_up_request_panic", "error", r, "session_id", sessionID)
		}
	}()

	h.ServeHTTP(rr, req)

	// Log outcome with status and body snippet (truncated to 256 bytes).
	if rr.Code >= 400 {
		bodySnippet := rr.Body.String()
		if len(bodySnippet) > 256 {
			bodySnippet = bodySnippet[:256] + "..."
		}
		slog.Warn("follow_up_request_failed",
			"session_id", sessionID,
			"action", action,
			"status_code", rr.Code,
			"body", bodySnippet,
		)
	} else {
		slog.Info("follow_up_request_completed",
			"session_id", sessionID,
			"action", action,
			"status_code", rr.Code,
		)
	}
}

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
