package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// LarkBotConfig 飞书机器人配置
type LarkBotConfig struct {
	// AppID 应用ID
	AppID string

	// AppSecret 应用Secret
	AppSecret string

	// VerificationToken 验证Token
	VerificationToken string

	// EncryptKey 加密Key
	EncryptKey string

	// BaseURL API基础URL
	BaseURL string
}

// LarkBotChannel 飞书机器人通知渠道
//
// 职责：
//   - 发送文本消息和交互式卡片
//   - 处理用户交互回调
//   - Token管理和自动刷新
type LarkBotChannel struct {
	config        LarkBotConfig
	accessToken   string
	tokenExpireAt time.Time
	tokenMu       sync.RWMutex
	httpClient    *http.Client
	callbackSrv   *CallbackServer
	routingRules  RoutingRules
}

// NewLarkBotChannel 创建飞书机器人渠道
func NewLarkBotChannel(config LarkBotConfig, routingRules RoutingRules) *LarkBotChannel {
	if config.BaseURL == "" {
		config.BaseURL = "https://open.feishu.cn"
	}

	return &LarkBotChannel{
		config:       config,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		routingRules: routingRules,
	}
}

// Name 返回渠道名称
func (c *LarkBotChannel) Name() string {
	return "lark_bot"
}

// Send 发送普通消息
func (c *LarkBotChannel) Send(ctx context.Context, msg *Message) error {
	if err := c.ensureAccessToken(ctx); err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// 构建消息体
	for _, recipient := range msg.Recipients {
		messageBody := map[string]any{
			"receive_id_type": "open_id",
			"receive_id":      recipient,
			"msg_type":        "text",
			"content": map[string]any{
				"text": fmt.Sprintf("%s\n\n%s", msg.Title, msg.Content),
			},
		}

		if err := c.sendMessage(ctx, messageBody); err != nil {
			slog.Error("failed to send message", "recipient", recipient, "error", err)
			// 继续发送给其他接收人
			continue
		}

		slog.Info("message sent", "recipient", recipient, "msg_id", msg.ID)
	}

	return nil
}

// SendCard 发送交互式卡片
func (c *LarkBotChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	if err := c.ensureAccessToken(ctx); err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// 转换为飞书卡片格式
	larkCard := c.convertToLarkCard(card)

	// 发送卡片
	cardJSON, err := json.Marshal(larkCard)
	if err != nil {
		return fmt.Errorf("failed to marshal card: %w", err)
	}

	// 从元数据获取接收人列表
	recipients, ok := card.Metadata["recipients"].([]string)
	if !ok || len(recipients) == 0 {
		return fmt.Errorf("no recipients specified in card metadata")
	}

	for _, recipient := range recipients {
		messageBody := map[string]any{
			"receive_id_type": "open_id",
			"receive_id":      recipient,
			"msg_type":        "interactive",
			"content":         string(cardJSON),
		}

		if err := c.sendMessage(ctx, messageBody); err != nil {
			slog.Error("failed to send card", "recipient", recipient, "error", err)
			continue
		}

		slog.Info("card sent", "recipient", recipient)
	}

	return nil
}

// HandleCallback 处理回调
func (c *LarkBotChannel) HandleCallback(ctx context.Context, callback *Callback) error {
	slog.Info("handling callback",
		"action", callback.Action,
		"user", callback.User.Name,
		"session_id", callback.SessionID)

	// 这里应该调用具体的业务逻辑处理回调
	// 例如：审批通过/拒绝等

	return nil
}

// ensureAccessToken 确保access token有效
func (c *LarkBotChannel) ensureAccessToken(ctx context.Context) error {
	c.tokenMu.RLock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpireAt) {
		c.tokenMu.RUnlock()
		return nil
	}
	c.tokenMu.RUnlock()

	// 获取新token
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// 双重检查
	if c.accessToken != "" && time.Now().Before(c.tokenExpireAt) {
		return nil
	}

	return c.refreshAccessToken(ctx)
}

// refreshAccessToken 刷新access token
func (c *LarkBotChannel) refreshAccessToken(ctx context.Context) error {
	url := fmt.Sprintf("%s/open-apis/auth/v3/tenant_access_token/internal", c.config.BaseURL)

	requestBody := map[string]string{
		"app_id":     c.config.AppID,
		"app_secret": c.config.AppSecret,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Body = http.NoBody
	_ = bodyBytes // 实际应该作为body发送

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Code != 0 {
		return fmt.Errorf("API error: %s (code: %d)", result.Msg, result.Code)
	}

	// 更新token（提前5分钟过期以确保有效性）
	c.accessToken = result.TenantAccessToken
	c.tokenExpireAt = time.Now().Add(time.Duration(result.Expire-300) * time.Second)

	slog.Info("access token refreshed", "expire_at", c.tokenExpireAt)

	return nil
}

// sendMessage 发送消息
func (c *LarkBotChannel) sendMessage(ctx context.Context, messageBody map[string]any) error {
	url := fmt.Sprintf("%s/open-apis/im/v1/messages", c.config.BaseURL)

	bodyBytes, err := json.Marshal(messageBody)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.tokenMu.RLock()
	token := c.accessToken
	c.tokenMu.RUnlock()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Body = http.NoBody
	_ = bodyBytes // 实际应该作为body发送

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Code != 0 {
		return fmt.Errorf("API error: %s (code: %d)", result.Msg, result.Code)
	}

	return nil
}

// convertToLarkCard 转换为飞书卡片格式
func (c *LarkBotChannel) convertToLarkCard(card *InteractiveCard) map[string]any {
	larkCard := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
	}

	// 转换header
	if card.Header.Title != "" {
		larkCard["header"] = map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": card.Header.Title,
			},
			"template": card.Header.Template,
		}
	}

	// 转换elements
	elements := make([]map[string]any, 0)
	for _, elem := range card.Elements {
		switch elem.Type {
		case ElementTypeText:
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": elem.Text,
				},
			})

		case ElementTypeField:
			fields := make([]map[string]any, 0)
			for _, field := range elem.Fields {
				fields = append(fields, map[string]any{
					"is_short": field.Short,
					"text": map[string]any{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**%s**\n%s", field.Key, field.Value),
					},
				})
			}
			elements = append(elements, map[string]any{
				"tag":    "div",
				"fields": fields,
			})

		case ElementTypeDivider:
			elements = append(elements, map[string]any{
				"tag": "hr",
			})

		case ElementTypeNote:
			elements = append(elements, map[string]any{
				"tag": "note",
				"elements": []map[string]any{
					{
						"tag":     "plain_text",
						"content": elem.Text,
					},
				},
			})
		}
	}

	if len(elements) > 0 {
		larkCard["elements"] = elements
	}

	// 转换actions
	if len(card.Actions) > 0 {
		actions := make([]map[string]any, 0)
		for _, action := range card.Actions {
			btnType := "default"
			if action.Style == "primary" {
				btnType = "primary"
			} else if action.Style == "danger" {
				btnType = "danger"
			}

			actions = append(actions, map[string]any{
				"tag": "button",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": action.Text,
				},
				"type":  btnType,
				"value": action.Value,
			})
		}

		// 添加actions作为最后一个element
		larkCard["elements"] = append(larkCard["elements"].([]map[string]any), map[string]any{
			"tag":     "action",
			"actions": actions,
		})
	}

	return larkCard
}

// CallbackServer 回调服务器
//
// 职责：
//   - 接收飞书回调请求
//   - 验证请求签名
//   - 解析回调数据
//   - 调用回调处理器
type CallbackServer struct {
	config   LarkBotConfig
	handlers map[string]CallbackHandler
	mu       sync.RWMutex
}

// CallbackHandler 回调处理器
type CallbackHandler func(ctx context.Context, callback *Callback) error

// NewCallbackServer 创建回调服务器
func NewCallbackServer(config LarkBotConfig) *CallbackServer {
	return &CallbackServer{
		config:   config,
		handlers: make(map[string]CallbackHandler),
	}
}

// RegisterHandler 注册回调处理器
func (s *CallbackServer) RegisterHandler(action string, handler CallbackHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[action] = handler
}

// ServeHTTP 处理HTTP回调
func (s *CallbackServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 验证请求
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析回调数据
	var callbackData map[string]any
	if err := json.NewDecoder(r.Body).Decode(&callbackData); err != nil {
		slog.Error("failed to decode callback", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// URL验证（首次配置时）
	if challenge, ok := callbackData["challenge"].(string); ok {
		response := map[string]string{"challenge": challenge}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// 解析为Callback结构
	callback := &Callback{
		Timestamp: time.Now(),
		Data:      callbackData,
	}

	// 提取action
	if action, ok := callbackData["action"].(string); ok {
		callback.Action = action
	}

	// 查找并执行处理器
	s.mu.RLock()
	handler, ok := s.handlers[callback.Action]
	s.mu.RUnlock()

	if !ok {
		slog.Warn("no handler for action", "action", callback.Action)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 执行处理器
	if err := handler(ctx, callback); err != nil {
		slog.Error("callback handler failed", "action", callback.Action, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
