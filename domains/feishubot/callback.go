package feishubot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CallbackHandler 处理飞书卡片回调。
//
// 责任：
//  1. 签名校验（HMAC + 时间戳防重放）
//  2. 白名单检查
//  3. 命令分派到 CommandRouter
//  4. 回复卡片（SendCard）
//
// 不负责：
//   - 业务层审批执行（由 ApprovalNotifier 处理，本 handler 仅转发命令）
//   - 飞书 API 细节（由 LarkChannel 实现）
type CallbackHandler struct {
	plugin  *Plugin
	router  *CommandRouter
	channel LarkChannel
}

// NewCallbackHandler 构造回调处理器。
func NewCallbackHandler(p *Plugin, r *CommandRouter, ch LarkChannel) *CallbackHandler {
	return &CallbackHandler{plugin: p, router: r, channel: ch}
}

// FeishuCallback 飞书事件回调结构（精简版）。
type FeishuCallback struct {
	Type      string       `json:"type"` // url_verification / event_callback
	Challenge string       `json:"challenge"`
	Token     string       `json:"token"`
	Timestamp string       `json:"timestamp"`
	Nonce     string       `json:"nonce"`
	Signature string       `json:"signature"` // 明文签名（HTTP header 也可能含）
	Event     *FeishuEvent `json:"event"`
}

// FeishuEvent 事件数据。
type FeishuEvent struct {
	Type     string          `json:"type"` // card.action.trigger 等
	UserID   string          `json:"user_id"`
	Action   *FeishuAction   `json:"action"`
	Operator *FeishuOperator `json:"operator"`
}

// FeishuAction 按钮动作。
type FeishuAction struct {
	ActionID string         `json:"action_id"`
	Value    map[string]any `json:"value"`
	Tag      string         `json:"tag"`
}

// FeishuOperator 操作人。
type FeishuOperator struct {
	UserID   string `json:"user_id"`
	OpenID   string `json:"open_id"`
	UserName string `json:"user_name"`
}

// Handle 处理 HTTP POST /api/webhooks/feishu/callback
func (h *CallbackHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	cfg := h.plugin.Snapshot()
	if !cfg.Enabled {
		slog.Warn("feishu_bot: callback received but module disabled")
		http.Error(w, "module disabled", http.StatusServiceUnavailable)
		return
	}

	var cb FeishuCallback
	if err := json.Unmarshal(body, &cb); err != nil {
		slog.Warn("feishu_bot: invalid callback json", "error", err, "body", string(body))
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// 1. URL 验证（首次配置）
	if cb.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": cb.Challenge})
		return
	}

	// 2. 签名校验
	timestamp := firstNonEmpty(cb.Timestamp, r.Header.Get("X-Lark-Request-Timestamp"))
	nonce := firstNonEmpty(cb.Nonce, r.Header.Get("X-Lark-Request-Nonce"))
	signature := firstNonEmpty(cb.Signature, r.Header.Get("X-Lark-Signature"), r.Header.Get("X-Feishu-Signature"))

	if cfg.SignatureRequired {
		if !VerifyLarkTimestamp(timestamp, cfg.TimestampWindowSeconds) {
			slog.Warn("feishu_bot: callback timestamp out of window",
				"timestamp", timestamp, "window_sec", cfg.TimestampWindowSeconds)
			http.Error(w, "timestamp out of window", http.StatusUnauthorized)
			return
		}
		if cfg.EncryptKey != "" && !VerifyLarkSignature(timestamp, nonce, string(body), signature, cfg.EncryptKey) {
			slog.Warn("feishu_bot: callback signature mismatch")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		// Token 校验（基础层）
		if cfg.VerifyToken != "" && cb.Token != "" && cb.Token != cfg.VerifyToken {
			slog.Warn("feishu_bot: callback token mismatch")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	}

	// 3. 仅处理 card.action.trigger
	if cb.Event == nil || cb.Event.Type != "card.action.trigger" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	// 4. 命令分派
	cmd := h.parseCommand(cb)
	if cmd.Action == "" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "no_action"})
		return
	}

	// 命令开关
	if !cfg.CommandsEnabled {
		slog.Info("feishu_bot: commands disabled, ignoring", "action", cmd.Action)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "commands_disabled"})
		return
	}

	card, allowed, err := h.router.Handle(r.Context(), cmd, cfg.CommandsAdminOnly)
	if err != nil {
		slog.Error("feishu_bot: command handler error", "action", cmd.Action, "error", err)
		http.Error(w, "command error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		// 白名单拒绝：返回 200 但不推送任何回复（不暴露命令存在）
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	if card == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// 5. 推送回复卡片给触发用户
	card.Metadata = ensureRecipients(card.Metadata, cmd.UserID)
	cctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := h.channel.SendCard(cctx, card); err != nil {
		slog.Error("feishu_bot: reply card send failed", "error", err)
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"action": cmd.Action,
	})
}

// parseCommand 从回调构造 Command。
func (h *CallbackHandler) parseCommand(cb FeishuCallback) Command {
	cmd := Command{Raw: map[string]any{}}
	if cb.Event == nil {
		return cmd
	}
	if cb.Event.Operator != nil {
		cmd.UserID = firstNonEmpty(cb.Event.Operator.OpenID, cb.Event.Operator.UserID)
		cmd.UserName = cb.Event.Operator.UserName
	}
	if cb.Event.Action != nil {
		cmd.Action = ParseCommandAction(cb.Event.Action.Value)
		// 透传 Value（便于子命令解析）
		for k, v := range cb.Event.Action.Value {
			cmd.Raw[k] = v
		}
		cmd.Raw["action_id"] = cb.Event.Action.ActionID
	}
	if cmd.UserID == "" && cb.Event.UserID != "" {
		cmd.UserID = cb.Event.UserID
	}
	return cmd
}

// ensureRecipients 确保卡片 metadata 包含 recipients 字段。
func ensureRecipients(m map[string]any, userID string) map[string]any {
	if m == nil {
		m = make(map[string]any)
	}
	if _, ok := m["recipients"]; !ok {
		m["recipients"] = []string{userID}
	}
	return m
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ── Settings → WhitelistChecker 适配器 ──────────────────────────────

// CfgWhitelist 把 Config 适配为 WhitelistChecker。
type CfgWhitelist struct {
	Cfg *Config
}

// IsAllowed 实现 WhitelistChecker。
func (c *CfgWhitelist) IsAllowed(openID string) bool {
	if c.Cfg == nil {
		return false
	}
	return c.Cfg.IsUserAllowed(openID)
}

// Compile-time assertion.
var _ http.Handler = (*callbackHTTPAdapter)(nil)

// callbackHTTPAdapter 让 http.Handler 接口通过方法适配暴露（便于 main.go 注册）。
type callbackHTTPAdapter struct{ h *CallbackHandler }

func (a *callbackHTTPAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.h.Handle(w, r)
}

// AsHTTPHandler 把 CallbackHandler 暴露为 http.Handler（用于 main.go mux.Handle）。
func (h *CallbackHandler) AsHTTPHandler() http.Handler {
	return &callbackHTTPAdapter{h: h}
}

// Compile-time guard: avoid unused fmt import warning if all branches optimized out.
var _ = fmt.Sprintf
