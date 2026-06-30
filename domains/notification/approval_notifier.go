package notification

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// ApprovalNotifier 审批通知器
//
// 职责：
//   - 发送审批通知到飞书
//   - 处理审批回调（批准/拒绝）
//   - 管理审批路由规则
type ApprovalNotifier struct {
	channel      NotificationChannel
	approvalMgr  *sessionaudit.ApprovalManager
	routingTable *ApprovalRoutingTable
}

// NewApprovalNotifier 创建审批通知器
func NewApprovalNotifier(
	channel NotificationChannel,
	approvalMgr *sessionaudit.ApprovalManager,
	routingTable *ApprovalRoutingTable,
) *ApprovalNotifier {
	return &ApprovalNotifier{
		channel:      channel,
		approvalMgr:  approvalMgr,
		routingTable: routingTable,
	}
}

// NotifyApproval 发送审批通知
func (n *ApprovalNotifier) NotifyApproval(ctx context.Context, record *sessionaudit.ApprovalRecord) error {
	// 1. 根据租户/风险级别路由到对应审批人
	approvers := n.routingTable.Route(record.TenantID, riskLevelFromScore(record.DetectResult.Score))
	
	if len(approvers) == 0 {
		slog.Warn("no approvers found", "tenant_id", record.TenantID, "risk_level", riskLevelFromScore(record.DetectResult.Score))
		return fmt.Errorf("no approvers found for tenant %s", record.TenantID)
	}
	
	// 2. 构建审批卡片
	card := &ApprovalCard{
		SessionID:    record.SessionID,
		TenantID:     record.TenantID,
		RequestID:    record.RequestID,
		ApprovalID:   record.ID,
		RiskLevel:    riskLevelFromScore(record.DetectResult.Score),
		DetectResult: record.DetectResult,
		Snapshot:     record.Snapshot,
		Actions: []CardAction{
			{
				ID:    "approve",
				Text:  "✅ 批准",
				Style: "primary",
				Value: map[string]any{
					"approval_id": record.ID,
					"action":      "approve",
				},
			},
			{
				ID:    "reject",
				Text:  "❌ 拒绝",
				Style: "danger",
				Value: map[string]any{
					"approval_id": record.ID,
					"action":      "reject",
				},
			},
			{
				ID:    "detail",
				Text:  "📋 查看详情",
				Style: "default",
				Value: map[string]any{
					"approval_id": record.ID,
					"action":      "detail",
				},
			},
		},
		CreatedAt: time.Now(),
	}
	
	interactiveCard := card.ToInteractiveCard()
	
	// 3. 提取接收人的OpenID
	recipients := make([]string, 0, len(approvers))
	for _, approver := range approvers {
		if approver.LarkOpenID != "" {
			recipients = append(recipients, approver.LarkOpenID)
		}
	}
	
	if len(recipients) == 0 {
		return fmt.Errorf("no valid recipients found")
	}
	
	// 添加接收人到卡片元数据
	if interactiveCard.Metadata == nil {
		interactiveCard.Metadata = make(map[string]any)
	}
	interactiveCard.Metadata["recipients"] = recipients
	
	// 4. 发送卡片
	if err := n.channel.SendCard(ctx, interactiveCard); err != nil {
		return fmt.Errorf("failed to send approval card: %w", err)
	}
	
	slog.Info("approval notification sent",
		"approval_id", record.ID,
		"session_id", record.SessionID,
		"recipients_count", len(recipients))
	
	return nil
}

// HandleApprovalCallback 处理审批回调
func (n *ApprovalNotifier) HandleApprovalCallback(ctx context.Context, callback *Callback) error {
	action := callback.Action
	approvalID, ok := callback.Data["approval_id"].(string)
	if !ok {
		return fmt.Errorf("approval_id not found in callback data")
	}
	
	userID := callback.User.OpenID
	userName := callback.User.Name
	
	slog.Info("handling approval callback",
		"action", action,
		"approval_id", approvalID,
		"user", userName)
	
	switch action {
	case "approve":
		reason := fmt.Sprintf("批准人: %s", userName)
		if reasonText, ok := callback.Data["reason"].(string); ok && reasonText != "" {
			reason += fmt.Sprintf(" - %s", reasonText)
		}
		
		if err := n.approvalMgr.Approve(ctx, approvalID, callback.TenantID, userID, reason); err != nil {
			return fmt.Errorf("failed to approve: %w", err)
		}
		
		// 发送确认消息
		n.sendConfirmation(ctx, callback.User, "审批已批准", "✅ 您已批准此审批请求")
		
	case "reject":
		reason := fmt.Sprintf("拒绝人: %s", userName)
		if reasonText, ok := callback.Data["reason"].(string); ok && reasonText != "" {
			reason += fmt.Sprintf(" - %s", reasonText)
		}
		
		if err := n.approvalMgr.Reject(ctx, approvalID, callback.TenantID, userID, reason); err != nil {
			return fmt.Errorf("failed to reject: %w", err)
		}
		
		// 发送确认消息
		n.sendConfirmation(ctx, callback.User, "审批已拒绝", "❌ 您已拒绝此审批请求")
		
	case "detail":
		// 发送详细信息
		return n.sendDetailCard(ctx, approvalID, callback.User)
		
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
	
	return nil
}

// sendConfirmation 发送确认消息
func (n *ApprovalNotifier) sendConfirmation(ctx context.Context, user CallbackUser, title, content string) {
	msg := &Message{
		ID:         uuid.New().String(),
		Type:       MessageTypeInfo,
		Priority:   PriorityNormal,
		Title:      title,
		Content:    content,
		Recipients: []string{user.OpenID},
		CreatedAt:  time.Now(),
	}
	
	if err := n.channel.Send(ctx, msg); err != nil {
		slog.Error("failed to send confirmation", "user", user.Name, "error", err)
	}
}

// sendDetailCard 发送详细信息卡片
func (n *ApprovalNotifier) sendDetailCard(ctx context.Context, approvalID string, user CallbackUser) error {
	// 获取审批记录
	record, err := n.approvalMgr.GetForTenant(ctx, approvalID, "")
	if err != nil {
		return fmt.Errorf("failed to get approval record: %w", err)
	}
	
	// 构建详细卡片
	card := &InteractiveCard{
		Header: CardHeader{
			Title:    "📋 审批详情",
			Template: "blue",
		},
		Elements: []CardElement{
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
			{
				Type: ElementTypeDivider,
			},
		},
		Metadata: map[string]any{
			"recipients": []string{user.OpenID},
		},
	}
	
	// 添加检测结果
	if record.DetectResult != nil {
		card.Elements = append(card.Elements, CardElement{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "综合评分", Value: fmt.Sprintf("%d/10", record.DetectResult.Score), Short: true},
				{Key: "决策", Value: string(record.DetectResult.Decision), Short: true},
			},
		})
		
		if len(record.DetectResult.SensitiveWords) > 0 {
			card.Elements = append(card.Elements, CardElement{
				Type: ElementTypeText,
				Text: "⚠️ 敏感词: " + joinStrings(record.DetectResult.SensitiveWords, ", "),
			})
		}
		
		if len(record.DetectResult.Threats) > 0 {
			threatsText := ""
			for _, threat := range record.DetectResult.Threats {
				threatsText += fmt.Sprintf("- %s (严重度: %d)\n", threat.Type, threat.Severity)
			}
			card.Elements = append(card.Elements, CardElement{
				Type: ElementTypeText,
				Text: "🚨 威胁:\n" + threatsText,
			})
		}
	}
	
	// 添加快照信息
	if record.Snapshot != nil {
		card.Elements = append(card.Elements, CardElement{
			Type: ElementTypeDivider,
		})
		card.Elements = append(card.Elements, CardElement{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "客户端模型", Value: record.Snapshot.ClientModel, Short: true},
				{Key: "客户端IP", Value: record.Snapshot.ClientInfo.IP, Short: true},
			},
		})
	}
	
	return n.channel.SendCard(ctx, card)
}

// ApprovalRoutingTable 审批路由表
type ApprovalRoutingTable struct {
	rules RoutingRules
}

// NewApprovalRoutingTable 创建审批路由表
func NewApprovalRoutingTable(rules RoutingRules) *ApprovalRoutingTable {
	return &ApprovalRoutingTable{rules: rules}
}

// Route 根据租户和风险级别路由到审批人
func (t *ApprovalRoutingTable) Route(tenantID string, riskLevel string) []Recipient {
	return t.rules.Route(tenantID, riskLevel)
}

// AddRule 添加路由规则
func (t *ApprovalRoutingTable) AddRule(rule RoutingRule) {
	t.rules = append(t.rules, rule)
}

// RemoveRule 删除路由规则
func (t *ApprovalRoutingTable) RemoveRule(tenantID, riskLevel string) {
	newRules := make(RoutingRules, 0)
	for _, rule := range t.rules {
		if rule.TenantID != tenantID || rule.RiskLevel != riskLevel {
			newRules = append(newRules, rule)
		}
	}
	t.rules = newRules
}

// LoadFromDatabase 从数据库加载路由规则（待实现）
func (t *ApprovalRoutingTable) LoadFromDatabase(ctx context.Context) error {
	// TODO: 从 approval_routing 表加载规则
	return nil
}

// riskLevelFromScore 根据评分计算风险级别
func riskLevelFromScore(score int) string {
	switch {
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 5:
		return "medium"
	default:
		return "low"
	}
}

// priorityFromScore 根据评分计算优先级
func priorityFromScore(score int) Priority {
	switch {
	case score >= 9:
		return PriorityUrgent
	case score >= 7:
		return PriorityHigh
	case score >= 5:
		return PriorityNormal
	default:
		return PriorityLow
	}
}
