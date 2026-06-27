package sessionaudit

import (
	"time"
)

// Event 类型常量
const (
	EventTypeSessionAudit    = "session.audit"            // 会话审计事件（触发异步分析）
	EventTypeApprovalNeeded  = "session.approval_needed"  // 需要审批事件
	EventTypeApprovalDecided = "session.approval_decided" // 审批完成事件
)

// SessionAuditEvent 会话审计事件（发布到 EventBus，异步处理）
type SessionAuditEvent struct {
	SessionID    string        `json:"session_id"`
	TenantID     string        `json:"tenant_id"`
	Content      string        `json:"content"`       // 用户输入内容
	DetectResult *DetectResult `json:"detect_result"` // 实时检测结果
	ClientInfo   ClientInfo    `json:"client_info"`
	timestamp    time.Time     // 小写，避免与方法冲突
}

// Type 实现 eventbus.Event 接口
func (e *SessionAuditEvent) Type() string {
	return EventTypeSessionAudit
}

// Timestamp 实现 eventbus.Event 接口
func (e *SessionAuditEvent) Timestamp() time.Time {
	return e.timestamp
}

// ApprovalNeededEvent 需要审批事件
type ApprovalNeededEvent struct {
	ApprovalID   string           `json:"approval_id"`
	SessionID    string           `json:"session_id"`
	TenantID     string           `json:"tenant_id"`
	RequestID    string           `json:"request_id"`
	DetectResult *DetectResult    `json:"detect_result"`
	Snapshot     *RequestSnapshot `json:"snapshot"`
	ExpiresAt    time.Time        `json:"expires_at"`
	timestamp    time.Time
}

func (e *ApprovalNeededEvent) Type() string {
	return EventTypeApprovalNeeded
}

func (e *ApprovalNeededEvent) Timestamp() time.Time {
	return e.timestamp
}

// ApprovalDecidedEvent 审批完成事件
type ApprovalDecidedEvent struct {
	ApprovalID string         `json:"approval_id"`
	SessionID  string         `json:"session_id"`
	Status     ApprovalStatus `json:"status"` // approved/rejected/timeout
	ApprovedBy string         `json:"approved_by"`
	Reason     string         `json:"reason"`
	timestamp  time.Time
}

func (e *ApprovalDecidedEvent) Type() string {
	return EventTypeApprovalDecided
}

func (e *ApprovalDecidedEvent) Timestamp() time.Time {
	return e.timestamp
}
