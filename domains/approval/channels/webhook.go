// Package channels provides notification channel implementations for approval workflows.
package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// WebhookChannel sends approval notifications via HTTP webhooks.
type WebhookChannel struct {
	url        string
	secret     string
	client     *http.Client
	maxRetries int
}

// NewWebhookChannel creates a new webhook notification channel.
func NewWebhookChannel(url, secret string) *WebhookChannel {
	return &WebhookChannel{
		url:        url,
		secret:     secret,
		maxRetries: 3,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     30 * time.Second,
				DisableCompression:  false,
				DisableKeepAlives:   false,
				MaxIdleConnsPerHost: 2,
			},
		},
	}
}

// WebhookPayload represents the payload sent to webhook endpoints.
type WebhookPayload struct {
	Event      string                  `json:"event"`
	Timestamp  int64                   `json:"timestamp"`
	Request    *approval.ApprovalRequest `json:"request"`
	Approvers  []approval.Approver     `json:"approvers"`
}

// SendApprovalNotification sends a webhook notification for an approval request.
func (c *WebhookChannel) SendApprovalNotification(ctx context.Context, req *approval.ApprovalRequest, approvers []approval.Approver) error {
	// Build payload
	payload := WebhookPayload{
		Event:     "approval.created",
		Timestamp: time.Now().Unix(),
		Request:   req,
		Approvers: approvers,
	}

	// Marshal to JSON
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Send with retry
	return c.sendWithRetry(ctx, payloadBytes)
}

// sendWithRetry sends the webhook request with exponential backoff retry.
func (c *WebhookChannel) sendWithRetry(ctx context.Context, payloadBytes []byte) error {
	var lastErr error
	
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}

		err := c.sendRequest(ctx, payloadBytes)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !c.isRetryable(err) {
			return fmt.Errorf("non-retryable error: %w", err)
		}
	}

	return fmt.Errorf("failed after %d retries: %w", c.maxRetries, lastErr)
}

// sendRequest sends a single webhook request.
func (c *WebhookChannel) sendRequest(ctx context.Context, payloadBytes []byte) error {
	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Generate HMAC signature
	signature := c.generateSignature(payloadBytes)

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llm-gateway-webhook/1.0")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))

	// Send request
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body (for error messages)
	body, _ := io.ReadAll(resp.Body)

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &WebhookError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(body),
		}
	}

	return nil
}

// generateSignature generates HMAC-SHA256 signature for the payload.
func (c *WebhookChannel) generateSignature(payload []byte) string {
	if c.secret == "" {
		return ""
	}

	h := hmac.New(sha256.New, []byte(c.secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// isRetryable determines if an error should be retried.
func (c *WebhookChannel) isRetryable(err error) bool {
	// Context errors are not retryable
	if err == context.Canceled || err == context.DeadlineExceeded {
		return false
	}

	// Check if it's a webhook error
	if webhookErr, ok := err.(*WebhookError); ok {
		// Retry on server errors (5xx) and rate limiting (429)
		return webhookErr.StatusCode >= 500 || webhookErr.StatusCode == 429
	}

	// Retry on network errors
	return true
}

// VerifyWebhookSignature verifies the HMAC signature of an incoming webhook payload.
// This can be used by webhook receivers to validate authenticity.
func VerifyWebhookSignature(payload []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}

	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	expectedSignature := hex.EncodeToString(h.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

// WebhookError represents an error response from a webhook endpoint.
type WebhookError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *WebhookError) Error() string {
	return fmt.Sprintf("webhook request failed with status %d: %s", e.StatusCode, e.Status)
}

// IsWebhookError checks if an error is a WebhookError.
func IsWebhookError(err error) bool {
	_, ok := err.(*WebhookError)
	return ok
}

// SetMaxRetries sets the maximum number of retry attempts.
func (c *WebhookChannel) SetMaxRetries(maxRetries int) {
	c.maxRetries = maxRetries
}

// SetTimeout sets the HTTP client timeout.
func (c *WebhookChannel) SetTimeout(timeout time.Duration) {
	c.client.Timeout = timeout
}
