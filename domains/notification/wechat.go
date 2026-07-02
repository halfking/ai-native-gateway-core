// Package notification — wechat.go
//
// 企业微信（WeCom）通知渠道实现。
//
// 支持：
//   - 应用消息（text/markdown/textcard）通过 access_token 推送
//   - 群机器人 Webhook（可选）
//   - 回调解析（企业微信事件订阅 + 加密 XML）
//
// 凭证策略：
//   - CorpID / AgentID / CorpSecret 都不进入日志
//   - 加密回调使用 EncodingAESKey 解密（v1 协议）
package notification

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// WeChatConfig 企业微信配置。
type WeChatConfig struct {
	CorpID         string // 企业 CorpID
	AgentID        string // 应用 AgentID
	CorpSecret     string // 应用 Secret（敏感）
	EncodingAESKey string // 回调加密 AES Key（敏感，可选）
	Token          string // 回调校验 Token

	WebhookURL string // 可选：群机器人 Webhook

	BaseURL string // 可选，默认 https://qyapi.weixin.qq.com
}

// WeChatChannel 企业微信通知渠道。
type WeChatChannel struct {
	config      WeChatConfig
	httpClient  *http.Client
	tokenMu     sync.RWMutex
	accessToken string
	tokenExpire time.Time
}

// NewWeChatChannel 创建企业微信渠道。
func NewWeChatChannel(config WeChatConfig) *WeChatChannel {
	if config.BaseURL == "" {
		config.BaseURL = "https://qyapi.weixin.qq.com"
	}
	return &WeChatChannel{
		config:     config,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name 返回渠道标识。
func (c *WeChatChannel) Name() string {
	return string(ChannelWeChat)
}

// Send 发送文本/markdown 应用消息。
func (c *WeChatChannel) Send(ctx context.Context, msg *Message) error {
	if c.config.WebhookURL != "" {
		return c.sendWebhook(ctx, msg)
	}
	return c.sendAppMessage(ctx, msg)
}

// SendCard 发送 textcard 模板卡片。
func (c *WeChatChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	if c.config.WebhookURL != "" {
		return c.sendWebhookMarkdown(ctx, card)
	}

	recipients, _ := card.Metadata["recipients"].([]string)
	touser := joinStrings(recipients, "|")
	if touser == "" {
		touser = "@all"
	}

	body := map[string]any{
		"touser":  touser,
		"msgtype": "textcard",
		"agentid": c.config.AgentID,
		"textcard": map[string]any{
			"title":       card.Header.Title,
			"description": buildCardText(card),
			"url":         stringFromAny(card.Metadata["detail_url"]),
			"btntxt":      "查看详情",
		},
	}
	return c.postApp(ctx, "/cgi-bin/message/send", body)
}

// ParseCallback 解析企业微信回调（XML + 加密）。
//
// 如果配置了 EncodingAESKey 则尝试解密，否则按明文 JSON 处理（用于业务自定义回调）。
func (c *WeChatChannel) ParseCallback(ctx context.Context, raw []byte) (*Callback, error) {
	decoded, err := c.decryptCallback(raw)
	if err != nil {
		return nil, fmt.Errorf("notification: wechat decrypt: %w", err)
	}

	if looksLikeXML(decoded) {
		var env struct {
			XMLName      xml.Name `xml:"xml"`
			ToUserName   string   `xml:"ToUserName"`
			FromUserName string   `xml:"FromUserName"`
			CreateTime   int64    `xml:"CreateTime"`
			MsgType      string   `xml:"MsgType"`
			Event        string   `xml:"Event"`
			EventKey     string   `xml:"EventKey"`
		}
		if err := xml.Unmarshal(decoded, &env); err != nil {
			return nil, fmt.Errorf("notification: wechat xml parse: %w", err)
		}
		return &Callback{
			Action:    env.EventKey,
			Data:      map[string]any{"event": env.Event, "msg_type": env.MsgType},
			User:      CallbackUser{ID: env.FromUserName, OpenID: env.FromUserName, Name: env.FromUserName},
			TenantID:  env.ToUserName,
			Timestamp: time.Unix(env.CreateTime, 0),
		}, nil
	}

	// Fallback: JSON
	var data map[string]any
	if err := json.Unmarshal(decoded, &data); err != nil {
		return nil, fmt.Errorf("notification: wechat json parse: %w", err)
	}
	cb := &Callback{
		Action:    stringFromAny(data["action"]),
		Data:      data,
		Timestamp: time.Now(),
	}
	if v, ok := data["user_id"].(string); ok {
		cb.User.ID = v
		cb.User.OpenID = v
	}
	if v, ok := data["user_name"].(string); ok {
		cb.User.Name = v
	}
	return cb, nil
}

// HealthCheck 检查 access_token 可获取。
func (c *WeChatChannel) HealthCheck(ctx context.Context) error {
	if c.config.WebhookURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.WebhookURL, nil)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("notification: wechat webhook health: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("notification: wechat webhook unhealthy %d", resp.StatusCode)
		}
		return nil
	}
	_, err := c.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("notification: wechat health: %w", err)
	}
	return nil
}

func (c *WeChatChannel) sendWebhook(ctx context.Context, msg *Message) error {
	body := map[string]any{
		"msgtype": "text",
		"text": map[string]any{
			"content": fmt.Sprintf("%s\n\n%s", msg.Title, msg.Content),
			// 群机器人 @ user via mentioned_list
		},
	}
	if len(msg.Recipients) > 0 {
		body["text"].(map[string]any)["mentioned_list"] = msg.Recipients
	}
	return c.postRaw(ctx, c.config.WebhookURL, body)
}

func (c *WeChatChannel) sendWebhookMarkdown(ctx context.Context, card *InteractiveCard) error {
	body := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"content": buildCardText(card),
		},
	}
	return c.postRaw(ctx, c.config.WebhookURL, body)
}

func (c *WeChatChannel) sendAppMessage(ctx context.Context, msg *Message) error {
	touser := joinStrings(msg.Recipients, "|")
	if touser == "" {
		touser = "@all"
	}
	body := map[string]any{
		"touser":   touser,
		"msgtype":  "markdown",
		"agentid":  c.config.AgentID,
		"markdown": map[string]any{"content": fmt.Sprintf("# %s\n\n%s", msg.Title, msg.Content)},
	}
	return c.postApp(ctx, "/cgi-bin/message/send", body)
}

func (c *WeChatChannel) postApp(ctx context.Context, path string, body map[string]any) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s%s?access_token=%s", c.config.BaseURL, path, token)
	return c.postRaw(ctx, url, body)
}

func (c *WeChatChannel) postRaw(ctx context.Context, url string, body map[string]any) error {
	bs, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notification: wechat marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("notification: wechat new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notification: wechat http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("notification: wechat status %d: %s", resp.StatusCode, string(raw))
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("notification: wechat decode: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("notification: wechat api: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	return nil
}

func (c *WeChatChannel) getAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.RLock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpire) {
		tok := c.accessToken
		c.tokenMu.RUnlock()
		return tok, nil
	}
	c.tokenMu.RUnlock()

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpire) {
		return c.accessToken, nil
	}

	url := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		c.config.BaseURL, c.config.CorpID, c.config.CorpSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("notification: wechat new req: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("notification: wechat token http: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("notification: wechat token decode: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("notification: wechat token api: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	c.accessToken = result.AccessToken
	c.tokenExpire = time.Now().Add(time.Duration(result.ExpiresIn-300) * time.Second)
	slog.Debug("wechat access token refreshed", "expire_at", c.tokenExpire)
	return c.accessToken, nil
}

// decryptCallback 解密企业微信加密回调（如果配置了 EncodingAESKey）。
func (c *WeChatChannel) decryptCallback(raw []byte) ([]byte, error) {
	if c.config.EncodingAESKey == "" {
		return raw, nil
	}

	var env struct {
		XMLName      xml.Name `xml:"xml"`
		Encrypt      string   `xml:"Encrypt"`
		MsgSignature string   `xml:"MsgSignature"`
		TimeStamp    string   `xml:"TimeStamp"`
		Nonce        string   `xml:"Nonce"`
	}
	if err := xml.Unmarshal(raw, &env); err != nil {
		// 不是 XML，直接返回
		return raw, nil
	}
	if env.Encrypt == "" {
		return raw, nil
	}

	if c.config.Token != "" && env.MsgSignature != "" {
		if !verifyWeChatSignature(c.config.Token, env.TimeStamp, env.Nonce, env.Encrypt, env.MsgSignature) {
			return nil, fmt.Errorf("notification: wechat signature invalid")
		}
	}

	cipherText, err := base64.StdEncoding.DecodeString(env.Encrypt)
	if err != nil {
		return nil, fmt.Errorf("notification: wechat base64: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(c.config.EncodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("notification: wechat aes key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("notification: wechat aes key length %d, want 32", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("notification: wechat aes: %w", err)
	}
	if len(cipherText) < block.BlockSize() || len(cipherText)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("notification: wechat ciphertext length invalid")
	}
	iv := cipherText[:block.BlockSize()]
	cipherText = cipherText[block.BlockSize():]

	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherText))
	mode.CryptBlocks(plain, cipherText)

	// PKCS#7 unpad
	padLen := int(plain[len(plain)-1])
	if padLen < 1 || padLen > block.BlockSize() {
		return nil, fmt.Errorf("notification: wechat pkcs7 padding invalid")
	}
	for i := len(plain) - padLen; i < len(plain); i++ {
		if plain[i] != byte(padLen) {
			return nil, fmt.Errorf("notification: wechat pkcs7 padding inconsistent")
		}
	}
	plain = plain[:len(plain)-padLen]

	// 前 16 字节随机串 + 4 字节长度（网络序）+ content + corpID
	if len(plain) < 20 {
		return nil, fmt.Errorf("notification: wechat plain too short")
	}
	contentLen := binary.BigEndian.Uint32(plain[16:20])
	if int(contentLen) < 0 || int(contentLen) > len(plain)-20 {
		return nil, fmt.Errorf("notification: wechat content length invalid")
	}
	return plain[20 : 20+contentLen], nil
}

func verifyWeChatSignature(token, timestamp, nonce, encrypt, signature string) bool {
	parts := []string{token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:]) == signature
}

func looksLikeXML(b []byte) bool {
	s := bytes.TrimSpace(b)
	return len(s) > 0 && s[0] == '<'
}
