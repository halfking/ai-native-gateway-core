// Package notification — lark.go
//
// 飞书（Feishu/Lark）通知渠道实现。
//
// 支持：
//   - 文本消息下发（基于 tenant_access_token）
//   - 交互式卡片（审批按钮 / 字段展示）
//   - 回调签名验证（VerificationToken + EncryptKey）
//   - Token 自动刷新（提前 5 分钟过期）
//
// 重要：app_secret / encrypt_key 不进入日志。
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
	"sync"
	"time"
)

// LarkBotConfig 飞书机器人配置。
type LarkBotConfig struct {
	AppID             string // 应用 ID
	AppSecret         string // 应用 Secret（敏感）
	VerificationToken string // 回调验证 Token
	EncryptKey        string // 回调加密 Key（敏感）
	BaseURL           string // 可选，默认 https://open.feishu.cn
}

// LarkBotChannel 飞书通知渠道。
type LarkBotChannel struct {
	config      LarkBotConfig
	httpClient  *http.Client
	tokenMu     sync.RWMutex
	accessToken string
	tokenExpire time.Time
}

// NewLarkBotChannel 创建飞书机器人渠道。
func NewLarkBotChannel(config LarkBotConfig) *LarkBotChannel {
	if config.BaseURL == "" {
		config.BaseURL = "https://open.feishu.cn"
	}
	return &LarkBotChannel{
		config:     config,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name 返回渠道标识。
func (c *LarkBotChannel) Name() string {
	return string(ChannelLark)
}

// Send 发送普通文本消息。
func (c *LarkBotChannel) Send(ctx context.Context, msg *Message) error {
	if err := c.ensureAccessToken(ctx); err != nil {
		return fmt.Errorf("notification: lark ensure token: %w", err)
	}

	var lastErr error
	for _, recipient := range msg.Recipients {
		body := map[string]any{
			"receive_id_type": "open_id",
			"receive_id":      recipient,
			"msg_type":        "text",
			"content": map[string]any{
				"text": fmt.Sprintf("%s\n\n%s", msg.Title, msg.Content),
			},
		}
		if err := c.sendJSON(ctx, "/open-apis/im/v1/messages", body); err != nil {
			slog.Error("lark send failed",
				"recipient", recipient,
				"msg_id", msg.ID,
				"error", err)
			lastErr = err
			continue
		}
		slog.Info("lark message sent", "msg_id", msg.ID, "recipient", recipient)
	}
	return lastErr
}

// SendCard 发送交互式卡片。
//
// recipients 从 card.Metadata["recipients"] 取（与 ApprovalNotifier 约定）。
func (c *LarkBotChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	if err := c.ensureAccessToken(ctx); err != nil {
		return fmt.Errorf("notification: lark ensure token: %w", err)
	}

	recipients, ok := card.Metadata["recipients"].([]string)
	if !ok || len(recipients) == 0 {
		return fmt.Errorf("notification: lark card missing recipients in metadata")
	}

	cardJSON, err := json.Marshal(c.convertToLarkCard(card))
	if err != nil {
		return fmt.Errorf("notification: lark marshal card: %w", err)
	}

	var lastErr error
	for _, recipient := range recipients {
		body := map[string]any{
			"receive_id_type": "open_id",
			"receive_id":      recipient,
			"msg_type":        "interactive",
			"content":         string(cardJSON),
		}
		if err := c.sendJSON(ctx, "/open-apis/im/v1/messages", body); err != nil {
			slog.Error("lark send card failed", "recipient", recipient, "error", err)
			lastErr = err
			continue
		}
		slog.Info("lark card sent", "recipient", recipient)
	}
	return lastErr
}

// ParseCallback 验证并解析飞书回调 payload → Callback。
//
// 飞书 URL verification 流程：
//  1. 首次配置时 POST body 含 {"challenge": "xxx"}，原样回写
//  2. 加密事件需用 EncryptKey 解密（这里简化为仅支持明文 + Token 校验）
func (c *LarkBotChannel) ParseCallback(ctx context.Context, payload []byte) (*Callback, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("notification: lark parse callback: %w", err)
	}

	// URL 校验
	if challenge, ok := raw["challenge"].(string); ok {
		return &Callback{
			ID:        challenge,
			Action:    "url_verification",
			Data:      map[string]any{"challenge": challenge},
			Timestamp: time.Now(),
		}, nil
	}

	// Token 校验（生产环境建议强制）
	if c.config.VerificationToken != "" {
		if token, _ := raw["token"].(string); token != c.config.VerificationToken {
			return nil, fmt.Errorf("notification: lark callback token mismatch")
		}
	}

	header, _ := raw["header"].(map[string]any)
	eventType, _ := header["event_type"].(string)
	if eventType != "card.action.trigger" && eventType != "card.action.trigger_v1" {
		return nil, fmt.Errorf("notification: lark unsupported event_type %q", eventType)
	}

	event, _ := raw["event"].(map[string]any)
	action, _ := event["action"].(map[string]any)
	operator, _ := event["operator"].(map[string]any)

	cb := &Callback{
		Action:    stringFromAny(action["action_id"]),
		Data:      mapFromAny(action["value"]),
		Timestamp: time.Now(),
	}
	if user, ok := operator["user_id"].(string); ok {
		cb.User.OpenID = user
	}
	if name, ok := operator["user_name"].(string); ok {
		cb.User.Name = name
	}
	if tenant, ok := event["tenant_key"].(string); ok {
		cb.TenantID = tenant
	}
	return cb, nil
}

// HealthCheck 检查飞书 API 可达性。
func (c *LarkBotChannel) HealthCheck(ctx context.Context) error {
	return c.ensureAccessToken(ctx)
}

// ensureAccessToken 保证 token 有效；过期前 5 分钟主动刷新。
func (c *LarkBotChannel) ensureAccessToken(ctx context.Context) error {
	c.tokenMu.RLock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpire) {
		c.tokenMu.RUnlock()
		return nil
	}
	c.tokenMu.RUnlock()

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpire) {
		return nil
	}
	return c.refreshAccessToken(ctx)
}

func (c *LarkBotChannel) refreshAccessToken(ctx context.Context) error {
	url := c.config.BaseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	body, _ := json.Marshal(map[string]string{
		"app_id":     c.config.AppID,
		"app_secret": c.config.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notification: lark new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notification: lark token http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("notification: lark token status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("notification: lark token decode: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("notification: lark token api: %s (code %d)", result.Msg, result.Code)
	}

	c.accessToken = result.TenantAccessToken
	c.tokenExpire = time.Now().Add(time.Duration(result.Expire-300) * time.Second)
	slog.Info("lark access token refreshed", "expire_at", c.tokenExpire)
	return nil
}

func (c *LarkBotChannel) sendJSON(ctx context.Context, path string, body map[string]any) error {
	url := c.config.BaseURL + path
	bs, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notification: lark marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("notification: lark new req: %w", err)
	}

	c.tokenMu.RLock()
	token := c.accessToken
	c.tokenMu.RUnlock()

	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notification: lark http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("notification: lark status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("notification: lark decode: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("notification: lark api: %s (code %d)", result.Msg, result.Code)
	}
	return nil
}

// convertToLarkCard 将通用 InteractiveCard 转为飞书卡片 JSON。
func (c *LarkBotChannel) convertToLarkCard(card *InteractiveCard) map[string]any {
	out := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
	}

	if card.Header.Title != "" {
		out["header"] = map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": card.Header.Title,
			},
			"template": card.Header.Template,
		}
	}

	elements := make([]map[string]any, 0, len(card.Elements))
	for _, e := range card.Elements {
		switch e.Type {
		case ElementTypeText:
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": e.Text,
				},
			})
		case ElementTypeField:
			fields := make([]map[string]any, 0, len(e.Fields))
			for _, f := range e.Fields {
				fields = append(fields, map[string]any{
					"is_short": f.Short,
					"text": map[string]any{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**%s**\n%s", f.Key, f.Value),
					},
				})
			}
			elements = append(elements, map[string]any{"tag": "div", "fields": fields})
		case ElementTypeDivider:
			elements = append(elements, map[string]any{"tag": "hr"})
		case ElementTypeNote:
			elements = append(elements, map[string]any{
				"tag": "note",
				"elements": []map[string]any{
					{"tag": "plain_text", "content": e.Text},
				},
			})
		case ElementTypeImage:
			if e.ImageURL != "" {
				img := map[string]any{
					"tag":     "img",
					"img_key": e.ImageURL,
				}
				if e.Alt != "" {
					img["alt"] = map[string]any{"tag": "plain_text", "content": e.Alt}
				}
				elements = append(elements, img)
			}
		}
	}

	if len(card.Actions) > 0 {
		buttons := make([]map[string]any, 0, len(card.Actions))
		for _, a := range card.Actions {
			btnType := "default"
			switch a.Style {
			case "primary":
				btnType = "primary"
			case "danger":
				btnType = "danger"
			}
			buttons = append(buttons, map[string]any{
				"tag": "button",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": a.Text,
				},
				"type":  btnType,
				"value": a.Value,
			})
		}
		elements = append(elements, map[string]any{
			"tag":     "action",
			"actions": buttons,
		})
	}

	if len(elements) > 0 {
		out["elements"] = elements
	}
	return out
}

// VerifyLarkSignature 校验飞书加密回调签名（用于 HTTP handler 入口）。
//
// 飞书 v2 签名算法：
//
//	timestamp + nonce + encrypt_key + body → SHA256 → hex
func VerifyLarkSignature(timestamp, nonce, body, signature, encryptKey string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(encryptKey))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(nonce))
	mac.Write([]byte(body))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// 辅助函数

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
