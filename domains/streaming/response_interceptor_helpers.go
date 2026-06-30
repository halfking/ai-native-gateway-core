package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// injectFollowUpRequest asynchronously sends a follow-up request to the LLM.
// Used by response interceptors for automatic handoff and goal-mode continuation.
func (h *ChatHandler) injectFollowUpRequest(ctx context.Context, sessionID string, followUpBody []byte, action string) {
	if len(followUpBody) == 0 {
		return
	}

	slog.Info("injecting_follow_up_request",
		"session_id", sessionID,
		"action", action,
		"body_size", len(followUpBody),
	)

	// Rate limit: prevent infinite loops
	// TODO: Add proper rate limiting per session
	time.Sleep(100 * time.Millisecond)

	// Create a synthetic HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", "/v1/chat/completions", bytes.NewReader(followUpBody))
	if err != nil {
		slog.Error("follow_up_request_create_failed", "error", err, "session_id", sessionID)
		return
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", sessionID)
	req.Header.Set("X-Gw-Follow-Up-Action", action)

	// Use a response recorder to capture the response
	rr := httptest.NewRecorder()

	// Execute the request through the handler
	defer func() {
		if r := recover(); r != nil {
			slog.Error("follow_up_request_panic", "error", r, "session_id", sessionID)
		}
	}()

	h.ServeHTTP(rr, req)

	if rr.Code >= 400 {
		slog.Warn("follow_up_request_failed",
			"session_id", sessionID,
			"action", action,
			"status_code", rr.Code,
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
	// Try stream capture first (more accurate for streaming)
	if capture != nil {
		m := capture.SummaryAsMap()
		if total, ok := m["total_tokens"].(int); ok && total > 0 {
			return total
		}
		// Fallback: sum prompt + completion
		prompt, _ := m["prompt_tokens"].(int)
		completion, _ := m["completion_tokens"].(int)
		if sum := prompt + completion; sum > 0 {
			return sum
		}
	}

	// Try response body for non-streaming
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
