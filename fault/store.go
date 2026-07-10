package fault

import "context"

type Store interface {
	CreateEvent(ctx context.Context, event *Event) error
	GetEvent(ctx context.Context, eventID int64) (*Event, error)
	GetOpenEventsByRule(ctx context.Context, ruleID int64) ([]Event, error)
	UpdateEventStatus(ctx context.Context, eventID int64, status EventStatus, actor string) error
	ListEvents(ctx context.Context, status EventStatus, offset, limit int) ([]Event, int, error)

	CreateRule(ctx context.Context, rule *Rule) error
	GetRule(ctx context.Context, ruleID int64) (*Rule, error)
	UpdateRule(ctx context.Context, rule *Rule) error
	DeleteRule(ctx context.Context, ruleID int64) error
	ListActiveRules(ctx context.Context) ([]Rule, error)
	ListAllRules(ctx context.Context, offset, limit int) ([]Rule, int, error)

	CreateActionLog(ctx context.Context, log *ActionLog) error
	GetActionLogs(ctx context.Context, eventID int64) ([]ActionLog, error)
	UpdateActionLog(ctx context.Context, logID int64, status, result string) error

	GetDashboardStats(ctx context.Context) (*DashboardStats, error)
}
