package guardian

import (
	"context"
	"log/slog"
	"time"
)

// AuditEvent 安全审计事件
type AuditEvent struct {
	TenantID string    `json:"tenant_id"`
	Guard    string    `json:"guard"`
	Action   string    `json:"action"`
	Message  string    `json:"message"`
	Blocked  bool      `json:"blocked,omitempty"`
	Time     time.Time `json:"time"`
}

// Auditor 安全审计器
//
// 负责记录安全决策事件到结构化日志，供事后审计与告警。
// 后续可扩展为写入 ES / ClickHouse 等分析后端。
type Auditor struct {
	logger *slog.Logger
}

// NewAuditor 创建审计器
func NewAuditor(logger *slog.Logger) *Auditor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Auditor{logger: logger}
}

// Log 记录审计事件
func (a *Auditor) Log(ctx context.Context, event *AuditEvent) {
	if event == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}

	level := slog.LevelInfo
	if event.Blocked {
		level = slog.LevelWarn
	}

	a.logger.LogAttrs(ctx, level,
		"security audit",
		slog.String("tenant_id", event.TenantID),
		slog.String("guard", event.Guard),
		slog.String("action", event.Action),
		slog.String("message", event.Message),
		slog.Bool("blocked", event.Blocked),
		slog.Time("time", event.Time),
	)
}

// LogVerdicts 批量记录多个守卫判定
func (a *Auditor) LogVerdicts(ctx context.Context, tenantID string, verdicts []*GuardVerdict) {
	for _, v := range verdicts {
		a.Log(ctx, &AuditEvent{
			TenantID: tenantID,
			Guard:    v.GuardName,
			Action:   string(v.Action),
			Message:  v.Message,
			Blocked:  v.Action == ActionBlock,
		})
	}
}
