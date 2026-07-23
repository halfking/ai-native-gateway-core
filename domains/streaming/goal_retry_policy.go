package streaming

import (
	"context"
	"time"
)

// GoalRetryPolicy is an immutable per-request retry policy resolved from
// tenant settings and cost-mode presets. It decouples the streaming layer
// from direct settings.Global access and provides a stable snapshot for
// the entire request lifecycle.
type GoalRetryPolicy struct {
	CostMode     string
	Enabled      bool
	MaxRetries   int
	TotalTimeout time.Duration
	BaseDelay    time.Duration
	MaxDelay     time.Duration
}

// GoalRetryPolicyResolver resolves tenant-scoped retry policy.
// Implementations should read from settings registry and apply cost-mode presets.
type GoalRetryPolicyResolver interface {
	ResolveGoalRetryPolicy(tenantID string) GoalRetryPolicy
}

// GoalRetryRecorder persists actual retry attempts to goal_sessions.retry_count.
// Implementations must be fail-open: persistence errors should not block requests.
type GoalRetryRecorder interface {
	AddRetryCount(ctx context.Context, sessionID string, delta int) error
}

// defaultGoalRetryPolicy returns a safe fallback when resolver is unavailable.
// Matches settings spec defaults: minimal mode, 2 retries, 40s timeout.
func defaultGoalRetryPolicy() GoalRetryPolicy {
	return GoalRetryPolicy{
		CostMode:     "minimal",
		Enabled:      true,
		MaxRetries:   2,
		TotalTimeout: 40 * time.Second,
		BaseDelay:    100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
	}
}

// Normalize ensures policy values are safe and consistent.
// Clamps illegal values to safe bounds rather than failing the request.
func (p GoalRetryPolicy) Normalize() GoalRetryPolicy {
	if p.MaxRetries < 0 {
		p.MaxRetries = 0
	}
	if p.TotalTimeout <= 0 {
		p.TotalTimeout = 40 * time.Second
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 100 * time.Millisecond
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}
