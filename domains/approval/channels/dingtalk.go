// Package channels provides notification channel implementations for approval workflows.
//
// This package implements various notification channels (DingTalk, Feishu, WeChat, etc.)
// that can send approval notifications to approvers.
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

const (
	// DingTalk API endpoints
	dingTalkTokenURL   = "https://oapi.dingtalk.com/gettoken"
	dingTalkMessageURL = "https://oapi.dingtalk.com/topapi/message/corpconversation/asyncsend_v2"

	// Token cache TTL (DingTalk access_token expires in 7200 seconds)
	tokenCacheTTL = 7000 * time.Second
)

// DingTalkChannel implements the notification channel for DingTalk.
type DingTalkChannel struct {
	appKey    string
	appSecret string
	client    *http.Client

	// Token cache
	tokenMu     sync.RWMutex
	cachedToken string
	tokenExpiry time.Time

	// Base URL for approval detail page
	baseURL string
}

// NewDingTalkChannel creates a new DingTalk notification channel.
func NewDingTalkChannel(appKey, appSecret, baseURL string) *DingTalkChannel {
	return &DingTalkChannel{
		appKey:    appKey,
		appSecret: appSecret,
		baseURL:   baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SendApprovalNotification sends an approval notification via DingTalk.
func (c *DingTalkChannel) SendApprovalNotification(ctx context.Context, req *approval.ApprovalRequest, approvers []approval.Approver) error {
	if len(approvers) == 0 {
		return fmt.Errorf("no approvers provided")
	}

	// Get access token
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("get access token: %w", err)
	}

	// Build user ID list
	userIDs := make([]string, 0, len(approvers))
	for _, approver := range approvers {
		if approver.Enabled && approver.UserID != "" {
			userIDs = append(userIDs, approver.UserID)
		}
	}

	if len(userIDs) == 0 {
		return fmt.Errorf("no enabled approvers with valid user IDs")
	}

	// Build notification message
	message := c.buildMessage(req)

	// Send message to each approver
	for _, userID := range userIDs {
		if err := c.sendMessage(ctx, token, userID, message); err != nil {
			// Log error but continue sending to other approvers
			// In production, you might want to track failed notifications
			return fmt.Errorf("send message to user %s: %w", userID, err)
		}
	}

	return nil
}

// getAccessToken retrieves and caches the DingTalk access token.
func (c *DingTalkChannel) getAccessToken(ctx context.Context) (string, error) {
	// Check cache first
	c.tokenMu.RLock()
	if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
		token := c.cachedToken
		c.tokenMu.RUnlock()
		return token, nil
	}
	c.tokenMu.RUnlock()

	// Acquire write lock to refresh token
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// Double-check after acquiring write lock
	if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.cachedToken, nil
	}

	// Request new token
	url := fmt.Sprintf("%s?appkey=%s&appsecret=%s", dingTalkTokenURL, c.appKey, c.appSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	var tokenResp struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}

	if tokenResp.ErrCode != 0 {
		return "", fmt.Errorf("dingtalk API error: code=%d, msg=%s", tokenResp.ErrCode, tokenResp.ErrMsg)
	}

	// Cache the token
	c.cachedToken = tokenResp.AccessToken
	c.tokenExpiry = time.Now().Add(tokenCacheTTL)

	return tokenResp.AccessToken, nil
}

// buildMessage constructs the notification message content.
func (c *DingTalkChannel) buildMessage(req *approval.ApprovalRequest) map[string]interface{} {
	// Format risk level with emoji
	riskEmoji := map[approval.RiskLevel]string{
		approval.RiskLow:      "🟢",
		approval.RiskMedium:   "🟡",
		approval.RiskHigh:     "🟠",
		approval.RiskCritical: "🔴",
	}

	emoji := riskEmoji[req.RiskLevel]
	if emoji == "" {
		emoji = "⚪"
	}

	// Build approval detail URL - trim all trailing slashes
	baseURL := strings.TrimRight(c.baseURL, "/")
	detailURL := fmt.Sprintf("%s/approval/%s", baseURL, req.RequestID)

	// Build markdown message
	var msgBuilder strings.Builder
	msgBuilder.WriteString(fmt.Sprintf("## %s LLM 请求需要审批\n\n", emoji))
	msgBuilder.WriteString(fmt.Sprintf("**触发原因**: %s\n\n", req.TriggerReason))
	msgBuilder.WriteString(fmt.Sprintf("**风险级别**: %s %s\n\n", emoji, req.RiskLevel))
	msgBuilder.WriteString(fmt.Sprintf("**会话ID**: %s\n\n", req.SessionID))
	
	// Session summary
	if req.SessionSummary.MessageCount > 0 {
		msgBuilder.WriteString(fmt.Sprintf("**会话消息数**: %d\n\n", req.SessionSummary.MessageCount))
	}
	if req.SessionSummary.UserIntent != "" {
		msgBuilder.WriteString(fmt.Sprintf("**用户意图**: %s\n\n", req.SessionSummary.UserIntent))
	}

	// Cost estimation
	if req.EstimatedCost > 0 {
		msgBuilder.WriteString(fmt.Sprintf("**预估成本**: ¥%.4f (约 %d tokens)\n\n", req.EstimatedCost, req.EstimatedTokens))
	}

	// User message (truncate if too long)
	userMsg := req.UserMessage
	if len(userMsg) > 200 {
		userMsg = userMsg[:200] + "..."
	}
	msgBuilder.WriteString(fmt.Sprintf("**用户消息**: %s\n\n", userMsg))

	// Sensitive info summary
	if len(req.SensitiveInfo) > 0 {
		msgBuilder.WriteString("**检测到敏感信息**:\n")
		for _, item := range req.SensitiveInfo {
			msgBuilder.WriteString(fmt.Sprintf("- %s (置信度: %.0f%%)\n", item.Type, item.Confidence*100))
		}
		msgBuilder.WriteString("\n")
	}

	// Expiry time
	msgBuilder.WriteString(fmt.Sprintf("**过期时间**: %s\n\n", req.ExpiresAt.Format("2006-01-02 15:04:05")))

	// Action links
	msgBuilder.WriteString(fmt.Sprintf("[查看详情](%s)\n", detailURL))

	// Build message structure
	message := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]interface{}{
			"title": fmt.Sprintf("%s 审批请求 - %s", emoji, req.TriggerReason),
			"text":  msgBuilder.String(),
		},
	}

	return message
}

// sendMessage sends a work notification message to a specific user.
func (c *DingTalkChannel) sendMessage(ctx context.Context, token, userID string, message map[string]interface{}) error {
	url := fmt.Sprintf("%s?access_token=%s", dingTalkMessageURL, token)

	// Build request body
	reqBody := map[string]interface{}{
		"agent_id":    c.appKey, // Use appKey as agent_id (simplified)
		"userid_list": userID,
		"msg":         message,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create message request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("execute message request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read message response: %w", err)
	}

	var msgResp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		TaskID  int64  `json:"task_id"`
	}

	if err := json.Unmarshal(body, &msgResp); err != nil {
		return fmt.Errorf("parse message response: %w", err)
	}

	if msgResp.ErrCode != 0 {
		return fmt.Errorf("dingtalk API error: code=%d, msg=%s", msgResp.ErrCode, msgResp.ErrMsg)
	}

	return nil
}

// InvalidateToken clears the cached access token (useful for testing or error recovery).
func (c *DingTalkChannel) InvalidateToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.cachedToken = ""
	c.tokenExpiry = time.Time{}
}
