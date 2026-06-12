package routing

import (
	"context"
	"errors"
	"time"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// CommonExecutor holds the protocol-agnostic state and logic for
// executing a request against a credential: retry budget, circuit
// breaker, timeout, credential state writes. It is the substrate
// that protocol-specific executors (chat, anthropic) compose onto.
//
// Phase 1 keeps the surface intentionally small: only the retry loop
// and circuit/limiter handles used by RunWithCredential are wired up.
// The wider field set is declared so future phases (P2 onwards) can
// grow the abstraction without re-plumbing the type.
type CommonExecutor struct {
	Circuit              *circuit.Manager
	Limiter              *limiter.Limiter
	Pools                *pool.PoolManager
	State                *credentialstate.Writer
	HeaderProfiles       *HeaderProfileCache
	Upstream             *upstream.Client
	FpSlots              *credentialfpslot.Manager
	UpstreamTimeout      time.Duration
	StreamTimeout        time.Duration
	StreamRetryThreshold int

	// Internal: identifies the (provider, credential) pair this
	// CommonExecutor instance is bound to. RunWithCredential uses
	// these to record circuit success/failure. Lower-case because
	// they are only meant to be set by the surrounding Executor /
	// protocol-specific executor at composition time.
	providerID   int
	credentialID int
}

// SetProviderCredential binds the CommonExecutor instance to a
// specific (providerID, credentialID) pair so RunWithCredential can
// record circuit success/failure. Called by the surrounding Executor
// before invoking a protocol-specific executor.
func (c *CommonExecutor) SetProviderCredential(providerID, credentialID int) {
	c.providerID = providerID
	c.credentialID = credentialID
}

// AttemptFunc is the per-attempt callback passed to RunWithCredential.
// It receives the 0-indexed attempt number and returns nil on success,
// a *retryableError to signal "try again", or any other error to give
// up immediately. Implementations are responsible for building the
// request, sending it, and surfacing the result.
type AttemptFunc func(attempt int) error

// RunWithCredential drives the "try, fail, retry, give up" loop for a
// single credential. It calls attempt() up to maxRetries+1 times. The
// common executor handles:
//   - per-attempt exponential backoff
//   - circuit breaker RecordSuccess / RecordFailure
//   - context cancellation
//   - retry budget exhaustion
//
// It does NOT handle: per-credential concurrency limiting (caller's
// job — already done by Executor.Execute before we get here),
// request/response building (caller's job), or credential state writes
// for confirmed failures (caller's job — the existing Executor.Execute
// loop already has the policy).
func (c *CommonExecutor) RunWithCredential(ctx context.Context, attempt AttemptFunc) error {
	const maxRetries = 2
	for n := 0; n <= maxRetries; n++ {
		if n > 0 {
			delay := time.Duration(500*(1<<(n-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err := attempt(n)
		if err == nil {
			if c.Circuit != nil {
				c.Circuit.RecordSuccess(c.providerID, c.credentialID)
			}
			return nil
		}
		if !isRetryableError(err) {
			return err
		}
		if c.Circuit != nil {
			c.Circuit.RecordFailure(c.providerID, c.credentialID, classifyKind(err))
		}
	}
	return errors.New("exhausted retries")
}

// isRetryableError reports whether err signals "try again" via the
// retryableError sentinel. This is the type defined in executor.go
// alongside Executor; the common executor reuses it so callers do
// not need a second type.
func isRetryableError(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

// classifyKind extracts a circuit-friendly ErrorKind from a retryable
// error. The existing *retryableError wraps any error (which may
// itself be a *upstreampkg.Error carrying a Kind); we try that path
// first, then fall back to KindTransient for opaque retryable errors.
func classifyKind(err error) errorsx.ErrorKind {
	var re *retryableError
	if errors.As(err, &re) {
		// Walk the wrapped chain to surface a typed Kind if present.
		inner := re.err
		if inner != nil {
			// errorsx.ClassifyError returns KindCanceled / KindNetwork /
			// KindTimeout / etc. based on the error text. For the retry
			// path the outer code has already classified; the inner
			// error carries the raw signal, so we let classify look at it.
			kind := errorsx.ClassifyError(inner, nil)
			if kind != "" {
				return kind
			}
		}
		return errorsx.KindTransient
	}
	return errorsx.KindTransient
}
