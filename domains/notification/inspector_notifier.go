// Package notification — inspector_notifier.go
//
// Session Inspector 告警通知器。
// 订阅 session_inspector.* 事件，转换为 IM 卡片推送到飞书/企业微信。
//
// 设计：
//   - 与 ApprovalNotifier 并列（职责清晰）
//   - 支持多渠道路由（可扩展 DingTalk/Email）
//   - 失败降级：单渠道失败不影响其他渠道
//
// 接入点：cmd/gateway/main.go 在 gAuditBus.Subscribe() 注册。

package notification

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// InspectorNotifier Session Inspector 告警通知器。
type InspectorNotifier struct {
	channels       map[string]NotificationChannel // 渠道名 → NotificationChannel 实例
	defaultChannel string                         // 默认渠道（当路由失败时使用）
}

// InspectorNotifierConfig 通知器配置。
type InspectorNotifierConfig struct {
	Channels       map[string]NotificationChannel // 必填：至少一个渠道
	DefaultChannel string                         // 必填：默认渠道名（必须在 Channels 中存在）
}

// NewInspectorNotifier 创建 Inspector 告警通知器。
func NewInspectorNotifier(cfg InspectorNotifierConfig) (*InspectorNotifier, error) {
	if len(cfg.Channels) == 0 {
		return nil, fmt.Errorf("inspector_notifier: at least one channel required")
	}
	if cfg.DefaultChannel == "" {
		return nil, fmt.Errorf("inspector_notifier: default_channel required")
	}
	if _, ok := cfg.Channels[cfg.DefaultChannel]; !ok {
		return nil, fmt.Errorf("inspector_notifier: default_channel %q not in channels", cfg.DefaultChannel)
	}
	return &InspectorNotifier{
		channels:       cfg.Channels,
		defaultChannel: cfg.DefaultChannel,
	}, nil
}

// HandleFindingEvent 处理 SessionInspectorFindingEvent（EventBus 订阅回调）。
func (n *InspectorNotifier) HandleFindingEvent(ctx context.Context, event eventbus.Event) error {
	// 类型断言（runtime 保证正确，因为订阅时已指定 Type）
	fe, ok := event.(interface {
		Type() string
		Timestamp() time.Time
		// 暴露字段（避免 import sessioninspector 造成循环依赖，使用 interface）
		GetSessionID() string
		GetTenantID() string
		GetFinding() interface{} // *Finding
		GetSource() string
	})
	if !ok {
		slog.Warn("inspector_notifier: invalid finding event type", "event_type", fmt.Sprintf("%T", event))
		return nil // 吞掉错误，避免阻塞 EventBus
	}

	// 构造卡片（简化版：仅文本消息）
	// 生产环境建议构造 InteractiveCard 以支持按钮（如"查看详情"跳转 admin 页面）
	finding := fe.GetFinding()
	msg := &Message{
		Title: "🔍 会话健康检查告警",
		Content: fmt.Sprintf(
			"会话: %s\n租户: %s\n来源: %s\n检测时间: %s\n\n%+v",
			fe.GetSessionID(),
			fe.GetTenantID(),
			fe.GetSource(),
			fe.Timestamp().Format("2006-01-02 15:04:05"),
			finding, // 简化：直接打印 finding
		),
	}

	// 降级策略：仅推送到默认渠道（可扩展为读取租户路由表）
	ch := n.channels[n.defaultChannel]
	if ch == nil {
		slog.Warn("inspector_notifier: default channel not found", "channel", n.defaultChannel)
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ch.Send(sendCtx, msg); err != nil {
		slog.Warn("inspector_notifier: send finding failed",
			"session_id", fe.GetSessionID(),
			"channel", n.defaultChannel,
			"error", err)
	} else {
		slog.Info("inspector_notifier: finding sent",
			"session_id", fe.GetSessionID(),
			"channel", n.defaultChannel)
	}
	return nil
}

// HandleRecycleEvent 处理 SessionInspectorRecycleEvent（EventBus 订阅回调）。
func (n *InspectorNotifier) HandleRecycleEvent(ctx context.Context, event eventbus.Event) error {
	re, ok := event.(interface {
		Type() string
		Timestamp() time.Time
		GetSessionID() string
		GetTenantID() string
		GetAction() string
		GetReason() string
		GetIdleFor() string
	})
	if !ok {
		slog.Warn("inspector_notifier: invalid recycle event type", "event_type", fmt.Sprintf("%T", event))
		return nil
	}

	msg := &Message{
		Title: "♻️ 会话回收通知",
		Content: fmt.Sprintf(
			"会话: %s\n租户: %s\n动作: %s\n原因: %s\n闲置时长: %s\n时间: %s",
			re.GetSessionID(),
			re.GetTenantID(),
			re.GetAction(),
			re.GetReason(),
			re.GetIdleFor(),
			re.Timestamp().Format("2006-01-02 15:04:05"),
		),
	}

	ch := n.channels[n.defaultChannel]
	if ch == nil {
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ch.Send(sendCtx, msg); err != nil {
		slog.Warn("inspector_notifier: send recycle failed",
			"session_id", re.GetSessionID(),
			"channel", n.defaultChannel,
			"error", err)
	} else {
		slog.Info("inspector_notifier: recycle sent",
			"session_id", re.GetSessionID(),
			"channel", n.defaultChannel)
	}
	return nil
}
