// Package sessionaudit 实现会话审计与安全监控领域。
//
// 核心能力：
//   - 实时检测：敏感词扫描、Prompt Injection、PII 泄漏检测（≤5ms）
//   - 异步审计：LLM 意图分析、内容总结、多维度评分
//   - 暂停/恢复：高风险会话进入审批流程
//   - 链式审批：可插拔的审批节点
//
// 领域边界：
//   - 本包 OWNS：检测逻辑、评分引擎、审批流程
//   - 本包 NOT OWNS：请求路由（gateway）、审计持久化（observability/audit）
package sessionaudit

import (
	"time"
)

// Decision 检测决策
type Decision string

const (
	DecisionPass         Decision = "pass"          // 通过，继续执行
	DecisionWarn         Decision = "warn"          // 警告，记录 + 继续
	DecisionBlock        Decision = "block"         // 阻断，返回 403
	DecisionNeedApproval Decision = "need_approval" // 需要人工审批
)

// String 返回稳定的字符串表示（用于审计日志）
func (d Decision) String() string {
	return string(d)
}

// Threat 威胁检测结果
type Threat struct {
	Type       string    `json:"type"`     // "prompt_inject"/"pii_leak"/"jailbreak"/"rate_abuse"
	Severity   int       `json:"severity"` // 0-10
	Evidence   string    `json:"evidence"` // 命中的文本片段（PII 需脱敏）
	DetectedAt time.Time `json:"detected_at"`
}

// DetectResult 实时检测结果
type DetectResult struct {
	Score          int      `json:"score"`           // 0-10 综合评分
	SensitiveWords []string `json:"sensitive_words"` // 命中的敏感词
	Threats        []Threat `json:"threats"`         // 威胁列表
	Decision       Decision `json:"decision"`        // 决策
	Reason         string   `json:"reason"`          // 决策原因
	LatencyMs      int      `json:"latency_ms"`      // 检测耗时（毫秒）
}

// Intent 意图分析结果（异步 LLM 分析）
type Intent struct {
	Type       string    `json:"type"`   // "chat"/"code"/"tool_use"/"harmful"/"unknown"
	Score      float64   `json:"score"`  // 0.0-1.0 置信度
	Reason     string    `json:"reason"` // 分析原因
	DetectedAt time.Time `json:"detected_at"`
}

// Summary 内容总结
type Summary struct {
	Title       string    `json:"title"`        // 会话标题（≤50 字符）
	KeyPoints   []string  `json:"key_points"`   // 关键要点
	ContentHash string    `json:"content_hash"` // SHA256 摘要（用于去重）
	GeneratedAt time.Time `json:"generated_at"`
}

// ClientInfo 客户端信息
type ClientInfo struct {
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	Model      string `json:"model"`       // 客户端请求的模型
	Agent      string `json:"agent"`       // 智能体标识（如果有）
	DeviceSeed string `json:"device_seed"` // 设备指纹
}

// MultiDimensionScore 多维度评分
type MultiDimensionScore struct {
	Security  int `json:"security"`  // 安全性 0-10（越高越安全）
	Danger    int `json:"danger"`    // 危险性 0-10（越高越危险）
	Trust     int `json:"trust"`     // 可信度 0-10（越高越可信）
	Sensitive int `json:"sensitive"` // 敏感性 0-10（越高越敏感）
}

// RequestSnapshot 请求快照（用于暂停/恢复）
type RequestSnapshot struct {
	SessionID    string        `json:"session_id"`
	TenantID     string        `json:"tenant_id"`
	RequestID    string        `json:"request_id"`
	BodyBytes    []byte        `json:"body_bytes"` // 原始请求体
	ClientModel  string        `json:"client_model"`
	ClientInfo   ClientInfo    `json:"client_info"`
	DetectResult *DetectResult `json:"detect_result"`
	CreatedAt    time.Time     `json:"created_at"`
}

// ApprovalStatus 审批状态
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"  // 待审批
	ApprovalApproved ApprovalStatus = "approved" // 已批准
	ApprovalRejected ApprovalStatus = "rejected" // 已拒绝
	ApprovalTimeout  ApprovalStatus = "timeout"  // 超时（自动拒绝）
)

// ApprovalRecord 审批记录
type ApprovalRecord struct {
	ID           string           `json:"id"` // UUID
	SessionID    string           `json:"session_id"`
	TenantID     string           `json:"tenant_id"`
	RequestID    string           `json:"request_id"`
	Status       ApprovalStatus   `json:"status"`
	DetectResult *DetectResult    `json:"detect_result"`
	Snapshot     *RequestSnapshot `json:"snapshot"`
	ApprovedBy   string           `json:"approved_by,omitempty"` // 审批人
	ApprovedAt   *time.Time       `json:"approved_at,omitempty"`
	Reason       string           `json:"reason,omitempty"` // 审批意见
	CreatedAt    time.Time        `json:"created_at"`
	ExpiresAt    time.Time        `json:"expires_at"` // 超时时间
}

// AuditRecord 审计记录（数据库持久化）
type AuditRecord struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id"`
	RequestID string `json:"request_id"`

	// 客户端信息
	ClientInfo ClientInfo `json:"client_info"`

	// 内容摘要
	Summary *Summary `json:"summary,omitempty"`
	Intent  *Intent  `json:"intent,omitempty"`

	// 多维度评分
	Scores MultiDimensionScore `json:"scores"`

	// 检测结果
	DetectResult *DetectResult `json:"detect_result"`

	// 状态
	Status         Decision       `json:"status"` // pass/warn/blocked/need_approval
	ApprovalStatus ApprovalStatus `json:"approval_status,omitempty"`
	ApprovedBy     string         `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time     `json:"approved_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
