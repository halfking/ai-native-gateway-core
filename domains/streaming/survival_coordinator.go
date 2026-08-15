package streaming

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// survival_coordinator.go — SR-06 (doc 18 §5.1, §7, §8, §9.1)
//
// SurvivalCoordinator is the in-connection recovery loop for request
// survival (Phase 1: pre-first-content recovery only). It owns the commit
// decision window: one fresh AttemptCommitGate (buffered mode) per attempt
// over the shared SerializedStreamWriter, ExecuteAttempt for the bounded
// pass, AggregateTaskOutcome for the task-level verdict.
//
//   - retry-now: discard the uncommitted attempt, refresh candidates,
//     back off (base), retry immediately after;
//   - wait-recovery: emit a keepalive comment, sleep the backoff window,
//     refresh candidates, retry;
//   - resume-blocked / fail-terminal / fail-closed: hand the final
//     protocol rendering to the Terminal seam and stop.
//
// Every loop iteration checks the interactive deadline. The coordinator
// never touches the legacy flag-off path: it is constructed only by the
// survival wiring (SR-07) when request survival is enabled for the tenant.

// SurvivalOptions bounds the coordinator loop (doc 18 §5.1 interactive
// deadline, §8.4 retry delay, §8.5 storm prevention).
type SurvivalOptions struct {
	// Deadline is the total in-connection budget. 0 → 30 minutes.
	Deadline time.Duration
	// RetryBase is the first backoff step. 0 → 2s.
	RetryBase time.Duration
	// RetryMax caps the backoff growth. 0 → 300s.
	RetryMax time.Duration
}

func (o SurvivalOptions) withDefaults() SurvivalOptions {
	if o.Deadline <= 0 {
		o.Deadline = 30 * time.Minute
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 2 * time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 300 * time.Second
	}
	return o
}

// SurvivalResult is the coordinator's final verdict for the task.
type SurvivalResult struct {
	Succeed bool
	// Decision is the TaskDecision of the last attempt (or the synthetic
	// deadline-exceeded verdict).
	Decision TaskDecision
	// FinalAttempt is the last AttemptResult (nil only if the coordinator
	// never ran an attempt, e.g. immediate deadline).
	FinalAttempt *AttemptResult
	// Attempts counts bounded executor passes.
	Attempts int
}

// SurvivalCoordinator drives one request's in-connection recovery loop.
// The exported seam fields are nil-safe: production wiring supplies all of
// them; tests substitute bounded fakes.
type SurvivalCoordinator struct {
	// Exec runs one bounded attempt (SR-05 adapter target).
	Exec AttemptExecutor
	// Protocol is the CLIENT wire protocol used to build per-attempt gates.
	Protocol ClientProtocol
	// Options bound the loop.
	Options SurvivalOptions

	// Now / Sleep are the clock seams. Sleep must honor ctx cancellation.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// Refresh re-resolves the candidate list between attempts (the
	// executor's internal refresh ladder is suppressed under survival).
	Refresh func(ctx context.Context)
	// Keepalive emits a transport-level comment through the shared writer.
	// Nil → the coordinator writes the default comment itself.
	Keepalive func()
	// Terminal renders the final protocol frame(s) once the task ends.
	// committed reports whether the client already saw semantic output
	// (resume-blocked) and therefore needs a well-formed stream ending
	// rather than a bare error frame.
	Terminal func(decision TaskDecision, committed bool)
}

func (c *SurvivalCoordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *SurvivalCoordinator) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// keepalive emits a transport-level SSE comment through the shared writer
// (doc 18 §9.1 — strict clients only ever receive comments from the
// survival loop). The Keepalive seam overrides the default rendering.
func (c *SurvivalCoordinator) keepalive(sw *SerializedStreamWriter) {
	if c.Keepalive != nil {
		c.Keepalive()
		return
	}
	if sw != nil {
		if _, err := sw.Write([]byte(": gw-survival-keepalive\n\n")); err == nil {
			sw.Flush()
		}
	}
}

// Run drives the recovery loop until the task reaches a terminal state,
// the deadline expires or ctx is cancelled. sw is the shared serialized
// writer over the real connection; the caller owns its construction.
func (c *SurvivalCoordinator) Run(ctx context.Context, sw *SerializedStreamWriter, params *executors.ExecParams) SurvivalResult {
	opts := c.Options.withDefaults()
	deadline := c.now().Add(opts.Deadline)
	backoff := opts.RetryBase

	res := SurvivalResult{}

	for {
		gate := NewAttemptCommitGate(c.Protocol, sw, GateOptions{Mode: GateModeBuffered})

		gw := NewGateWriterWithResponse(gate, params.W)
		attemptParams := *params
		attemptParams.W = gw
		res.FinalAttempt = ExecuteAttempt(ctx, c.Exec, gate, &attemptParams)
		res.Attempts++
		res.Decision = AggregateTaskOutcome(res.FinalAttempt)

		switch res.Decision.Action {
		case TaskActionSucceed:
			finishGateWriter(gw, gate)
			res.Succeed = true
			return res

		case TaskActionRetryNow, TaskActionWaitRecovery:
			// Uncommitted by construction (the aggregator upgrades
			// committed+recoverable to ResumeBlocked); Discard defensively
			// and treat any surprise as terminal.
			if err := gate.Discard(); err != nil {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "discard_refused"}
				c.renderTerminal(res.Decision, gate)
				return res
			}
			if c.now().After(deadline) {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
				c.renderTerminal(res.Decision, gate)
				return res
			}
			wait := backoff
			if res.Decision.Action == TaskActionWaitRecovery &&
				res.Decision.NextRetryAfter > wait && res.Decision.NextRetryAfter <= opts.RetryMax {
				wait = res.Decision.NextRetryAfter
			}
			c.keepalive(sw)
			if err := c.sleep(ctx, wait); err != nil {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
				return res
			}
			if c.Refresh != nil {
				c.Refresh(ctx)
			}
			backoff *= 2
			if backoff > opts.RetryMax {
				backoff = opts.RetryMax
			}
			continue

		default: // ResumeBlocked / FailTerminal / FailClosed
			finishGateWriter(gw, gate)
			c.renderTerminal(res.Decision, gate)
			return res
		}
	}
}

// finishGateWriter flushes any trailing partial frame at attempt end
// (A-P2-5: line-protocol bytes must never be dropped) — but only when the
// attempt committed; a discarded attempt owns nothing on the wire. Finish
// routes through AttemptCommitGate.FinishAttempt, which itself refuses
// trailing bytes on a discarded attempt.
func finishGateWriter(w interface{ Finish() error }, gate *AttemptCommitGate) {
	if gate != nil && gate.Committed() {
		w.Finish()
	}
}

func (c *SurvivalCoordinator) renderTerminal(decision TaskDecision, gate *AttemptCommitGate) {
	if c.Terminal == nil {
		return
	}
	committed := gate != nil && gate.Committed()
	c.Terminal(decision, committed)
}
