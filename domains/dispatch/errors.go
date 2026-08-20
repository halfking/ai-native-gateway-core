package dispatch

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Hard limits that bound a single request's lifetime in the pipeline.
const (
	// MaxNodeFailures is the maximum number of failed upstream calls allowed on
	// one node for one request before dispatch switches to a sibling node.
	MaxNodeFailures = 3
	// maxAttempts is the absolute ceiling on total forward attempts across all
	// credentials/models. Prevents a pathological request from churning the
	// whole candidate set; well above any realistic candidate count × retry.
	maxAttempts = 100
	// maxRetryBudget is a sentinel used to force-skip same-credential retry
	// (e.g. on pacing timeout where retrying a saturated credential is futile).
	maxRetryBudget = 1 << 30

	// DefaultOverflowRetryAfter is the suggested client backoff carried by
	// OverflowError (spec gap G9 / N5): without it clients storm-retry a full
	// gateway and turn a queue spike into a retry avalanche.
	DefaultOverflowRetryAfter = 1 * time.Second

	// Retry scheduling backoff ladder (R2.4 / T3-8), aligned with the
	// five-second minimum retry policy. Upstream Retry-After hints, when supplied via
	// ForwardOutcome.RetryAfter, take priority over the ladder.
	retryBaseDelay = 5 * time.Second
	retryMaxDelay  = 120 * time.Second
)

// Sentinel errors produced by the dispatch pipeline. The executor maps these
// to HTTP responses (e.g. ErrNoRoute → 503 + Retry-After).

// errPaceTimeout is returned by a governor when the pacing wait exceeds the
// request's queue-wait budget. The forwarder routes the request to the
// failover mover (try another credential / model).
var errPaceTimeout = errors.New("dispatch: pacing wait exceeded queue budget")

// ErrNoRoute is returned to the Submit caller when no model/credential path
// is available (all tried, or no candidates and model-change disabled).
var ErrNoRoute = errors.New("dispatch: no routable credential/model available")

// ErrOverflow is returned when the entry (Tier-1 model) queue is full and the
// request cannot be admitted.
var ErrOverflow = errors.New("dispatch: entry queue full")

// ErrShutdown is returned when Submit is called after the pipeline stopped.
var ErrShutdown = errors.New("dispatch: pipeline shut down")

// IsPaceTimeout reports whether err is the governor pacing-timeout sentinel.
func IsPaceTimeout(err error) bool { return errors.Is(err, errPaceTimeout) }

// IsShutdown reports whether err is the pipeline-shutdown sentinel.
func IsShutdown(err error) bool { return errors.Is(err, ErrShutdown) }

// IsOverflow reports whether err is a queue-admission overflow (R1.3).
func IsOverflow(err error) bool { return errors.Is(err, ErrOverflow) }

// OverflowError is the R1.3 admission-refusal error: a dispatch queue (or the
// registry's unfinished watermark, R1.8) is full and the request is rejected
// IMMEDIATELY — zero queue wait — with a suggested Retry-After (G9) so
// well-behaved clients back off instead of storm-retrying.
//
// It wraps the ErrOverflow sentinel, so errors.Is(err, ErrOverflow) keeps
// matching the executor's existing mapping (dispatchErrToExecuteError →
// *ExecuteError{Exhausted: true} → 503 + Retry-After).
type OverflowError struct {
	// Reason is a bounded machine-readable cause
	// (model_queue_full | cred_queue_full | registry_unfinished_full).
	Reason string
	// RetryAfter is the suggested client backoff; > 0 in practice.
	RetryAfter time.Duration
}

func (e *OverflowError) Error() string {
	if e == nil {
		return ErrOverflow.Error()
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("dispatch: entry queue full (%s), retry after %s", e.Reason, e.RetryAfter)
	}
	return fmt.Sprintf("dispatch: entry queue full (%s)", e.Reason)
}

// Unwrap anchors the error to the ErrOverflow sentinel.
func (e *OverflowError) Unwrap() error { return ErrOverflow }

// ExhaustionAttempt is one tried model×node combination in an aggregate
// exhaustion summary (R2.4 / UT-FO-05).
type ExhaustionAttempt struct {
	// Model is the resolved model of the attempt.
	Model string
	// ProviderID identifies the routing provider (0 when unknown).
	ProviderID int64
	// CredentialID identifies the upstream credential/node.
	CredentialID int64
	// Reason is the bounded failure classification for that attempt.
	Reason string
}

// ExhaustedError marks combination exhaustion: every candidate model × node
// the ladder could reach has been tried (R2.4 — combination exhaustion takes
// TERMINATION PRIORITY over the 100-attempt budget). It carries the aggregate
// summary (models/nodes/reasons) the spec requires in the terminal error
// body; per ADR-Disp-006 the executor maps it to *ExecuteError{Exhausted}
// (503 + Retry-After) exactly like the legacy exhaustion shape.
//
// Error() and Unwrap() deliberately delegate to the preserved cause so
// callers that pin the concrete upstream error (see TestNoRoute /
// TestLastUpstreamErrorPreserved) keep working; the summary is additive.
type ExhaustedError struct {
	// Cause is the last concrete failure (usually the original upstream
	// error); ErrNoRoute when the request never routed.
	Cause error
	// Attempts is the aggregate model/node/reason summary.
	Attempts []ExhaustionAttempt
}

func (e *ExhaustedError) Error() string {
	if e == nil {
		return ErrNoRoute.Error()
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return ErrNoRoute.Error()
}

// Unwrap preserves errors.Is matching against the concrete cause.
func (e *ExhaustedError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrNoRoute
	}
	return e.Cause
}

// Summary renders the aggregate attempt summary for logs/error envelopes.
func (e *ExhaustedError) Summary() string {
	if e == nil || len(e.Attempts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		parts = append(parts, fmt.Sprintf("%s#%d:%s", a.Model, a.CredentialID, a.Reason))
	}
	return strings.Join(parts, ", ")
}

// AsExhaustedError extracts the aggregate summary when err is (or wraps) an
// ExhaustedError. Exported so the executor layer can surface the summary in
// its *ExecuteError{Exhausted} envelope without duplicating dispatch types.
func AsExhaustedError(err error) (*ExhaustedError, bool) {
	var exhausted *ExhaustedError
	if errors.As(err, &exhausted) {
		return exhausted, true
	}
	return nil, false
}
