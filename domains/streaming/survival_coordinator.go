package streaming

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
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
//
// Termination priority (会话优化 v4 §8-14 / R2.4, T3): 组合穷尽 > 2h 时限 >
// 100 次预算 — the deadline is the STRONGER stop condition versus the retry
// budget (worst-case 100 × 120s backoff ≈ 3.2h exceeds it).
type SurvivalOptions struct {
	// Deadline is the total in-connection budget. 0 → 2 hours (v4 T3:
	// 24h → 2h, aligned with the 2h request-cache TTL; env override
	// LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS still wins
	// through the config wiring in cmd/gateway/main.go).
	Deadline time.Duration
	// RetryBase is the first backoff step. 0 → 2s.
	RetryBase time.Duration
	// RetryMax caps the backoff growth. 0 or values above 120s → 120s.
	RetryMax time.Duration
	// MaxRetries is the retry budget after the initial attempt. 0 → 100.
	MaxRetries int
	// KeepaliveInterval controls connection heartbeats while waiting. 0 → 15s.
	KeepaliveInterval time.Duration
}

func (o SurvivalOptions) withDefaults() SurvivalOptions {
	if o.Deadline <= 0 {
		o.Deadline = 2 * time.Hour
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 2 * time.Second
	}
	if o.RetryMax <= 0 || o.RetryMax > 2*time.Minute {
		o.RetryMax = 2 * time.Minute
	}
	if o.MaxRetries <= 0 || o.MaxRetries > executors.DefaultUpstreamAttemptLimit {
		o.MaxRetries = executors.DefaultUpstreamAttemptLimit
	}
	if o.KeepaliveInterval <= 0 {
		o.KeepaliveInterval = 15 * time.Second
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
	// JitterRand is the ±20% backoff jitter seam (T3, anti retry-storm;
	// mirrors legacy handler.go calculateRetryDelay). nil → math/rand.
	// Float64() ∈ [0,1): 0 → −20%, 1 → +20%, 0.5 → unchanged.
	JitterRand func() float64
	// Refresh re-resolves the candidate list between attempts (the
	// executor's internal refresh ladder is suppressed under survival).
	Refresh func(ctx context.Context)
	// Keepalive emits a transport-level comment through the shared writer.
	// Nil means the coordinator uses TransportHeartbeat or its default frame.
	Keepalive func()
	// TransportHeartbeat is the error-preserving heartbeat owner supplied by
	// the HTTP session. It bypasses semantic/durable capture while sharing the
	// connection's serialized writer.
	TransportHeartbeat func() error
	// Terminal renders the final protocol frame(s) once the task ends.
	// committed reports whether the client already saw semantic output
	// (resume-blocked) and therefore needs a well-formed stream ending
	// rather than a bare error frame.
	Terminal func(decision TaskDecision, committed bool)
	// BeforeSemanticCommit runs before the first semantic frame reaches the client.
	// It doubles as the durable write-ahead checkpoint hook (SR-W3): the
	// survival wiring adapts the DurableStreamBinding into this seam.
	BeforeSemanticCommit func(ctx context.Context, state CommitState) error
	// Reschedule persists the next runnable time while the current lease is valid.
	Reschedule func(ctx context.Context, nextRetryAt time.Time, reason string) error
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
func (c *SurvivalCoordinator) keepalive(sw *SerializedStreamWriter) error {
	if c.Keepalive != nil {
		c.Keepalive()
		return nil
	}
	if c.TransportHeartbeat != nil {
		if err := c.TransportHeartbeat(); err != nil {
			recordSurvivalKeepaliveWriteError(c.Protocol)
			return err
		}
		return nil
	}
	if sw != nil {
		frame := ": gw-survival-keepalive\n\n"
		if c.Protocol == ProtocolAnthropic {
			frame = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
		}
		if _, err := sw.Write([]byte(frame)); err != nil {
			recordSurvivalKeepaliveWriteError(c.Protocol)
			return err
		}
		if err := sw.FlushError(); err != nil {
			recordSurvivalKeepaliveWriteError(c.Protocol)
			return err
		}
	}
	return nil
}

func (c *SurvivalCoordinator) waitWithKeepalive(ctx context.Context, sw *SerializedStreamWriter, wait, interval time.Duration) error {
	if err := c.keepalive(sw); err != nil {
		return err
	}
	if c.Sleep != nil || wait <= interval {
		return c.sleep(ctx, wait)
	}
	timer := time.NewTimer(wait)
	ticker := time.NewTicker(interval)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			if err := c.keepalive(sw); err != nil {
				return err
			}
		}
	}
}

// Run drives the recovery loop until the task reaches a terminal state,
// the deadline expires or ctx is cancelled. sw is the shared serialized
// writer over the real connection; the caller owns its construction.
func (c *SurvivalCoordinator) Run(ctx context.Context, sw *SerializedStreamWriter, params *executors.ExecParams) SurvivalResult {
	opts := c.Options.withDefaults()
	if params.UpstreamAttempts == nil {
		params.UpstreamAttempts = executors.NewUpstreamAttemptBudget(opts.MaxRetries)
	}
	deadline := c.now().Add(opts.Deadline)
	backoff := opts.RetryBase

	res := SurvivalResult{}
	// recoveryStart anchors gateway_survival_recovery_latency_seconds: the
	// instant the task saw its first recoverable failure.
	var recoveryStart time.Time

	// 2026-08-19 observability: every decision in this loop is now
	// greppable by request_id via the streamLogger; surviving supervisors
	// can reconstruct the full attempt history (committed?, kinds?,
	// buffer_bytes at discard) without an audit table round-trip.
	log := streamLogFromContext(ctx, nil).With(
		"protocol", c.Protocol.String(),
	)

	for {
		gate := NewAttemptCommitGate(c.Protocol, sw, GateOptions{Mode: GateModeBuffered, BeforeSemanticCommit: func(state CommitState) error {
			if c.BeforeSemanticCommit == nil {
				return nil
			}
			return c.BeforeSemanticCommit(ctx, state)
		}})

		gw := NewGateWriterWithResponse(gate, params.W)
		attemptParams := *params
		attemptParams.W = gw
		res.FinalAttempt = ExecuteAttempt(ctx, c.Exec, gate, &attemptParams)
		res.Attempts++
		recordSurvivalAttempt(res.FinalAttempt)
		res.Decision = AggregateTaskOutcome(res.FinalAttempt)

		// 2026-08-19: log the per-attempt verdict right after aggregation so
		// the committed/resume-blocked transition is visible regardless of
		// which branch we take next. Keep kinds as a comma-joined string to
		// keep structured-log keys stable across Prometheus label values.
		attemptKinds := make([]string, 0, len(res.FinalAttempt.CandidateOutcomes))
		lastProviderID := 0
		lastRawModel := ""
		for _, co := range res.FinalAttempt.CandidateOutcomes {
			if co.Kind != "" {
				attemptKinds = append(attemptKinds, string(co.Kind))
			}
			if co.ProviderID != 0 {
				lastProviderID = co.ProviderID
			}
		}
		// ExecResult carries the success-path candidate; CandidateOutcome
		// carries the failure-path provider/model attributes. Prefer
		// whichever is non-empty so the log line always has *something*
		// to attribute the failure to.
		if res.FinalAttempt.ExecResult != nil {
			c := res.FinalAttempt.ExecResult.Candidate
			if c.RawModel != "" || c.ProviderID != 0 {
				lastRawModel = c.RawModel
			}
		}
		if lastRawModel == "" {
			for _, co := range res.FinalAttempt.CandidateOutcomes {
				if co.CandidateID != "" {
					// CandidateID is set by foldCandidateOutcomes as
					// "provider:N/model:M"; pull the model suffix when
					// it's the only attribution we have.
					if idx := strings.Index(co.CandidateID, "/model:"); idx >= 0 {
						lastRawModel = co.CandidateID[idx+len("/model:"):]
						break
					}
				}
			}
		}
		log.Info("survival_attempt_outcome",
			"attempt", res.Attempts,
			"committed", res.FinalAttempt.CommitState >= CommitStateContent,
			"commit_state", res.FinalAttempt.CommitState.String(),
			"kinds", strings.Join(attemptKinds, ","),
			"candidate_count", len(res.FinalAttempt.CandidateOutcomes),
			"action", res.Decision.Action.String(),
			"reason", res.Decision.Reason,
			"provider_id", lastProviderID,
			"raw_model", lastRawModel,
		)

		switch res.Decision.Action {
		case TaskActionSucceed:
			if err := finishGateWriter(gw, gate); err != nil {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
				)
				return res
			}
			res.Succeed = true
			recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			if !recoveryStart.IsZero() {
				observeSurvivalRecoveryLatency(c.Protocol, c.now().Sub(recoveryStart))
			}
			log.Info("survival_task_ended",
				"attempt", res.Attempts,
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
				"committed", res.FinalAttempt.CommitState >= CommitStateContent,
				"succeed", true,
			)
			return res

		case TaskActionRetryNow, TaskActionWaitRecovery:
			if params.UpstreamAttempts != nil && params.UpstreamAttempts.Exhausted() {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "attempt_limit_exceeded"}
				c.renderTerminal(res.Decision, gate)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
					"provider_id", lastProviderID,
					"raw_model", lastRawModel,
				)
				return res
			}
			if res.Attempts-1 >= opts.MaxRetries {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "retry_limit_exceeded"}
				c.renderTerminal(res.Decision, gate)
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
					"provider_id", lastProviderID,
					"raw_model", lastRawModel,
				)
				return res
			}
			// Uncommitted by construction (the aggregator upgrades
			// committed+recoverable to ResumeBlocked); Discard defensively
			// and treat any surprise as terminal.
			bufferBytes, holdbackHeld, gateState := gate.Snapshot()
			gateStateStr := gateState.String()
			if err := gate.Discard(); err != nil {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "discard_refused"}
				c.renderTerminal(res.Decision, gate)
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Error("survival_discard_refused",
					"attempt", res.Attempts,
					"buffer_bytes", bufferBytes,
						"holdback_held", holdbackHeld,
						"state", gateState,
						"provider_id", lastProviderID,
						"raw_model", lastRawModel,
					)
				return res
			}
			log.Info("survival_attempt_discarded",
				"attempt", res.Attempts,
				"buffer_bytes", bufferBytes,
				"holdback_held", holdbackHeld,
				"state", gateState,
				"provider_id", lastProviderID,
				"raw_model", lastRawModel,
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
			)
			// Record the discard into the audit capture so request_logs_hot
			// .discard_events JSONB column carries the buffer size and
			// decision context for offline post-mortem.
			if params.Capture != nil {
				params.Capture.MarkDiscarded(audit.DiscardEvent{
					Reason:         "survival_attempt_discarded",
					BufferBytes:    bufferBytes,
					HoldbackHeld:   holdbackHeld,
					State:          gateStateStr,
					AttemptNumber:  res.Attempts,
					ProviderID:     lastProviderID,
					RawModel:       lastRawModel,
					DecisionAction: res.Decision.Action.String(),
					DecisionReason: res.Decision.Reason,
				})
			}
			if c.now().After(deadline) {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
				c.renderTerminal(res.Decision, gate)
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
					"provider_id", lastProviderID,
					"raw_model", lastRawModel,
				)
				return res
			}
			wait := backoff
			if res.Decision.Action == TaskActionWaitRecovery &&
				res.Decision.NextRetryAfter > wait && res.Decision.NextRetryAfter <= opts.RetryMax {
				wait = res.Decision.NextRetryAfter
			}
			// ±20% jitter on the final backoff (Retry-After adopted values
			// included) spreads retry storms across clients (T3).
			wait = applyBackoffJitter(wait, c.JitterRand)
			if recoveryStart.IsZero() {
				recoveryStart = c.now()
			}
			waitState := survivalStateRetryNow
			if res.Decision.Action == TaskActionWaitRecovery {
				waitState = survivalStateWaiting
			}
			if c.Reschedule != nil {
				if err := c.Reschedule(ctx, c.now().Add(wait), res.Decision.Reason); err != nil {
					res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "durable_reschedule_failed"}
					c.renderTerminal(res.Decision, gate)
					recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
					recordSurvivalRequestTerminal(c.Protocol, res.Decision)
					log.Error("survival_task_ended",
						"attempt", res.Attempts,
						"action", res.Decision.Action.String(),
						"reason", res.Decision.Reason,
						"committed", res.FinalAttempt.CommitState >= CommitStateContent,
						"succeed", false,
						"provider_id", lastProviderID,
						"raw_model", lastRawModel,
					)
					return res
				}
			}
			recordSurvivalTransition(survivalStateRunning, waitState, res.Decision.Reason)
			if err := c.waitWithKeepalive(ctx, sw, wait, opts.KeepaliveInterval); err != nil {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
				recordSurvivalTransition(waitState, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
				)
				return res
			}
			if res.Decision.Action == TaskActionWaitRecovery && res.FinalAttempt != nil {
				observeSurvivalWait(res.FinalAttempt.LastKind(), wait)
			}
			recordSurvivalTransition(waitState, survivalStateRunning, "retry")
			if c.Refresh != nil {
				c.Refresh(ctx)
			}
			backoff *= 2
			if backoff > opts.RetryMax {
				backoff = opts.RetryMax
			}
			log.Debug("survival_attempt_recovery_resumed",
				"attempt", res.Attempts,
				"wait_ms", wait.Milliseconds(),
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
			)
			continue

		default: // ResumeBlocked / FailTerminal / FailClosed
			_ = finishGateWriter(gw, gate)
			c.renderTerminal(res.Decision, gate)
			recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
			recordSurvivalResumeSafetyBlocked(res.Decision, res.FinalAttempt)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			// 2026-08-19: structured log alongside the SSE frame the client
			// receives. This is the single line operators should grep for
			// the "gateway_survival_resume_blocked / gateway request
			// survival ended: committed_output" failure class — it carries
			// the same fields the SSE envelope does plus the
			// request-correlation context the envelope cannot.
			log.Warn("survival_resume_blocked",
				"attempt", res.Attempts,
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
				"committed", res.FinalAttempt.CommitState >= CommitStateContent,
				"commit_state", res.FinalAttempt.CommitState.String(),
				"kinds", strings.Join(attemptKinds, ","),
				"provider_id", lastProviderID,
				"raw_model", lastRawModel,
			)
			return res
		}
	}
}

// applyBackoffJitter spreads one backoff step by ±20% (T3, 防重试风暴),
// mirroring the legacy handler retry path (handler.go calculateRetryDelay):
//
//	jitter = d × 0.2 × (2·r − 1)   // r ∈ [0,1) → −20%..+20%
//	final  = d + jitter            // clamped to [0.8·d, 1.2·d], never < 0
//
// randFloat nil → math/rand.Float64. Pure and deterministic under a fixed
// seam so tests pin the exact boundaries.
func applyBackoffJitter(d time.Duration, randFloat func() float64) time.Duration {
	if d <= 0 {
		return d
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	factor := 1 + 0.2*(2*randFloat()-1) // ∈ [0.8, 1.2)
	jittered := time.Duration(float64(d) * factor)
	if jittered < 0 {
		return 0
	}
	return jittered
}

// finishGateWriter flushes any trailing partial frame at attempt end
// (A-P2-5: line-protocol bytes must never be dropped) — but only when the
// attempt committed; a discarded attempt owns nothing on the wire. Finish
// routes through AttemptCommitGate.FinishAttempt, which itself refuses
// trailing bytes on a discarded attempt.
func finishGateWriter(w interface{ Finish() error }, gate *AttemptCommitGate) error {
	if gate != nil && gate.Committed() {
		return w.Finish()
	}
	return nil
}

func (c *SurvivalCoordinator) renderTerminal(decision TaskDecision, gate *AttemptCommitGate) {
	if c.Terminal == nil {
		return
	}
	committed := gate != nil && gate.Committed()
	c.Terminal(decision, committed)
}
