// Package notification — callback_handler.go
//
// 渠道回调 HTTP 处理器：
//   - 按 URL path 选择对应渠道解析回调（/webhooks/lark, /webhooks/dingtalk, /webhooks/wechat）
//   - 解析后委托 ApprovalNotifier.HandleApprovalCallback 处理业务
//   - 响应策略：URL 校验 challenge → 原样回写；业务错误 → 200 + 业务 success=false（避免渠道重试风暴）
//
// 之所以不直接用 echo.HandlerFunc：
//   - 调用方（cmd/gateway）可能用 echo / gin / net.http
//   - 把 Handle 暴露成 `func(http.ResponseWriter, *http.Request)` 即可适配任意框架
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CallbackHandler HTTP 入口聚合。
type CallbackHandler struct {
	notifier *ApprovalNotifier
	channels map[ChannelType]NotificationChannel
	timeout  time.Duration
}

// NewCallbackHandler 创建回调处理器。
func NewCallbackHandler(notifier *ApprovalNotifier, channels map[ChannelType]NotificationChannel) *CallbackHandler {
	if channels == nil && notifier != nil {
		channels = notifier.cfg.Channels
	}
	return &CallbackHandler{
		notifier: notifier,
		channels: channels,
		timeout:  30 * time.Second,
	}
}

// ServeHTTP 处理任意渠道的回调。
//
// URL path 用最后一段作为渠道名（兼容 /webhooks/lark 与 /lark 两种前缀）。
func (h *CallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chType, ok := h.channelFromPath(r.URL.Path)
	if !ok {
		http.Error(w, "unknown channel", http.StatusNotFound)
		return
	}
	ch, ok := h.channels[chType]
	if !ok {
		http.Error(w, fmt.Sprintf("channel %q not registered", chType), http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	callback, err := ch.ParseCallback(ctx, body)
	if err != nil {
		slog.Error("callback parse failed", "channel", chType, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// URL 校验（飞书）
	if challenge, ok := callback.Data["challenge"].(string); ok && callback.Action == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
		return
	}

	if h.notifier == nil {
		http.Error(w, "no notifier", http.StatusInternalServerError)
		return
	}

	if err := h.notifier.HandleApprovalCallback(ctx, callback); err != nil {
		slog.Error("approval callback failed",
			"channel", chType,
			"approval_id", callback.Data["approval_id"],
			"error", err)
		// 业务错误：返回 200 但 success=false，避免渠道重试风暴
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (h *CallbackHandler) channelFromPath(path string) (ChannelType, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 {
		return "", false
	}
	last := segments[len(segments)-1]
	switch ChannelType(last) {
	case ChannelLark, ChannelDingTalk, ChannelWeChat:
		return ChannelType(last), true
	}
	return "", false
}

// ErrNoNotifier 没注入 notifier 时的错误（用于单元测试断言）。
var ErrNoNotifier = errors.New("notification: notifier not configured")
