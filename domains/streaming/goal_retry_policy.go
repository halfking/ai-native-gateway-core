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
	AddRetryCount(ctx context.Context, tenantID, sessionID string, delta int) error
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
//
// 注意：Enabled 的判读不在此处统一处理（零值未初始化 = false，会误伤未写 Enabled
// 的合法策略），改由调用方在使用前根据策略来源显式分支。handler 通过
// EffectiveMaxRetries() 收敛「Enabled=false ⇒ MaxRetries=0」的不变量。
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

// EffectiveMaxRetries returns the MaxRetries actually used by the retry loop.
// 当 Enabled=false 时强制返回 0（仅一次执行），防止下游直接读取 MaxRetries
// 绕过关闭开关、跑满指数退避（2026-07-24 审计修复）。
func (p GoalRetryPolicy) EffectiveMaxRetries() int {
	if !p.Enabled {
		return 0
	}
	return p.MaxRetries
}
