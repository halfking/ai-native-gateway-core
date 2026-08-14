package streaming

import (
	"context"
)

// attempt_gate_context.go — SR-W1 plumbing
//
// Threads the per-attempt AttemptCommitGate from the handler layer down to
// bridge call sites without changing bridge signatures: the survival
// coordinator stamps the gate onto the attempt context, and any code that
// needs the commit state (W2's ExecuteAttempt adapter, outcome aggregation,
// metrics) reads it back. With request survival disabled the gate is absent
// and every reader takes the legacy path — zero behavior change.

type attemptGateCtxKey struct{}

// SetAttemptGateOnContext stamps the attempt's commit gate onto ctx.
func SetAttemptGateOnContext(ctx context.Context, gate *AttemptCommitGate) context.Context {
	return context.WithValue(ctx, attemptGateCtxKey{}, gate)
}

// AttemptGateFromContext returns the attempt's commit gate, or nil when
// request survival is disabled for this attempt.
func AttemptGateFromContext(ctx context.Context) *AttemptCommitGate {
	gate, _ := ctx.Value(attemptGateCtxKey{}).(*AttemptCommitGate)
	return gate
}
