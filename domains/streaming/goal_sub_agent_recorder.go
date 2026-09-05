package streaming

import "context"

// GoalSubAgentRecorder persists client-reported sub-agent snapshots without
// making the reporting path part of the request's critical path.
type GoalSubAgentRecorder interface {
	RecordSubAgents(ctx context.Context, tenantID, sessionID string, total, completed, pending int) error
}
