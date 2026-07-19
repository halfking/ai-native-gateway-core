// Package routingtest provides a small OpenAI-compatible client for routing test runs.
package routingtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Message is an OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is an OpenAI-compatible chat completion request.
type Request struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// Result records one gateway request without assuming an upstream credential.
type Result struct {
	RequestID  string        `json:"request_id"`
	SessionID  string        `json:"session_id,omitempty"`
	Model      string        `json:"model"`
	Round      int           `json:"round"`
	Status     int           `json:"status"`
	Latency    time.Duration `json:"latency"`
	Pending    bool          `json:"pending"`
	RetryAfter string        `json:"retry_after,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// Client sends requests to the gateway.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient constructs a client with a bounded request timeout.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// Chat sends a request and treats a gateway 202 pending response as a recorded state,
// not a successful completion or a transport error.
func (c *Client) Chat(ctx context.Context, request Request, sessionID string, round int) Result {
	requestID := uuid.NewString()
	result := Result{
		RequestID: requestID,
		SessionID: sessionID,
		Model:     request.Model,
		Round:     round,
	}

	body, err := json.Marshal(request)
	if err != nil {
		result.Error = fmt.Sprintf("marshal request: %v", err)
		return result
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		result.Error = fmt.Sprintf("create request: %v", err)
		return result
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Request-Id", requestID)
	if sessionID != "" {
		httpRequest.Header.Set("X-Gw-Session-Id", sessionID)
	}

	started := time.Now()
	response, err := c.httpClient.Do(httpRequest)
	result.Latency = time.Since(started)
	if err != nil {
		result.Error = fmt.Sprintf("send request: %v", err)
		return result
	}
	defer response.Body.Close()

	result.Status = response.StatusCode
	result.RetryAfter = response.Header.Get("Retry-After")
	result.Pending = response.StatusCode == http.StatusAccepted && response.Header.Get("X-Gw-Pending") != ""

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		result.Error = fmt.Sprintf("read response: %v", readErr)
		return result
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Error = compactError(responseBody)
	}
	return result
}

func compactError(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		if payload.Error.Type != "" {
			return payload.Error.Type + ": " + payload.Error.Message
		}
		return payload.Error.Message
	}
	return strings.TrimSpace(string(body))
}
