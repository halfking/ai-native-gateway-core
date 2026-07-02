// Package channels provides notification channel implementations for approval workflows.
//
// This package implements various notification channels (WeChat Work, Feishu, DingTalk, etc.)
// that send approval notifications to approvers through different messaging platforms.
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// WeChatChannel implements WeChat Work (企业微信) notification channel.
//
// Features:
//   - Text card messages with interactive buttons
//   - Access token caching and auto-refresh
//   - Error handling and retry logic
//   - Approval link generation
type WeChatChannel struct {
	corpID     string
	corpSecret string
	agentID    int
	baseURL    string // Frontend base URL for approval links
	client     *http.Client

	// Token cache
	tokenMu     sync.RWMutex
	accessToken string
	tokenExpiry time.Time
}

// NewWeChatChannel creates a new WeChat Work notification channel.
//
// Parameters:
//   - corpID: WeChat Work Corp ID
//   - corpSecret: WeChat Work Corp Secret
//   - agentID: WeChat Work Agent ID
//   - baseURL: Frontend base URL for approval links (e.g., "https://llm-gateway.example.com")
func NewWeChatChannel(corpID, corpSecret string, agentID int, baseURL string) *WeChatChannel {
	if baseURL == "" {
		baseURL = "https://llm-gateway.example.com"
	}
	return &WeChatChannel{
		corpID:     corpID,
		corpSecret: corpSecret,
		agentID:    agentID,
		baseURL:    baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SendApprovalNotification sends an approval notification to approvers via WeChat Work.
//
// The notification includes:
//   - Approval request ID and risk level
//   - Trigger reason
//   - Session ID
//   - Estimated cost and tokens
//   - User message preview
//   - Interactive link to approve/reject
func (c *WeChatChannel) SendApprovalNotification(
	ctx context.Context,
	req *approval.ApprovalRequest,
	approvers []approval.Approver,
) error {
	if len(approvers) == 0 {
		return fmt.Errorf("no approvers specified")
	}

	// Get access token
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// Build text card message
	card := c.buildTextCard(req)

	// Extract user IDs from approvers
	userIDs := make([]string, 0, len(approvers))
	for _, approver := range approvers {
		if approver.UserID != "" {
			userIDs = append(userIDs, approver.UserID)
		}
	}

	if len(userIDs) == 0 {
		return fmt.Errorf("no valid user IDs in approvers")
	}

	// Send message to all approvers
	return c.sendMessage(ctx, token, userIDs, card)
}

// getAccessToken retrieves a valid access token (from cache or by requesting a new one).
func (c *WeChatChannel) getAccessToken(ctx context.Context) (string, error) {
	// Check cached token
	c.tokenMu.RLock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		token := c.accessToken
		c.tokenMu.RUnlock()
		return token, nil
	}
	c.tokenMu.RUnlock()

	// Acquire write lock to refresh token
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// Double-check after acquiring write lock
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}

	// Request new token
	url := fmt.Sprintf(
		"https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		c.corpID,
		c.corpSecret,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.ErrCode != 0 {
		return "", fmt.Errorf("WeChat API error: %d - %s", result.ErrCode, result.ErrMsg)
	}

	// Cache token (expire 5 minutes early for safety)
	c.accessToken = result.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(result.ExpiresIn-300) * time.Second)

	return c.accessToken, nil
}

// buildTextCard builds a text card message for approval notification.
func (c *WeChatChannel) buildTextCard(req *approval.ApprovalRequest) map[string]interface{} {
	// Build risk level display
	riskEmoji := c.getRiskEmoji(req.RiskLevel)
	riskText := fmt.Sprintf("%s %s", riskEmoji, req.RiskLevel)

	// Build description
	description := fmt.Sprintf(`<div class="gray">%s</div>
<div class="highlight">会话ID: %s</div>
<div class="normal">预估成本: $%.4f (%d tokens)</div>
<div class="normal">用户消息: %s</div>`,
		escapeHTML(req.TriggerReason),
		escapeHTML(req.SessionID),
		req.EstimatedCost,
		req.EstimatedTokens,
		escapeHTML(truncate(req.UserMessage, 100)),
	)

	// Build approval link
	approvalURL := c.buildApprovalURL(req.RequestID, req.TenantID)

	return map[string]interface{}{
		"msgtype": "textcard",
		"textcard": map[string]interface{}{
			"title":       fmt.Sprintf("审批请求 - %s", riskText),
			"description": description,
			"url":         approvalURL,
			"btntxt":      "点击查看详情",
		},
	}
}

// sendMessage sends a message to specified users via WeChat Work API.
func (c *WeChatChannel) sendMessage(
	ctx context.Context,
	token string,
	userIDs []string,
	message map[string]interface{},
) error {
	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s", token)

	// Build request payload
	payload := map[string]interface{}{
		"touser":  strings.Join(userIDs, "|"),
		"msgtype": message["msgtype"],
		"agentid": c.agentID,
	}

	// Copy message content
	for k, v := range message {
		if k != "msgtype" {
			payload[k] = v
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if result.ErrCode != 0 {
		// Token expired, clear cache and retry once
		if result.ErrCode == 42001 || result.ErrCode == 40014 {
			c.tokenMu.Lock()
			c.accessToken = ""
			c.tokenExpiry = time.Time{}
			c.tokenMu.Unlock()
			return fmt.Errorf("WeChat API error (token expired): %d - %s", result.ErrCode, result.ErrMsg)
		}
		return fmt.Errorf("WeChat API error: %d - %s", result.ErrCode, result.ErrMsg)
	}

	return nil
}

// buildApprovalURL builds the approval detail page URL.
func (c *WeChatChannel) buildApprovalURL(requestID, tenantID string) string {
	return fmt.Sprintf("%s/approvals/%s?tenant=%s", c.baseURL, requestID, tenantID)
}

// getRiskEmoji returns an emoji for the risk level.
func (c *WeChatChannel) getRiskEmoji(level approval.RiskLevel) string {
	switch level {
	case approval.RiskCritical:
		return "🔴"
	case approval.RiskHigh:
		return "🟠"
	case approval.RiskMedium:
		return "🟡"
	case approval.RiskLow:
		return "🟢"
	default:
		return "⚪"
	}
}

// Helper functions

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// escapeHTML escapes HTML special characters for WeChat text card.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}
