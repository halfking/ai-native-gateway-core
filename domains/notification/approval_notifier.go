// Package notification — approval_notifier.go
//
// 增强版审批通知器：
//   - 支持多渠道（飞书/钉钉/企业微信）路由下发
//   - 卡片整合会话总结（来自 SummarySource 接口）与敏感信息
//   - 审批回调驱动 ApprovalManager.Approve / Reject
//   - 失败降级：单个渠道失败不影响其他渠道
//
// 设计取舍：
//   - 与 sessionsummary 解耦：通过 SummarySource 接口注入，便于测试
//   - 与 sessionaudit 解耦：依赖最小公共类型（ApprovalRecord, DetectResult）
//   - 超时默认 30s，符合技术约束
package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// SummarySource 提供会话总结视图（来自 sessionsummary.Summarizer）。
//
// 接口方式注入避免 notification 包反向依赖 sessionsummary 全部类型。
// 当返回 error 或 view==nil 时，审批卡片将不展示会话总结。
type SummarySource interface {
	GetSummaryView(ctx context.Context, tenantID, sessionKey string) (*SessionSummaryView, error)
}

// SessionResumer 审批通过后通知业务层恢复会话（与 Task B 协同）。
//
// 失败应被记录但不应阻止回调返回 200（避免渠道重复推送）。
type SessionResumer interface {
	ResumeAfterApproval(ctx context.Context, tenantID, sessionID string) error
}

// NotifierConfig 构造参数。
type NotifierConfig struct {
	Channels       map[ChannelType]NotificationChannel // 多渠道
	DefaultChannel ChannelType                         // 兜底渠道（路由未指定时）
	Routing        *ApprovalRoutingTable
	Summary        SummarySource  // 可选，nil 时卡片不含总结
	Resumer        SessionResumer // 可选，nil 时不触发恢复
	ApprovalMgr    ApprovalStore  // 通过接口注入（*sessionaudit.ApprovalManager 已实现）
	Timeout        time.Duration  // 单次下发超时，默认 30s
}

// ApprovalStore 审批存储抽象（避免 notifier 直接依赖 *sessionaudit.ApprovalManager 指针）。
//
// *sessionaudit.ApprovalManager 天然实现该接口；测试用 mock 也实现它。
type ApprovalStore interface {
	Approve(ctx context.Context, approvalID, tenantID, user, reason string) error
	Reject(ctx context.Context, approvalID, tenantID, user, reason string) error
	GetForTenant(ctx context.Context, approvalID, expectedTenantID string) (*sessionaudit.ApprovalRecord, error)
}

// ApprovalNotifier 审批通知器（增强版）。
type ApprovalNotifier struct {
	cfg NotifierConfig
}

// NewApprovalNotifier 创建审批通知器。
//
// 任一必填字段缺失返回 error，便于 main.go 启动期 fail-fast。
func NewApprovalNotifier(cfg NotifierConfig) (*ApprovalNotifier, error) {
	if cfg.ApprovalMgr == nil {
		return nil, errors.New("notification: ApprovalMgr required")
	}
	if cfg.Routing == nil {
		cfg.Routing = NewEmptyRoutingTable()
	}
	if len(cfg.Channels) == 0 {
		return nil, errors.New("notification: at least one channel required")
	}
	if cfg.DefaultChannel == "" {
		cfg.DefaultChannel = ChannelLark
	}
	if _, ok := cfg.Channels[cfg.DefaultChannel]; !ok {
		return nil, fmt.Errorf("notification: default channel %q not registered", cfg.DefaultChannel)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &ApprovalNotifier{cfg: cfg}, nil
}

// RegisterChannel 动态注册渠道（用于热更新）。
func (n *ApprovalNotifier) RegisterChannel(t ChannelType, ch NotificationChannel) {
	if n.cfg.Channels == nil {
		n.cfg.Channels = make(map[ChannelType]NotificationChannel)
	}
	n.cfg.Channels[t] = ch
}

// NotifyApproval 发送审批通知（增强版：含会话总结 + 敏感信息）。
//
// 返回的 error 表示至少有一个渠道失败；所有渠道都失败时聚合错误。
// 单个接收人失败不阻塞其他接收人。
func (n *ApprovalNotifier) NotifyApproval(ctx context.Context, record *sessionaudit.ApprovalRecord) error {
	if record == nil {
		return errors.New("notification: nil approval record")
	}
	if record.DetectResult == nil {
		return errors.New("notification: approval record missing DetectResult")
	}

	risk := RiskLevelFromScore(record.DetectResult.Score)

	// 1. 查会话总结（可选）
	var summary *SessionSummaryView
	if n.cfg.Summary != nil {
		s, err := n.cfg.Summary.GetSummaryView(ctx, record.TenantID, record.SessionID)
		if err != nil {
			slog.Warn("get session summary failed (non-fatal)",
				"tenant_id", record.TenantID, "session_id", record.SessionID, "error", err)
		} else {
			summary = s
		}
	}

	// 2. 路由到接收人
	approvers := n.cfg.Routing.Route(record.TenantID, risk)
	if len(approvers) == 0 {
		slog.Warn("no approvers found",
			"tenant_id", record.TenantID,
			"risk_level", risk,
			"session_id", record.SessionID)
		return fmt.Errorf("notification: no approvers for tenant %s risk %s", record.TenantID, risk)
	}

	// 3. 按渠道分组接收人
	channelToRecipients := groupRecipientsByChannel(approvers)

	// 4. 构造卡片
	card := &ApprovalCard{
		SessionID:      record.SessionID,
		TenantID:       record.TenantID,
		RequestID:      record.RequestID,
		ApprovalID:     record.ID,
		RiskLevel:      string(risk),
		DetectResult:   record.DetectResult,
		Snapshot:       record.Snapshot,
		SessionSummary: summary,
		Recipients:     approvers,
		Actions:        n.buildApprovalActions(record),
		CreatedAt:      time.Now(),
	}

	// 5. 多渠道下发
	return n.sendToChannels(ctx, card, channelToRecipients)
}

// sendToChannels 按 channel 分发，单渠道失败记录但不阻塞其他渠道。
func (n *ApprovalNotifier) sendToChannels(ctx context.Context, card *ApprovalCard, groups map[ChannelType][]string) error {
	if len(groups) == 0 {
		// 兜底用 default channel + 所有 LarkOpenID
		larkIDs := make([]string, 0, len(card.Recipients))
		for _, r := range card.Recipients {
			if r.LarkOpenID != "" {
				larkIDs = append(larkIDs, r.LarkOpenID)
			}
		}
		if len(larkIDs) == 0 {
			return errors.New("notification: no valid recipients for default channel")
		}
		groups = map[ChannelType][]string{n.cfg.DefaultChannel: larkIDs}
	}

	interactive := card.ToInteractiveCard()
	if interactive.Metadata == nil {
		interactive.Metadata = make(map[string]any)
	}

	var errs []error
	for chType, recipients := range groups {
		ch, ok := n.cfg.Channels[chType]
		if !ok {
			slog.Warn("channel not registered, skipping",
				"channel", chType, "recipients", len(recipients))
			continue
		}
		if len(recipients) == 0 {
			continue
		}

		perMeta := copyMetadata(interactive.Metadata)
		perMeta["recipients"] = recipients
		perMeta["channel"] = string(chType)

		cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
		err := ch.SendCard(cctx, withMeta(interactive, perMeta))
		cancel()

		if err != nil {
			slog.Error("approval notification send failed",
				"channel", chType,
				"approval_id", card.ApprovalID,
				"recipients", len(recipients),
				"error", err)
			errs = append(errs, fmt.Errorf("%s: %w", chType, err))
			continue
		}
		slog.Info("approval notification sent",
			"channel", chType,
			"approval_id", card.ApprovalID,
			"session_id", card.SessionID,
			"recipients", len(recipients),
			"risk_level", card.RiskLevel,
			"has_summary", card.SessionSummary != nil && card.SessionSummary.HasSummary)
	}

	if len(errs) == len(groups) && len(groups) > 0 {
		return fmt.Errorf("notification: all channels failed: %w", joinErrors(errs))
	}
	if len(errs) > 0 {
		slog.Warn("partial channel failure",
			"approval_id", card.ApprovalID,
			"failed", len(errs),
			"total", len(groups))
	}
	return nil
}

// HandleApprovalCallback 处理审批回调（来自任意渠道）。
func (n *ApprovalNotifier) HandleApprovalCallback(ctx context.Context, cb *Callback) error {
	if cb == nil {
		return errors.New("notification: nil callback")
	}
	approvalID, ok := cb.Data["approval_id"].(string)
	if !ok || approvalID == "" {
		return errors.New("notification: approval_id missing in callback data")
	}

	userID := cb.User.OpenID
	if userID == "" {
		userID = cb.User.ID
	}
	userName := cb.User.Name

	slog.Info("handling approval callback",
		"action", cb.Action,
		"approval_id", approvalID,
		"user", userName)

	switch cb.Action {
	case "approve":
		reason := fmt.Sprintf("批准人: %s", userName)
		if r, ok := cb.Data["reason"].(string); ok && r != "" {
			reason += " - " + r
		}
		if err := n.cfg.ApprovalMgr.Approve(ctx, approvalID, cb.TenantID, userID, reason); err != nil {
			return fmt.Errorf("notification: approve: %w", err)
		}
		n.sendConfirmation(ctx, cb.User, "审批已批准", "✅ 您已批准此审批请求")
		n.triggerResume(ctx, cb.TenantID, cb.SessionID)
		return nil

	case "reject":
		reason := fmt.Sprintf("拒绝人: %s", userName)
		if r, ok := cb.Data["reason"].(string); ok && r != "" {
			reason += " - " + r
		}
		if err := n.cfg.ApprovalMgr.Reject(ctx, approvalID, cb.TenantID, userID, reason); err != nil {
			return fmt.Errorf("notification: reject: %w", err)
		}
		n.sendConfirmation(ctx, cb.User, "审批已拒绝", "❌ 您已拒绝此审批请求")
		return nil

	case "detail":
		return n.sendDetailCard(ctx, approvalID, cb.User)

	default:
		return fmt.Errorf("notification: unknown action %q", cb.Action)
	}
}

// buildApprovalActions 构造统一的审批按钮。
func (n *ApprovalNotifier) buildApprovalActions(record *sessionaudit.ApprovalRecord) []CardAction {
	return []CardAction{
		{
			ID:    "approve",
			Text:  "✅ 批准",
			Style: "primary",
			Value: map[string]any{
				"approval_id": record.ID,
				"session_id":  record.SessionID,
				"tenant_id":   record.TenantID,
				"action":      "approve",
			},
		},
		{
			ID:    "reject",
			Text:  "❌ 拒绝",
			Style: "danger",
			Value: map[string]any{
				"approval_id": record.ID,
				"session_id":  record.SessionID,
				"tenant_id":   record.TenantID,
				"action":      "reject",
			},
		},
		{
			ID:    "detail",
			Text:  "📋 查看详情",
			Style: "default",
			Value: map[string]any{
				"approval_id": record.ID,
				"session_id":  record.SessionID,
				"tenant_id":   record.TenantID,
				"action":      "detail",
			},
		},
	}
}

// sendConfirmation 发送确认回执。
func (n *ApprovalNotifier) sendConfirmation(ctx context.Context, user CallbackUser, title, content string) {
	ch := n.cfg.Channels[n.cfg.DefaultChannel]
	if ch == nil {
		return
	}
	msg := &Message{
		ID:         uuid.New().String(),
		Type:       MessageTypeInfo,
		Priority:   PriorityNormal,
		Title:      title,
		Content:    content,
		Recipients: []string{nonEmpty(user.OpenID, user.ID)},
		CreatedAt:  time.Now(),
	}
	cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	if err := ch.Send(cctx, msg); err != nil {
		slog.Error("send confirmation failed", "user", user.Name, "error", err)
	}
}

// sendDetailCard 发送详细卡片。
func (n *ApprovalNotifier) sendDetailCard(ctx context.Context, approvalID string, user CallbackUser) error {
	record, err := n.cfg.ApprovalMgr.GetForTenant(ctx, approvalID, "")
	if err != nil {
		return fmt.Errorf("notification: get approval: %w", err)
	}

	elements := []CardElement{
		{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "审批ID", Value: record.ID, Short: true},
				{Key: "会话ID", Value: record.SessionID, Short: true},
				{Key: "租户ID", Value: record.TenantID, Short: true},
				{Key: "请求ID", Value: record.RequestID, Short: true},
				{Key: "状态", Value: string(record.Status), Short: true},
				{Key: "创建时间", Value: record.CreatedAt.Format("2006-01-02 15:04:05"), Short: true},
			},
		},
		{Type: ElementTypeDivider},
	}
	if record.DetectResult != nil {
		elements = append(elements, CardElement{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "综合评分", Value: formatScore(record.DetectResult.Score), Short: true},
				{Key: "决策", Value: string(record.DetectResult.Decision), Short: true},
			},
		})
		if len(record.DetectResult.SensitiveWords) > 0 {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "⚠️ 敏感词: " + joinStrings(record.DetectResult.SensitiveWords, ", "),
			})
		}
		if len(record.DetectResult.Threats) > 0 {
			threatsText := ""
			for _, threat := range record.DetectResult.Threats {
				threatsText += fmt.Sprintf("- %s (严重度: %d)\n", threat.Type, threat.Severity)
			}
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "🚨 威胁:\n" + threatsText,
			})
		}
	}
	if record.Snapshot != nil && record.Snapshot.ClientInfo.IP != "" {
		elements = append(elements, CardElement{Type: ElementTypeDivider})
		elements = append(elements, CardElement{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "客户端模型", Value: record.Snapshot.ClientModel, Short: true},
				{Key: "客户端IP", Value: record.Snapshot.ClientInfo.IP, Short: true},
			},
		})
	}

	card := &InteractiveCard{
		Header:   CardHeader{Title: "📋 审批详情", Template: "blue"},
		Elements: elements,
		Metadata: map[string]any{
			"recipients": []string{nonEmpty(user.OpenID, user.ID)},
		},
	}
	ch := n.cfg.Channels[n.cfg.DefaultChannel]
	if ch == nil {
		return fmt.Errorf("notification: no channel to send detail card")
	}
	cctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	return ch.SendCard(cctx, card)
}

// triggerResume 审批通过后通知业务层恢复会话（与 Task B 协同）。
func (n *ApprovalNotifier) triggerResume(ctx context.Context, tenantID, sessionID string) {
	if n.cfg.Resumer == nil || tenantID == "" || sessionID == "" {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	if err := n.cfg.Resumer.ResumeAfterApproval(rctx, tenantID, sessionID); err != nil {
		slog.Error("resume session after approval failed",
			"tenant_id", tenantID, "session_id", sessionID, "error", err)
	}
}

// 辅助函数

func groupRecipientsByChannel(rs []Recipient) map[ChannelType][]string {
	out := make(map[ChannelType][]string)
	for _, r := range rs {
		if r.LarkOpenID != "" {
			out[ChannelLark] = append(out[ChannelLark], r.LarkOpenID)
		}
		if r.DingTalkUserID != "" {
			out[ChannelDingTalk] = append(out[ChannelDingTalk], r.DingTalkUserID)
		}
		if r.WeChatUserID != "" {
			out[ChannelWeChat] = append(out[ChannelWeChat], r.WeChatUserID)
		}
	}
	return out
}

func copyMetadata(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func withMeta(card *InteractiveCard, meta map[string]any) *InteractiveCard {
	cp := *card
	cp.Metadata = meta
	return &cp
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return errors.New(joinStrings(msgs, "; "))
}
