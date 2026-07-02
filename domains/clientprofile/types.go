// Package clientprofile 实现客户端画像领域。
//
// 核心能力：
//   - 跨会话聚合客户端行为特征
//   - 客户端偏好学习（模型偏好、任务类型）
//   - 行为模式分析（时间分布、质量指标）
//   - 趋势追踪与异常检测
//
// 领域边界：
//   - 本包 OWNS：客户端画像数据模型、聚合逻辑、趋势分析
//   - 本包 NOT OWNS：客户端身份识别（identity域）、会话管理（session域）
package clientprofile

import (
	"time"
)

// ClientProfile 客户端画像（跨会话聚合）
type ClientProfile struct {
	IdentityHash    string    `json:"identity_hash"`      // 来自 identity.ClientIdentity
	TenantID        string    `json:"tenant_id"`
	VirtualClientID string    `json:"virtual_client_id"`  // vc-xxx

	// 统计数据
	TotalSessions int64     `json:"total_sessions"`
	TotalRequests int64     `json:"total_requests"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`

	// 行为特征
	PreferredModels  []ModelPreference  `json:"preferred_models"`   // 模型偏好（使用频次排序）
	TaskDistribution map[string]int64   `json:"task_distribution"`  // 任务类型分布
	AvgSessionLength float64            `json:"avg_session_length"` // 平均会话轮次
	AvgTokensPerTurn float64            `json:"avg_tokens_per_turn"` // 平均每轮Token数

	// 质量指标
	ErrorRate    float64 `json:"error_rate"`    // 错误率 0-1
	ApprovalRate float64 `json:"approval_rate"` // 审批通过率（高风险会话占比）0-1

	// 时间模式
	ActiveHours  []int         `json:"active_hours"`   // 活跃时段（0-23小时分布）
	PeakUsageDay time.Weekday  `json:"peak_usage_day"` // 高峰使用日

	UpdatedAt time.Time `json:"updated_at"`
}

// ModelPreference 模型偏好
type ModelPreference struct {
	ModelName    string  `json:"model_name"`
	UsageCount   int64   `json:"usage_count"`
	SuccessRate  float64 `json:"success_rate"`   // 0-1
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// ClientBehaviorEvent 客户端行为事件（用于异步聚合）
type ClientBehaviorEvent struct {
	EventID      string    `json:"event_id"`
	IdentityHash string    `json:"identity_hash"`
	TenantID     string    `json:"tenant_id"`
	SessionID    string    `json:"session_id"`
	RequestID    string    `json:"request_id"`
	EventType    string    `json:"event_type"` // session_start, request_completed, approval_required, error
	Model        string    `json:"model"`
	TaskType     string    `json:"task_type"`  // code, chat, reasoning, unknown
	TokensUsed   int       `json:"tokens_used"`
	LatencyMs    int64     `json:"latency_ms"`
	Success      bool      `json:"success"`
	Timestamp    time.Time `json:"timestamp"`
}

// EventType 事件类型常量
const (
	EventTypeSessionStart     = "session_start"
	EventTypeRequestCompleted = "request_completed"
	EventTypeApprovalRequired = "approval_required"
	EventTypeError            = "error"
)

// TaskType 任务类型常量
const (
	TaskTypeCode      = "code"
	TaskTypeChat      = "chat"
	TaskTypeReasoning = "reasoning"
	TaskTypeUnknown   = "unknown"
)

// TrendAnalysis 趋势分析结果
type TrendAnalysis struct {
	IdentityHash string    `json:"identity_hash"`
	StartDate    time.Time `json:"start_date"`
	EndDate      time.Time `json:"end_date"`
	Days         int       `json:"days"`

	// 使用趋势
	DailyRequests []DailyMetric `json:"daily_requests"` // 每日请求数趋势
	DailySessions []DailyMetric `json:"daily_sessions"` // 每日会话数趋势

	// 质量趋势
	ErrorRateTrend    []DailyMetric `json:"error_rate_trend"`
	LatencyTrend      []DailyMetric `json:"latency_trend"`
	
	// 模型使用变化
	ModelShifts []ModelShift `json:"model_shifts"` // 模型偏好变化

	// 异常检测
	Anomalies []Anomaly `json:"anomalies"`
}

// DailyMetric 每日指标
type DailyMetric struct {
	Date  time.Time `json:"date"`
	Value float64   `json:"value"`
}

// ModelShift 模型偏好变化
type ModelShift struct {
	Date      time.Time `json:"date"`
	FromModel string    `json:"from_model"`
	ToModel   string    `json:"to_model"`
	Reason    string    `json:"reason"` // performance, cost, availability
}

// Anomaly 异常事件
type Anomaly struct {
	Date        time.Time `json:"date"`
	Type        string    `json:"type"` // error_spike, latency_spike, usage_drop
	Severity    string    `json:"severity"` // low, medium, high
	Description string    `json:"description"`
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
}

// ProfileSummary 画像摘要（用于列表展示）
type ProfileSummary struct {
	IdentityHash    string    `json:"identity_hash"`
	VirtualClientID string    `json:"virtual_client_id"`
	TotalSessions   int64     `json:"total_sessions"`
	TotalRequests   int64     `json:"total_requests"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	TopModel        string    `json:"top_model"`        // 最常用模型
	PrimaryTaskType string    `json:"primary_task_type"` // 主要任务类型
	ErrorRate       float64   `json:"error_rate"`
}
