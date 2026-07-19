package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents the OpenAI-compatible chat request
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
}

// ChatResponse represents the OpenAI-compatible chat response
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ErrorResponse represents error response from gateway
type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Client is the HTTP client for LLM Gateway
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	metrics    MetricsCollector
}

// MetricsCollector is the interface for collecting metrics
type MetricsCollector interface {
	RecordLatency(operation string, duration time.Duration)
	RecordError(errorType string)
	RecordSuccess()
}

// NewClient creates a new gateway client
func NewClient(baseURL, apiKey string, metrics MetricsCollector) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		apiKey:  apiKey,
		metrics: metrics,
	}
}

// ChatCompletion sends a chat completion request
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordLatency("chat_completion", time.Since(start))
		}
	}()

	// Marshal request body
	body, err := json.Marshal(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("marshal_error")
		}
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("create_request_error")
		}
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("X-Request-Id", generateRequestID())

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("network_error")
		}
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("read_response_error")
		}
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			if c.metrics != nil {
				c.metrics.RecordError(fmt.Sprintf("http_%d", resp.StatusCode))
			}
			return nil, fmt.Errorf("gateway error (status=%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		if c.metrics != nil {
			c.metrics.RecordError(fmt.Sprintf("http_%d", resp.StatusCode))
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("parse_error")
		}
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if c.metrics != nil {
		c.metrics.RecordSuccess()
	}
	return &chatResp, nil
}

// ChatCompletionWithSession sends a chat completion request with session ID
func (c *Client) ChatCompletionWithSession(ctx context.Context, req ChatRequest, sessionID string) (*ChatResponse, error) {
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordLatency("chat_completion_session", time.Since(start))
		}
	}()

	// Marshal request body
	body, err := json.Marshal(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("marshal_error")
		}
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("create_request_error")
		}
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("X-Request-Id", generateRequestID())
	httpReq.Header.Set("X-Gw-Session-Id", sessionID)

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("network_error")
		}
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("read_response_error")
		}
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			if c.metrics != nil {
				c.metrics.RecordError(fmt.Sprintf("http_%d", resp.StatusCode))
			}
			return nil, fmt.Errorf("gateway error (status=%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		if c.metrics != nil {
			c.metrics.RecordError(fmt.Sprintf("http_%d", resp.StatusCode))
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		if c.metrics != nil {
			c.metrics.RecordError("parse_error")
		}
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if c.metrics != nil {
		c.metrics.RecordSuccess()
	}
	return &chatResp, nil
}

// Ping sends a minimal request to test connectivity
func (c *Client) Ping(ctx context.Context, model string) (time.Duration, error) {
	req := ChatRequest{
		Model:     model,
		Messages:  []Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	}

	start := time.Now()
	_, err := c.ChatCompletion(ctx, req)
	return time.Since(start), err
}

// generateRequestID generates a unique request ID
func generateRequestID() string {
	return uuid.New().String()
}
