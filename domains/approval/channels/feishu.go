// Package channels provides notification channel implementations for approval workflows.
//
// This package contains implementations for various notification channels including
// Feishu (Lark), WeChat Work, DingTalk, Email, and Webhook.
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// FeishuChannel implements approval notifications via Feishu (Lark) interactive cards.
type FeishuChannel struct {
	appID        string
	appSecret    string
	callbackURL  string // Base URL for webhook callbacks
	client       *http.Client
	tokenCache   *feishuTokenCache
	mu           sync.RWMutex
}

// feishuTokenCache manages access token caching with expiration.
type feishuTokenCache struct {
	token     string
	expiresAt time.Time
	mu        sync.RWMutex
}

// FeishuConfig contains configuration for Feishu channel.
type FeishuConfig struct {
	AppID       string
	AppSecret   string
	CallbackURL string // Base URL for webhook callbacks, e.g., https://api.example.com
}

// NewFeishuChannel creates a new Feishu notification channel.
func NewFeishuChannel(config FeishuConfig) (*FeishuChannel, error) {
	if config.AppID == "" {
		return nil, fmt.Errorf("app_id is required")
	}
	if config.AppSecret == "" {
		return nil, fmt.Errorf("app_secret is required")
	}
	if config.CallbackURL == "" {
		return nil, fmt.Errorf("callback_url is required")
	}

	return &FeishuChannel{
		appID:       config.AppID,
		appSecret:   config.AppSecret,
		callbackURL: config.CallbackURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		tokenCache: &feishuTokenCache{},
	}, nil
}

// SendApprovalNotification sends an interactive card to approvers via Feishu.
func (c *FeishuChannel) SendApprovalNotification(ctx context.Context, req *approval.ApprovalRequest, approvers []approval.Approver) error {
	if len(approvers) == 0 {
		return fmt.Errorf("no approvers specified")
	}

	// Get access token
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// Build interactive card
	card := c.buildApprovalCard(req)

	// Send to each approver
	var lastErr error
	successCount := 0

	for _, approver := range approvers {
		if approver.Email == "" {
			continue // Skip approvers without email (we use email as open_id for Feishu)
		}

		err := c.sendMessage(ctx, token, approver.Email, card)
		if err != nil {
			lastErr = fmt.Errorf("failed to send to %s: %w", approver.Name, err)
			continue
		}
		successCount++
	}

	if successCount == 0 {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("failed to send to any approvers")
	}

	return nil
}

// TestConnection verifies the Feishu API configuration by requesting an access token.
func (c *FeishuChannel) TestConnection(ctx context.Context) error {
	_, err := c.getAccessToken(ctx)
	return err
}

// getAccessToken retrieves a valid access token, using cache if available.
func (c *FeishuChannel) getAccessToken(ctx context.Context) (string, error) {
	// Check cache first
	c.tokenCache.mu.RLock()
	if c.tokenCache.token != "" && time.Now().Before(c.tokenCache.expiresAt) {
		token := c.tokenCache.token
		c.tokenCache.mu.RUnlock()
		return token, nil
	}
	c.tokenCache.mu.RUnlock()

	// Request new token
	c.tokenCache.mu.Lock()
	defer c.tokenCache.mu.Unlock()

	// Double-check after acquiring write lock
	if c.tokenCache.token != "" && time.Now().Before(c.tokenCache.expiresAt) {
		return c.tokenCache.token, nil
	}

	// Request token from Feishu API
	reqBody := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Code != 0 {
		return "", fmt.Errorf("feishu API error: code=%d, msg=%s", tokenResp.Code, tokenResp.Msg)
	}

	// Cache token with 5-minute buffer before expiration
	c.tokenCache.token = tokenResp.TenantAccessToken
	c.tokenCache.expiresAt = time.Now().Add(time.Duration(tokenResp.Expire)*time.Second - 5*time.Minute)

	return tokenResp.TenantAccessToken, nil
}

// sendMessage sends an interactive card message to a specific user.
func (c *FeishuChannel) sendMessage(ctx context.Context, token, userEmail string, card *FeishuInteractiveCard) error {
	message := map[string]interface{}{
		"receive_id": userEmail,
		"msg_type":   "interactive",
		"content":    card.ToJSON(),
	}

	bodyBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=email",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var msgResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := json.Unmarshal(body, &msgResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if msgResp.Code != 0 {
		return fmt.Errorf("feishu API error: code=%d, msg=%s", msgResp.Code, msgResp.Msg)
	}

	return nil
}

// buildApprovalCard constructs an interactive card for approval request.
func (c *FeishuChannel) buildApprovalCard(req *approval.ApprovalRequest) *FeishuInteractiveCard {
	// Determine card header color based on risk level
	headerTemplate := c.riskLevelColor(req.RiskLevel)

	card := &FeishuInteractiveCard{
		Header: FeishuCardHeader{
			Title: FeishuCardTitle{
				Content: fmt.Sprintf("🔔 审批请求 - %s", req.RiskLevel),
				Tag:     "plain_text",
			},
			Template: headerTemplate,
		},
		Elements: []FeishuCardElement{},
	}

	// Add trigger information
	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "div",
		Fields: []FeishuCardField{
			{
				IsShort: true,
				Text: FeishuCardText{
					Tag:     "lark_md",
					Content: fmt.Sprintf("**触发原因**\n%s", req.TriggerReason),
				},
			},
			{
				IsShort: true,
				Text: FeishuCardText{
					Tag:     "lark_md",
					Content: fmt.Sprintf("**风险等级**\n%s", c.riskLevelEmoji(req.RiskLevel)),
				},
			},
		},
	})

	// Add session and cost information
	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "div",
		Fields: []FeishuCardField{
			{
				IsShort: true,
				Text: FeishuCardText{
					Tag:     "lark_md",
					Content: fmt.Sprintf("**会话 ID**\n`%s`", req.SessionID),
				},
			},
			{
				IsShort: true,
				Text: FeishuCardText{
					Tag:     "lark_md",
					Content: fmt.Sprintf("**预估成本**\n$%.4f (~%d tokens)", req.EstimatedCost, req.EstimatedTokens),
				},
			},
		},
	})

	// Add user message (truncated if too long)
	userMessage := req.UserMessage
	if len(userMessage) > 500 {
		userMessage = userMessage[:497] + "..."
	}

	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "div",
		Text: &FeishuCardText{
			Tag:     "lark_md",
			Content: fmt.Sprintf("**用户消息**\n%s", userMessage),
		},
	})

	// Add sensitive information warning if present
	if len(req.SensitiveInfo) > 0 {
		sensitiveTypes := make(map[string]int)
		for _, item := range req.SensitiveInfo {
			sensitiveTypes[item.Type]++
		}

		var warning string
		for sType, count := range sensitiveTypes {
			if warning != "" {
				warning += ", "
			}
			warning += fmt.Sprintf("%s×%d", sType, count)
		}

		card.Elements = append(card.Elements, FeishuCardElement{
			Tag: "note",
			Elements: []FeishuCardElement{
				{
					Tag:     "plain_text",
					Content: fmt.Sprintf("⚠️ 检测到敏感信息: %s", warning),
				},
			},
		})
	}

	// Add horizontal rule
	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "hr",
	})

	// Add action buttons
	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "action",
		Actions: []FeishuCardAction{
			{
				Tag: "button",
				Text: FeishuCardText{
					Content: "✅ 批准",
					Tag:     "plain_text",
				},
				Type: "primary",
				Value: map[string]string{
					"action":     "approve",
					"request_id": req.RequestID,
				},
				URL: fmt.Sprintf("%s/api/webhooks/feishu/approval-callback?action=approve&request_id=%s",
					c.callbackURL, req.RequestID),
			},
			{
				Tag: "button",
				Text: FeishuCardText{
					Content: "❌ 拒绝",
					Tag:     "plain_text",
				},
				Type: "danger",
				Value: map[string]string{
					"action":     "reject",
					"request_id": req.RequestID,
				},
				URL: fmt.Sprintf("%s/api/webhooks/feishu/approval-callback?action=reject&request_id=%s",
					c.callbackURL, req.RequestID),
			},
		},
	})

	// Add footer with expiration time
	timeLeft := time.Until(req.ExpiresAt)
	card.Elements = append(card.Elements, FeishuCardElement{
		Tag: "note",
		Elements: []FeishuCardElement{
			{
				Tag:     "plain_text",
				Content: fmt.Sprintf("⏰ 请在 %s 内处理，创建于 %s", formatDuration(timeLeft), req.CreatedAt.Format("2006-01-02 15:04:05")),
			},
		},
	})

	return card
}

// riskLevelColor returns the Feishu card header template color for a risk level.
func (c *FeishuChannel) riskLevelColor(level approval.RiskLevel) string {
	switch level {
	case approval.RiskLow:
		return "green"
	case approval.RiskMedium:
		return "yellow"
	case approval.RiskHigh:
		return "orange"
	case approval.RiskCritical:
		return "red"
	default:
		return "blue"
	}
}

// riskLevelEmoji returns an emoji representation of the risk level.
func (c *FeishuChannel) riskLevelEmoji(level approval.RiskLevel) string {
	switch level {
	case approval.RiskLow:
		return "🟢 LOW"
	case approval.RiskMedium:
		return "🟡 MEDIUM"
	case approval.RiskHigh:
		return "🟠 HIGH"
	case approval.RiskCritical:
		return "🔴 CRITICAL"
	default:
		return "⚪ UNKNOWN"
	}
}

// formatDuration formats a duration into a human-readable string.
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "已过期"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1f小时", d.Hours())
	}
	return fmt.Sprintf("%.1f天", d.Hours()/24)
}

// Feishu Interactive Card structures

// FeishuInteractiveCard represents a Feishu interactive message card.
type FeishuInteractiveCard struct {
	Header   FeishuCardHeader    `json:"header"`
	Elements []FeishuCardElement `json:"elements"`
}

// ToJSON converts the card to JSON string.
func (c *FeishuInteractiveCard) ToJSON() string {
	data, _ := json.Marshal(c)
	return string(data)
}

// FeishuCardHeader represents the card header.
type FeishuCardHeader struct {
	Title    FeishuCardTitle `json:"title"`
	Template string          `json:"template"` // Color: blue/wathet/turquoise/green/yellow/orange/red/carmine/violet/purple/indigo/grey
}

// FeishuCardTitle represents the card title.
type FeishuCardTitle struct {
	Content string `json:"content"`
	Tag     string `json:"tag"` // plain_text or lark_md
}

// FeishuCardElement represents a card element (can be div, action, note, hr, etc.).
type FeishuCardElement struct {
	Tag      string              `json:"tag"`
	Text     *FeishuCardText     `json:"text,omitempty"`
	Fields   []FeishuCardField   `json:"fields,omitempty"`
	Actions  []FeishuCardAction  `json:"actions,omitempty"`
	Elements []FeishuCardElement `json:"elements,omitempty"`
	Content  string              `json:"content,omitempty"` // For plain_text in note elements
}

// FeishuCardText represents text content.
type FeishuCardText struct {
	Content string `json:"content"`
	Tag     string `json:"tag"` // plain_text or lark_md
}

// FeishuCardField represents a field in a div element.
type FeishuCardField struct {
	IsShort bool            `json:"is_short"`
	Text    FeishuCardText  `json:"text"`
}

// FeishuCardAction represents an action button.
type FeishuCardAction struct {
	Tag   string            `json:"tag"`   // button
	Text  FeishuCardText    `json:"text"`
	Type  string            `json:"type"`  // default/primary/danger
	Value map[string]string `json:"value"` // Data passed to callback
	URL   string            `json:"url"`   // URL to open when clicked
}
