package credentialstate

import "time"

// State 凭据+模型的实时状态
type State struct {
	CredentialID     int        `json:"credential_id"`
	Model            string     `json:"model"`
	Available        bool       `json:"available"`
	HealthStatus     string     `json:"health_status"` // healthy, warning, degraded, unreachable
	SuccessRate      float64    `json:"success_rate"`
	AvgLatencyMs     int        `json:"avg_latency_ms"`
	P95LatencyMs     int        `json:"p95_latency_ms"`
	ActiveSessions   int        `json:"active_sessions"`
	ConcurrencyLimit int        `json:"concurrency_limit"`
	LastUpdatedAt    time.Time  `json:"last_updated_at"`
	LastSuccessAt    *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	RecoverAt        *time.Time `json:"recover_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	ConsecutiveFails int        `json:"consecutive_fails"`
	Source           string     `json:"source"` // request, probe_v2, model_probe, passive, manual
}

// StateUpdate 批量写入的状态更新记录
type StateUpdate struct {
	CredentialID  int
	Model         string
	Available     *bool
	HealthStatus  *string
	LatencyMs     *int
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	LastError     *string
	RecoverAt     *time.Time
	UpdatedAt     time.Time
}

// NodeStatus 节点状态枚举
type NodeStatus string

const (
	NodeStatusActive   NodeStatus = "active"
	NodeStatusDisabled NodeStatus = "disabled"
)

// RegisterNodeRequest 注册节点请求
type RegisterNodeRequest struct {
	CredentialID  int
	RawModelName  string
	ProbeEnabled  bool
	ProbeInterval time.Duration
	CreatedBy     string // "system" or "admin:<user_id>"
	TriggerProbe  bool   // 是否立即触发探测
}

// StateNode 状态监控节点
type StateNode struct {
	ID                   int
	CredentialID         int
	RawModelName         string
	NodeStatus           NodeStatus
	ProbeEnabled         bool
	ProbeIntervalSeconds int
	LastProbeAt          *time.Time
	NextProbeAt          *time.Time
	CreatedAt            time.Time
	CreatedBy            string
	UpdatedAt            time.Time
	DisabledAt           *time.Time
	DisabledBy           *string
}
