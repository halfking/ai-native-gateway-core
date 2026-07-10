// Package sessioninspector 实现会话检查器编排。
//
// 适用阶段：PreRouting / PostResponse
//
// 命名说明：Inspector（复数）不是单个检查器，而是可插拔的检查器框架；
// 具体的检查逻辑由实现 Inspector 接口的对象承担（TokenLimit、Inactive、...）。
package sessioninspector

import "time"

// Severity 严重程度。
type Severity string

const (
	// SeverityInfo 仅作提示，不影响主流程。
	SeverityInfo Severity = "info"
	// SeverityWarning 警告，需要关注。
	SeverityWarning Severity = "warning"
	// SeverityError 错误，需立即处理（但可降级）。
	SeverityError Severity = "error"
	// SeverityCritical 严重，可能涉及安全或合规问题。
	SeverityCritical Severity = "critical"
)

// Finding 检查发现。
type Finding struct {
	InspectorName string         `json:"inspector_name"`
	Severity      Severity       `json:"severity"`
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	Suggestion    string         `json:"suggestion,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	DetectedAt    time.Time      `json:"detected_at"`
}

// SessionSnapshot 会话快照。
//
// 由 InspectorHook 从 PipelineRequest 构造，作为检查器的输入。
// 不修改 PipelineRequest，避免 hook 间相互影响。
//
// 字段说明：
//   - RequestCount: 当前累计请求数（来自 metadata / DB）
//   - TokenCount:   当前累计 token 数
//   - StartedAt:    会话首次活跃时间（用于 absolute_max_lifetime 计算）
//   - LastActiveAt: 最近一次活跃时间（用于 idle 检测）
//   - ErrorRate:    会话错误率 0.0~1.0（来自 session_summaries）
//   - BurstCount:   burst_window_seconds 窗口内的请求数
//   - ConcurrentCount: 当前并发请求数
//   - TenantActiveCount: 当前租户 active 态会话数（用于 lifecycle 检查）
//   - ModelSwitchCount: 累计模型切换次数
type SessionSnapshot struct {
	SessionID    string         `json:"session_id"`
	TenantID     string         `json:"tenant_id"`
	RequestCount int            `json:"request_count"`
	TokenCount   int            `json:"token_count"`
	StartedAt    time.Time      `json:"started_at"`
	LastActiveAt time.Time      `json:"last_active_at"`
	ErrorRate    float64        `json:"error_rate"`
	BurstCount   int            `json:"burst_count"`
	ConcurrentCount int         `json:"concurrent_count"`
	TenantActiveCount int       `json:"tenant_active_count"`
	ModelSwitchCount int        `json:"model_switch_count"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Inspector 检查器接口。
//
// 一个 Inspector 负责检测一类问题（例如 token 超限、闲置过久）。
// Inspect 返回 nil 表示"通过"，返回 []*Finding 表示命中若干问题。
type Inspector interface {
	// Name 返回 Inspector 名称（用于 Finding 与日志）。
	Name() string

	// Inspect 检查会话。
	// 返回 nil, nil 表示通过。
	// 返回 findings, nil 表示命中。
	// 返回 nil, err 表示检查器自身故障（Hook 会上抛）。
	Inspect(snap *SessionSnapshot) ([]*Finding, error)
}
