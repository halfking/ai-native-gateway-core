package fault

import "time"

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

type EventStatus string

const (
	EventStatusNew       EventStatus = "new"
	EventStatusAck       EventStatus = "acknowledged"
	EventStatusResolving EventStatus = "resolving"
	EventStatusResolved  EventStatus = "resolved"
	EventStatusIgnored   EventStatus = "ignored"
)

type Event struct {
	ID          int64       `json:"id"`
	RuleID      int64       `json:"rule_id"`
	RuleName    string      `json:"rule_name"`
	Severity    Severity    `json:"severity"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Source      string      `json:"source"`
	Status      EventStatus `json:"status"`
	Metadata    []byte      `json:"metadata,omitempty"`
	DetectedAt  time.Time   `json:"detected_at"`
	AckedAt     *time.Time  `json:"acked_at,omitempty"`
	AckedBy     string      `json:"acked_by,omitempty"`
	ResolvedAt  *time.Time  `json:"resolved_at,omitempty"`
	ResolvedBy  string      `json:"resolved_by,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type Rule struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Metric       string    `json:"metric"`
	Operator     string    `json:"operator"`
	Threshold    float64   `json:"threshold"`
	Duration     string    `json:"duration"`
	Severity     Severity  `json:"severity"`
	Action       string    `json:"action"`
	ActionConfig []byte    `json:"action_config,omitempty"`
	Enabled      bool      `json:"enabled"`
	Cooldown     string    `json:"cooldown"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const (
	OpGte = "gte"
	OpLte = "lte"
	OpEq  = "eq"
	OpNe  = "ne"
)

const (
	ActionRestart  = "restart"
	ActionScaleUp  = "scale_up"
	ActionNotify   = "notify"
	ActionFailover = "failover"
	ActionRecover  = "auto_recover"
	ActionScript   = "run_script"
)

type ActionLog struct {
	ID          int64      `json:"id"`
	EventID     int64      `json:"event_id"`
	Action      string     `json:"action"`
	Status      string     `json:"status"`
	Result      string     `json:"result,omitempty"`
	DurationMs  int64      `json:"duration_ms"`
	TriggeredAt time.Time  `json:"triggered_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type DashboardStats struct {
	TotalEvents    int              `json:"total_events"`
	OpenEvents     int              `json:"open_events"`
	Resolved24h    int              `json:"resolved_24h"`
	BySeverity     map[Severity]int `json:"by_severity"`
	BySource       map[string]int   `json:"by_source"`
	AvgResolveMins float64          `json:"avg_resolve_mins"`
}
