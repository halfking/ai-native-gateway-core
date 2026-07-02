// Package notification 实现多渠道通知系统（飞书/钉钉/企业微信），
// 支撑审批流程的实时触达与交互回调。
//
// 核心能力：
//   - 多渠道通知：飞书机器人、钉钉机器人、企业微信
//   - 交互式卡片：支持审批、按钮、表单等交互
//   - 回调处理：处理用户在通知中的交互行为
//   - 路由规则：根据租户、风险级别等路由到对应人员
//
// 设计原则：
//   - 渠道抽象：统一的 NotificationChannel 接口，新增渠道实现接口即可
//   - 异步友好：所有外部调用受 ctx 控制，支持超时与取消
//   - 凭证隔离：app_secret / webhook_secret 等敏感字段不进入日志
package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// MessageType 通知消息类型。
type MessageType string

const (
	MessageTypeApproval     MessageType = "approval"     // 审批
	MessageTypeAlert        MessageType = "alert"        // 告警
	MessageTypeInfo         MessageType = "info"         // 普通信息
	MessageTypeNotification MessageType = "notification" // 通用通知
)

// Priority 优先级，决定卡片配色与下发顺序。
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// ChannelType 渠道类型。
type ChannelType string

const (
	ChannelLark     ChannelType = "lark"
	ChannelDingTalk ChannelType = "dingtalk"
	ChannelWeChat   ChannelType = "wechat"
)

// RiskLevel 风险等级（与 sessionaudit 的分数阈值对应）。
type RiskLevel string

const (
	RiskLevelCritical RiskLevel = "critical" // ≥9
	RiskLevelHigh     RiskLevel = "high"     // 7-8
	RiskLevelMedium   RiskLevel = "medium"   // 5-6
	RiskLevelLow      RiskLevel = "low"      // ≤4
)

// Message 普通文本通知消息。
type Message struct {
	ID         string         // 全局唯一 ID
	Type       MessageType    // 消息类型
	Priority   Priority       // 优先级
	TenantID   string         // 租户 ID
	SessionID  string         // 会话 ID
	RequestID  string         // 请求 ID
	Title      string         // 标题
	Content    string         // 内容
	Metadata   map[string]any // 自定义元数据（透传至渠道）
	Recipients []string       // 接收人 ID 列表（渠道内的 open_id / userid）
	CreatedAt  time.Time
}

// Action 用户在卡片上的可执行操作（Action 抽象，跨渠道复用）。
type Action struct {
	ID          string         // 全局唯一
	Label       string         // 按钮文本
	Type        string         // primary / danger / default
	URL         string         // 可选跳转链接
	Confirm     bool           // 是否二次确认
	ConfirmText string         // 确认文案
	Value       map[string]any // 回调透传值
}

// CardHeader 卡片头部。
type CardHeader struct {
	Title    string // 卡片标题
	Template string // 配色：blue/green/red/orange/grey
	Icon     string // 可选图标 URL
}

// CardElementType 卡片元素类型。
type CardElementType string

const (
	ElementTypeText    CardElementType = "text"
	ElementTypeField   CardElementType = "field"
	ElementTypeButton  CardElementType = "button"
	ElementTypeDivider CardElementType = "divider"
	ElementTypeImage   CardElementType = "image"
	ElementTypeNote    CardElementType = "note"
)

// CardElement 卡片元素。
type CardElement struct {
	Type     CardElementType
	Text     string
	Fields   []CardField
	Actions  []CardAction
	ImageURL string
	Alt      string
}

// CardField 键值对字段。
type CardField struct {
	Key   string
	Value string
	Short bool // 两列并排
}

// CardAction 卡片内嵌按钮。
type CardAction struct {
	ID    string
	Text  string
	Style string // primary / danger / default
	URL   string
	Value map[string]any
}

// InteractiveCard 通用交互式卡片。
type InteractiveCard struct {
	Header      CardHeader
	Elements    []CardElement
	Actions     []CardAction
	CallbackURL string
	Metadata    map[string]any // 至少包含 "recipients" 和 "approval_id"
}

// Callback 渠道回调（审批按钮点击 / 表单提交）。
type Callback struct {
	ID        string         // 回调唯一 ID
	Action    string         // 触发的 Action ID
	User      CallbackUser   // 操作人
	TenantID  string         // 租户
	SessionID string         // 会话
	Data      map[string]any // 卡片透传 Value
	Timestamp time.Time
}

// CallbackUser 回调用户。
type CallbackUser struct {
	ID     string
	OpenID string // 飞书 / 钉钉 / 企业微信用户标识（按渠道）
	Name   string
	Email  string
}

// NotificationChannel 通知渠道统一接口。
//
// 所有方法必须：
//   - 接受 ctx 并尊重 ctx.Done()
//   - 自行处理凭证（不在日志中暴露）
//   - 单次调用 ≤30s
//
// ParseCallback 接收渠道原始 HTTP body（含签名等），返回归一化的 Callback；
// 签名校验失败应返回 error，由 HTTP 层决定如何响应渠道（200 / 401）。
type NotificationChannel interface {
	Name() string                                                     // 渠道标识（lark/dingtalk/wechat）
	Send(ctx context.Context, msg *Message) error                     // 发送普通文本
	SendCard(ctx context.Context, card *InteractiveCard) error        // 发送交互式卡片
	ParseCallback(ctx context.Context, raw []byte) (*Callback, error) // 签名校验 + 解析
	HealthCheck(ctx context.Context) error                            // 渠道连通性检查
}

// ApprovalCard 审批场景专用卡片输入。
type ApprovalCard struct {
	SessionID    string
	TenantID     string
	RequestID    string
	ApprovalID   string
	RiskLevel    string
	DetectResult *sessionaudit.DetectResult
	Snapshot     *sessionaudit.RequestSnapshot

	// 增强字段（来自 SessionSummary，可选）
	SessionSummary *SessionSummaryView

	// 路由出来的接收人（每个渠道对应不同的 OpenID 字段）
	Recipients []Recipient

	Actions   []CardAction
	CreatedAt time.Time
}

// SessionSummaryView 会话总结摘要（在审批卡片中展示用）。
//
// 来自 sessionsummary.Summarizer 但仅保留卡片需要的字段，
// 避免 notification 包反向依赖 sessionsummary 的全部类型。
type SessionSummaryView struct {
	Title      string
	Summary    string
	KeyTopics  []string
	UserIntent string
	HasSummary bool
}

// ToInteractiveCard 将 ApprovalCard 转成通用交互式卡片。
//
// 输出包含：
//   - 头部（按风险级别着色）
//   - 基础信息（会话/请求/风险/评分）
//   - 威胁与敏感词列表
//   - 会话总结（如果有）
//   - 客户端信息
//   - 审批按钮（批准 / 拒绝 / 查看详情）
func (ac *ApprovalCard) ToInteractiveCard() *InteractiveCard {
	headerTemplate := pickHeaderTemplate(ac.RiskLevel)

	elements := []CardElement{
		{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "会话ID", Value: truncate(ac.SessionID, 32), Short: true},
				{Key: "请求ID", Value: truncate(ac.RequestID, 32), Short: true},
				{Key: "风险级别", Value: ac.RiskLevel, Short: true},
				{Key: "评分", Value: formatScore(ac.DetectResult.Score), Short: true},
			},
		},
		{Type: ElementTypeDivider},
	}

	if ac.DetectResult != nil {
		if len(ac.DetectResult.SensitiveWords) > 0 {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "⚠️ 敏感词: " + joinStrings(ac.DetectResult.SensitiveWords, ", "),
			})
		}
		if len(ac.DetectResult.Threats) > 0 {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "🚨 威胁: " + formatThreats(ac.DetectResult.Threats),
			})
		}
		if ac.DetectResult.Reason != "" {
			elements = append(elements, CardElement{
				Type: ElementTypeNote,
				Text: "原因: " + ac.DetectResult.Reason,
			})
		}
	}

	if ac.SessionSummary != nil && ac.SessionSummary.HasSummary {
		elements = append(elements, CardElement{Type: ElementTypeDivider})
		if ac.SessionSummary.Title != "" {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "📝 会话主题: **" + ac.SessionSummary.Title + "**",
			})
		}
		if ac.SessionSummary.Summary != "" {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "📄 总结: " + truncate(ac.SessionSummary.Summary, 280),
			})
		}
		if len(ac.SessionSummary.KeyTopics) > 0 {
			elements = append(elements, CardElement{
				Type: ElementTypeText,
				Text: "🏷️ 关键主题: " + joinStrings(ac.SessionSummary.KeyTopics, ", "),
			})
		}
		if ac.SessionSummary.UserIntent != "" {
			elements = append(elements, CardElement{
				Type: ElementTypeNote,
				Text: "🎯 用户意图: " + ac.SessionSummary.UserIntent,
			})
		}
	}

	if ac.Snapshot != nil && ac.Snapshot.ClientInfo.IP != "" {
		elements = append(elements, CardElement{Type: ElementTypeDivider})
		elements = append(elements, CardElement{
			Type: ElementTypeField,
			Fields: []CardField{
				{Key: "客户端模型", Value: ac.Snapshot.ClientModel, Short: true},
				{Key: "客户端IP", Value: ac.Snapshot.ClientInfo.IP, Short: true},
			},
		})
	}

	return &InteractiveCard{
		Header:   CardHeader{Title: "🔐 会话审批请求", Template: headerTemplate},
		Elements: elements,
		Actions:  ac.Actions,
		Metadata: map[string]any{
			"approval_id": ac.ApprovalID,
			"session_id":  ac.SessionID,
			"tenant_id":   ac.TenantID,
			"risk_level":  ac.RiskLevel,
		},
	}
}

// Recipient 接收人。三个渠道的 ID 字段各自独立，可同时存在。
type Recipient struct {
	ID             string
	Name           string
	Email          string
	LarkOpenID     string
	DingTalkUserID string
	WeChatUserID   string
}

// 辅助函数

func pickHeaderTemplate(risk string) string {
	switch RiskLevel(risk) {
	case RiskLevelCritical:
		return "red"
	case RiskLevelHigh:
		return "orange"
	case RiskLevelMedium:
		return "blue"
	case RiskLevelLow:
		return "green"
	default:
		return "blue"
	}
}

func formatScore(score int) string {
	return fmt.Sprintf("%d/10", score)
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	total := len(strs) - 1
	for _, s := range strs {
		total += len(s)
	}
	if total == 0 {
		return ""
	}
	buf := make([]byte, 0, total)
	buf = append(buf, strs[0]...)
	for i := 1; i < len(strs); i++ {
		buf = append(buf, sep...)
		buf = append(buf, strs[i]...)
	}
	return string(buf)
}

func formatThreats(threats []sessionaudit.Threat) string {
	if len(threats) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(threats))
	for _, threat := range threats {
		parts = append(parts, fmt.Sprintf("%s(严重度:%d)", threat.Type, threat.Severity))
	}
	return joinStrings(parts, ", ")
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// RiskLevelFromScore 将检测分数映射为风险级别。
func RiskLevelFromScore(score int) RiskLevel {
	switch {
	case score >= 9:
		return RiskLevelCritical
	case score >= 7:
		return RiskLevelHigh
	case score >= 5:
		return RiskLevelMedium
	default:
		return RiskLevelLow
	}
}

// PriorityFromScore 将检测分数映射为优先级。
func PriorityFromScore(score int) Priority {
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
