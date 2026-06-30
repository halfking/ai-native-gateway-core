// Package notification 实现通知系统，支持多种通知渠道（飞书、钉钉、企业微信等）。
//
// 核心能力：
//   - 多渠道通知：飞书机器人、钉钉机器人、企业微信等
//   - 交互式卡片：支持审批、按钮、表单等交互
//   - 回调处理：处理用户在通知中的交互行为
//   - 路由规则：根据租户、风险级别等路由到对应人员
//
// 设计原则：
//   - 渠道抽象：统一的NotificationChannel接口
//   - 可扩展：易于添加新的通知渠道
//   - 异步处理：不阻塞主流程
package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// MessageType 消息类型
type MessageType string

const (
	// MessageTypeApproval 审批消息
	MessageTypeApproval MessageType = "approval"
	
	// MessageTypeAlert 告警消息
	MessageTypeAlert MessageType = "alert"
	
	// MessageTypeInfo 信息消息
	MessageTypeInfo MessageType = "info"
	
	// MessageTypeNotification 通知消息
	MessageTypeNotification MessageType = "notification"
)

// Priority 优先级
type Priority string

const (
	// PriorityUrgent 紧急
	PriorityUrgent Priority = "urgent"
	
	// PriorityHigh 高
	PriorityHigh Priority = "high"
	
	// PriorityNormal 正常
	PriorityNormal Priority = "normal"
	
	// PriorityLow 低
	PriorityLow Priority = "low"
)

// Message 通知消息
type Message struct {
	// ID 消息唯一标识
	ID string
	
	// Type 消息类型
	Type MessageType
	
	// Priority 优先级
	Priority Priority
	
	// TenantID 租户ID
	TenantID string
	
	// SessionID 会话ID
	SessionID string
	
	// RequestID 请求ID
	RequestID string
	
	// Title 消息标题
	Title string
	
	// Content 消息内容
	Content string
	
	// Metadata 元数据
	Metadata map[string]any
	
	// Recipients 接收人列表（飞书openID或email）
	Recipients []string
	
	// Actions 可执行的操作
	Actions []Action
	
	// CreatedAt 创建时间
	CreatedAt time.Time
}

// Action 操作按钮
type Action struct {
	// ID 操作唯一标识
	ID string
	
	// Label 按钮文本
	Label string
	
	// Type 操作类型（primary/danger/default）
	Type string
	
	// URL 跳转链接（可选）
	URL string
	
	// Confirm 是否需要确认
	Confirm bool
	
	// ConfirmText 确认文本
	ConfirmText string
}

// CardHeader 卡片头部
type CardHeader struct {
	// Title 标题
	Title string
	
	// Template 模板颜色（blue/green/red/orange/grey）
	Template string
	
	// Icon 图标URL（可选）
	Icon string
}

// CardElementType 卡片元素类型
type CardElementType string

const (
	// ElementTypeText 文本
	ElementTypeText CardElementType = "text"
	
	// ElementTypeField 字段
	ElementTypeField CardElementType = "field"
	
	// ElementTypeButton 按钮
	ElementTypeButton CardElementType = "button"
	
	// ElementTypeDivider 分割线
	ElementTypeDivider CardElementType = "divider"
	
	// ElementTypeImage 图片
	ElementTypeImage CardElementType = "image"
	
	// ElementTypeNote 备注
	ElementTypeNote CardElementType = "note"
)

// CardElement 卡片元素
type CardElement struct {
	// Type 元素类型
	Type CardElementType
	
	// Text 文本内容
	Text string
	
	// Fields 字段列表（用于field类型）
	Fields []CardField
	
	// Actions 操作按钮列表（用于button类型）
	Actions []CardAction
	
	// ImageURL 图片URL（用于image类型）
	ImageURL string
	
	// Alt 图片替代文本
	Alt string
}

// CardField 卡片字段
type CardField struct {
	// Key 字段名
	Key string
	
	// Value 字段值
	Value string
	
	// Short 是否短字段（两列显示）
	Short bool
}

// CardAction 卡片操作
type CardAction struct {
	// ID 操作唯一标识
	ID string
	
	// Text 按钮文本
	Text string
	
	// Style 按钮样式（primary/danger/default）
	Style string
	
	// URL 跳转URL（可选）
	URL string
	
	// Value 携带的值
	Value map[string]any
}

// InteractiveCard 交互式卡片
type InteractiveCard struct {
	// Header 头部
	Header CardHeader
	
	// Elements 元素列表
	Elements []CardElement
	
	// Actions 底部操作按钮
	Actions []CardAction
	
	// CallbackURL 回调URL
	CallbackURL string
	
	// Metadata 元数据
	Metadata map[string]any
}

// Callback 回调数据
type Callback struct {
	// ID 回调唯一标识
	ID string
	
	// Action 触发的操作ID
	Action string
	
	// User 操作用户
	User CallbackUser
	
	// TenantID 租户ID
	TenantID string
	
	// SessionID 会话ID
	SessionID string
	
	// Data 携带的数据
	Data map[string]any
	
	// Timestamp 时间戳
	Timestamp time.Time
}

// CallbackUser 回调用户信息
type CallbackUser struct {
	// ID 用户ID
	ID string
	
	// OpenID 飞书OpenID
	OpenID string
	
	// Name 用户名
	Name string
	
	// Email 邮箱
	Email string
}

// NotificationChannel 通知渠道接口
type NotificationChannel interface {
	// Name 返回渠道名称
	Name() string
	
	// Send 发送普通消息
	Send(ctx context.Context, msg *Message) error
	
	// SendCard 发送交互式卡片
	SendCard(ctx context.Context, card *InteractiveCard) error
	
	// HandleCallback 处理回调
	HandleCallback(ctx context.Context, callback *Callback) error
}

// ApprovalCard 审批卡片（专用于审批场景）
type ApprovalCard struct {
	// SessionID 会话ID
	SessionID string
	
	// TenantID 租户ID
	TenantID string
	
	// RequestID 请求ID
	RequestID string
	
	// ApprovalID 审批记录ID
	ApprovalID string
	
	// RiskLevel 风险级别
	RiskLevel string
	
	// DetectResult 检测结果
	DetectResult *sessionaudit.DetectResult
	
	// Snapshot 请求快照
	Snapshot *sessionaudit.RequestSnapshot
	
	// Actions 操作按钮
	Actions []CardAction
	
	// CreatedAt 创建时间
	CreatedAt time.Time
}

// ToInteractiveCard 转换为通用交互式卡片
func (ac *ApprovalCard) ToInteractiveCard() *InteractiveCard {
	// 根据风险级别选择颜色
	headerTemplate := "blue"
	switch ac.RiskLevel {
	case "critical":
		headerTemplate = "red"
	case "high":
		headerTemplate = "orange"
	case "medium":
		headerTemplate = "blue"
	case "low":
		headerTemplate = "green"
	}
	
	card := &InteractiveCard{
		Header: CardHeader{
			Title:    "🔐 会话审批请求",
			Template: headerTemplate,
		},
		Elements: []CardElement{
			{
				Type: ElementTypeField,
				Fields: []CardField{
					{Key: "会话ID", Value: ac.SessionID, Short: true},
					{Key: "请求ID", Value: ac.RequestID, Short: true},
					{Key: "风险级别", Value: ac.RiskLevel, Short: true},
					{Key: "评分", Value: formatScore(ac.DetectResult.Score), Short: true},
				},
			},
			{
				Type: ElementTypeDivider,
			},
		},
		Actions: ac.Actions,
		Metadata: map[string]any{
			"approval_id": ac.ApprovalID,
			"session_id":  ac.SessionID,
			"tenant_id":   ac.TenantID,
		},
	}
	
	// 添加检测结果
	if ac.DetectResult != nil {
		if len(ac.DetectResult.SensitiveWords) > 0 {
			card.Elements = append(card.Elements, CardElement{
				Type: ElementTypeText,
				Text: "⚠️ 敏感词: " + joinStrings(ac.DetectResult.SensitiveWords, ", "),
			})
		}
		
		if len(ac.DetectResult.Threats) > 0 {
			card.Elements = append(card.Elements, CardElement{
				Type: ElementTypeText,
				Text: "🚨 威胁: " + formatThreats(ac.DetectResult.Threats),
			})
		}
		
		if ac.DetectResult.Reason != "" {
			card.Elements = append(card.Elements, CardElement{
				Type: ElementTypeNote,
				Text: "原因: " + ac.DetectResult.Reason,
			})
		}
	}
	
	return card
}

// Recipient 接收人
type Recipient struct {
	// ID 用户ID
	ID string
	
	// Name 用户名
	Name string
	
	// Email 邮箱
	Email string
	
	// LarkOpenID 飞书OpenID
	LarkOpenID string
	
	// DingTalkUserID 钉钉UserID
	DingTalkUserID string
	
	// WeChatUserID 企业微信UserID
	WeChatUserID string
}

// RoutingRule 路由规则
type RoutingRule struct {
	// TenantID 租户ID
	TenantID string
	
	// RiskLevel 风险级别
	RiskLevel string
	
	// Recipients 接收人列表
	Recipients []Recipient
	
	// Priority 优先级
	Priority int
	
	// Enabled 是否启用
	Enabled bool
}

// RoutingRules 路由规则集合
type RoutingRules []RoutingRule

// Route 根据租户和风险级别路由到接收人
func (rr RoutingRules) Route(tenantID, riskLevel string) []Recipient {
	var recipients []Recipient
	
	for _, rule := range rr {
		if !rule.Enabled {
			continue
		}
		
		if rule.TenantID == tenantID && rule.RiskLevel == riskLevel {
			recipients = append(recipients, rule.Recipients...)
		}
	}
	
	return recipients
}

// 辅助函数
func formatScore(score int) string {
	return fmt.Sprintf("%d/10", score)
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func formatThreats(threats []sessionaudit.Threat) string {
	if len(threats) == 0 {
		return "无"
	}
	result := ""
	for i, threat := range threats {
		if i > 0 {
			result += ", "
		}
		result += fmt.Sprintf("%s(严重度:%d)", threat.Type, threat.Severity)
	}
	return result
}
