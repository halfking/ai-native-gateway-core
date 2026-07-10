// Package notification — dingtalk.go
//
// 钉钉（DingTalk）通知渠道实现。
//
// 支持：
//   - 群机器人 Webhook（加签 / 自定义关键词）
//   - 工作通知（基于 access_token + agent_id，按 userid 列表下发）
//   - 交互式卡片（ActionCard 格式）
//
// 凭证策略：
//   - Webhook URL 包含 access_token，敏感度低
//   - 加签 secret 用于签名校验，不进入日志
package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DingTalkConfig 钉钉渠道配置。
//
// 三种模式互斥：
//   - Webhook 模式：WebhookURL + SignSecret 可选
//   - App 模式：AppKey + AppSecret + AgentID
type DingTalkConfig struct {
	WebhookURL string // 群机器人 Webhook（包含 access_token）
	SignSecret string // 加签 secret（敏感，可选）

	AppKey    string // 应用 Key（App 模式）
	AppSecret string // 应用 Secret（App 模式，敏感）
	AgentID   string // 应用 AgentID（App 模式）

	BaseURL string // 可选，默认 https://oapi.dingtalk.com
}

// DingTalkChannel 钉钉通知渠道。
type DingTalkChannel struct {
	config     DingTalkConfig
	httpClient *http.Client
}

// NewDingTalkChannel 创建钉钉渠道。
func NewDingTalkChannel(config DingTalkConfig) *DingTalkChannel {
	if config.BaseURL == "" {
		config.BaseURL = "https://oapi.dingtalk.com"
	}
	return &DingTalkChannel{
		config:     config,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name 返回渠道标识。
func (c *DingTalkChannel) Name() string {
	return string(ChannelDingTalk)
}

// Send 发送文本消息。
//
// Webhook 模式：@指定用户（Recipients 列表）；App 模式：通过 agent_id + userid 列表发工作通知。
func (c *DingTalkChannel) Send(ctx context.Context, msg *Message) error {
	if c.config.WebhookURL != "" {
		return c.sendWebhook(ctx, msg)
	}
	if c.hasAppConfig() {
		return c.sendWorkNotification(ctx, msg)
	}
	return fmt.Errorf("notification: dingtalk missing webhook or agent config")
}

// SendCard 发送 ActionCard 交互式卡片。
func (c *DingTalkChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	if c.config.WebhookURL != "" {
		return c.sendWebhookCard(ctx, card)
	}
	if c.hasAppConfig() {
		// App 模式降级为工作通知 Markdown（ActionCard 交互仅群机器人支持）
		return c.sendWorkNotificationFromCard(ctx, card)
	}
	return fmt.Errorf("notification: dingtalk missing webhook or agent config")
}

// ParseCallback 解析钉钉群机器人回调（当前为单向推送，回调通常来自业务方）。
//
// 钉钉群机器人不直接发起回调，本方法主要用于校验外部系统推过来的业务回调。
func (c *DingTalkChannel) ParseCallback(ctx context.Context, raw []byte) (*Callback, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("notification: dingtalk parse callback: %w", err)
	}
	cb := &Callback{
		Action:    stringFromAny(data["action"]),
		Data:      data,
		Timestamp: time.Now(),
	}
	if v, ok := data["user_id"].(string); ok {
		cb.User.OpenID = v
		cb.User.ID = v
	}
	if v, ok := data["user_name"].(string); ok {
		cb.User.Name = v
	}
	if v, ok := data["tenant_id"].(string); ok {
		cb.TenantID = v
	}
	if v, ok := data["session_id"].(string); ok {
		cb.SessionID = v
	}
	return cb, nil
}

// HealthCheck 校验 webhook 可达或 app token 可获取。
func (c *DingTalkChannel) HealthCheck(ctx context.Context) error {
	if c.config.WebhookURL != "" {
		// GET webhook 仅用于存活探测（钉钉不返回 body）
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.WebhookURL, nil)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("notification: dingtalk webhook health: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("notification: dingtalk webhook unhealthy status %d", resp.StatusCode)
		}
		return nil
	}
	if c.hasAppConfig() {
		_, err := c.getAppAccessToken(ctx)
		if err != nil {
			return fmt.Errorf("notification: dingtalk health: %w", err)
		}
		return nil
	}
	return fmt.Errorf("notification: dingtalk no config to check")
}

func (c *DingTalkChannel) hasAppConfig() bool {
	return c.config.AppKey != "" && c.config.AppSecret != "" && c.config.AgentID != ""
}

// sendWebhook 通过群机器人 Webhook 发送文本。
func (c *DingTalkChannel) sendWebhook(ctx context.Context, msg *Message) error {
	body := map[string]any{
		"msgtype": "text",
		"text": map[string]any{
			"content": fmt.Sprintf("%s\n\n%s", msg.Title, msg.Content),
		},
	}
	if len(msg.Recipients) > 0 {
		at := map[string]any{}
		atMobiles := make([]string, 0, len(msg.Recipients))
		for _, r := range msg.Recipients {
			atMobiles = append(atMobiles, r)
		}
		at["atMobiles"] = atMobiles
		body["at"] = at
	}
	return c.postSigned(ctx, body)
}

// sendWebhookCard 通过群机器人发送 ActionCard。
func (c *DingTalkChannel) sendWebhookCard(ctx context.Context, card *InteractiveCard) error {
	recipients, _ := card.Metadata["recipients"].([]string)

	text := buildCardText(card)
	title := card.Header.Title
	if title == "" {
		title = "审批通知"
	}

	body := map[string]any{
		"msgtype": "actionCard",
		"actionCard": map[string]any{
			"title":       title,
			"text":        text,
			"singleTitle": "查看详情",
			"singleURL":   stringFromAny(card.Metadata["detail_url"]),
		},
	}
	if len(recipients) > 0 {
		body["at"] = map[string]any{"atMobiles": recipients}
	}
	return c.postSigned(ctx, body)
}

// postSigned 计算加签后 POST 到 webhook。
func (c *DingTalkChannel) postSigned(ctx context.Context, body map[string]any) error {
	endpoint := c.config.WebhookURL
	if c.config.SignSecret != "" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sign := c.computeSign(timestamp)
		u, err := url.Parse(endpoint)
		if err != nil {
			return fmt.Errorf("notification: dingtalk parse webhook url: %w", err)
		}
		q := u.Query()
		q.Set("timestamp", timestamp)
		q.Set("sign", sign)
		u.RawQuery = q.Encode()
		endpoint = u.String()
	}

	bs, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("notification: dingtalk new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notification: dingtalk http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("notification: dingtalk status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("notification: dingtalk decode: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("notification: dingtalk api: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

func (c *DingTalkChannel) computeSign(timestamp string) string {
	stringToSign := timestamp + "\n" + c.config.SignSecret
	h := hmac.New(sha256.New, []byte(c.config.SignSecret))
	h.Write([]byte(stringToSign))
	sum := h.Sum(nil)
	return base64.StdEncoding.EncodeToString(sum)
}

// sendWorkNotification 通过工作通知发送 Markdown。
func (c *DingTalkChannel) sendWorkNotification(ctx context.Context, msg *Message) error {
	token, err := c.getAppAccessToken(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"msg": map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"title": msg.Title,
				"text":  fmt.Sprintf("# %s\n\n%s", msg.Title, msg.Content),
			},
		},
		"userid_list": joinStrings(msg.Recipients, ","),
		"agent_id":    c.config.AgentID,
	}
	return c.postJSON(ctx, "/topapi/message/corpconversation/asyncsend_v2?access_token="+token, body)
}

// sendWorkNotificationFromCard 将卡片渲染为 Markdown 工作通知。
func (c *DingTalkChannel) sendWorkNotificationFromCard(ctx context.Context, card *InteractiveCard) error {
	recipients, _ := card.Metadata["recipients"].([]string)
	body := map[string]any{
		"msg": map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"title": card.Header.Title,
				"text":  buildCardText(card),
			},
		},
		"userid_list": joinStrings(recipients, ","),
		"agent_id":    c.config.AgentID,
	}
	token, err := c.getAppAccessToken(ctx)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/topapi/message/corpconversation/asyncsend_v2?access_token="+token, body)
}

func (c *DingTalkChannel) getAppAccessToken(ctx context.Context) (string, error) {
	body := map[string]any{
		"appkey":    c.config.AppKey,
		"appsecret": c.config.AppSecret,
	}
	bs, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.config.BaseURL+"/gettoken", bytes.NewReader(bs))
	if err != nil {
		return "", fmt.Errorf("notification: dingtalk new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("notification: dingtalk token http: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("notification: dingtalk token decode: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("notification: dingtalk token api: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("notification: dingtalk empty access_token")
	}
	slog.Debug("dingtalk access token refreshed")
	return result.AccessToken, nil
}

func (c *DingTalkChannel) postJSON(ctx context.Context, path string, body map[string]any) error {
	bs, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notification: dingtalk marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+path, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("notification: dingtalk new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notification: dingtalk http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("notification: dingtalk status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("notification: dingtalk decode: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("notification: dingtalk api: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

// buildCardText 把 InteractiveCard 渲染为钉钉 markdown 文本。
func buildCardText(card *InteractiveCard) string {
	var buf bytes.Buffer
	if card.Header.Title != "" {
		buf.WriteString("# ")
		buf.WriteString(card.Header.Title)
		buf.WriteString("\n\n")
	}
	for _, e := range card.Elements {
		switch e.Type {
		case ElementTypeText:
			buf.WriteString(e.Text)
			buf.WriteString("\n\n")
		case ElementTypeField:
			for _, f := range e.Fields {
				buf.WriteString("**")
				buf.WriteString(f.Key)
				buf.WriteString("**: ")
				buf.WriteString(f.Value)
				buf.WriteString("  \n")
			}
			buf.WriteString("\n")
		case ElementTypeDivider:
			buf.WriteString("---\n\n")
		case ElementTypeNote:
			buf.WriteString("> ")
			buf.WriteString(e.Text)
			buf.WriteString("\n\n")
		}
	}
	return buf.String()
}
