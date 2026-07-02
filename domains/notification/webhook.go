// Package notification — webhook.go
//
// WebhookChannel 通过 HTTP POST 发送审批通知到自定义 URL。
// 实现 NotificationChannel 接口（ParseCallback 解析回调请求体）。
// 支持请求重试、超时控制和 HMAC 签名验证。
package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// WebhookConfig Webhook 渠道配置。
type WebhookConfig struct {
	URL         string // 目标 URL
	Secret      string // HMAC 签名密钥（空则不签名）
	MaxRetries  int    // 最大重试次数（默认 3）
	TimeoutSec  int    // 单次请求超时秒数（默认 30）
}

// WebhookChannel HTTP Webhook 通知渠道。
type WebhookChannel struct {
	cfg    WebhookConfig
	client *http.Client
}

// NewWebhookChannel 创建 Webhook 渠道。
func NewWebhookChannel(cfg WebhookConfig) *WebhookChannel {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 30
	}
	return &WebhookChannel{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSec) * time.Second,
		},
	}
}

// Name 返回渠道标识。
func (c *WebhookChannel) Name() string { return "webhook" }

// Send 发送普通文本消息（JSON 格式 POST）。
func (c *WebhookChannel) Send(ctx context.Context, msg *Message) error {
	payload := map[string]any{
		"type":     "text",
		"title":    msg.Title,
		"content":  msg.Content,
		"priority": msg.Priority,
	}
	return c.postWithRetry(ctx, payload)
}

// SendCard 发送审批卡片（完整 JSON POST，含批准/拒绝数据）。
func (c *WebhookChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	payload := map[string]any{
		"type":     "approval_card",
		"header":   card.Header,
		"elements": card.Elements,
		"actions":  card.Actions,
		"metadata": card.Metadata,
	}
	return c.postWithRetry(ctx, payload)
}

// ParseCallback 解析 Webhook 回调（接收方处理审批后的回调通知）。
func (c *WebhookChannel) ParseCallback(ctx context.Context, raw []byte) (*Callback, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("webhook channel: empty callback body")
	}
	var cb Callback
	if err := json.Unmarshal(raw, &cb); err != nil {
		return nil, fmt.Errorf("webhook channel: parse callback: %w", err)
	}
	if cb.SessionID == "" && cb.ID == "" {
		return nil, fmt.Errorf("webhook channel: callback missing session_id/id")
	}
	return &cb, nil
}

// HealthCheck 发送 HEAD 请求验证 URL 可达性。
func (c *WebhookChannel) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("webhook healthcheck: build request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook healthcheck: %w", err)
	}
	defer resp.Body.Close()
	// 2xx / 3xx / 405（HEAD 不支持）都算可达
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		return fmt.Errorf("webhook healthcheck: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// postWithRetry 带重试的 POST 请求。
// 重试策略：指数退避（1s, 2s, 4s），5xx 和网络错误重试，4xx 不重试。
func (c *WebhookChannel) postWithRetry(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook channel: marshal payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return fmt.Errorf("webhook channel: cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		err := c.doPost(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err

		// 4xx 错误不重试（客户端错误）
		if isClientError(err) {
			slog.Warn("webhook channel: client error, not retrying",
				"url", c.cfg.URL, "error", err)
			return err
		}
		slog.Warn("webhook channel: retrying",
			"attempt", attempt+1, "max", c.cfg.MaxRetries, "error", err)
	}
	return fmt.Errorf("webhook channel: failed after %d retries: %w", c.cfg.MaxRetries, lastErr)
}

// doPost 执行单次 POST 请求（含 HMAC 签名）。
func (c *WebhookChannel) doPost(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llm-gateway-webhook/1.0")

	// HMAC-SHA256 签名（X-Webhook-Signature 头）
	if c.cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(c.cfg.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Webhook-Signature", sig)
		req.Header.Set("X-Webhook-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 读取响应体用于错误信息（限制 1KB）
	bodySnippet := make([]byte, 0, 1024)
	n, _ := io.ReadFull(resp.Body, bodySnippet)
	return &webhookHTTPError{
		StatusCode: resp.StatusCode,
		Body:       string(bodySnippet[:n]),
	}
}

// webhookHTTPError 携带 HTTP 状态码的错误，用于区分 4xx/5xx。
type webhookHTTPError struct {
	StatusCode int
	Body       string
}

func (e *webhookHTTPError) Error() string {
	return fmt.Sprintf("webhook returned status %d: %s", e.StatusCode, e.Body)
}

// isClientError 判断是否为 4xx 客户端错误（不重试）。
func isClientError(err error) bool {
	var httpErr *webhookHTTPError
	if !asWebhookError(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode >= 400 && httpErr.StatusCode < 500
}

// asWebhookError 是 errors.As 的本地包装（避免引入 errors 包的复杂度）。
func asWebhookError(err error, target **webhookHTTPError) bool {
	if err == nil {
		return false
	}
	if he, ok := err.(*webhookHTTPError); ok {
		*target = he
		return true
	}
	return false
}
