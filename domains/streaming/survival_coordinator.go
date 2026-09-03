package streaming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
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
//     wait for the configured recovery cadence, then retry;
//   - wait-recovery: emit a keepalive comment, sleep the recovery window,
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
// Termination priority: candidate exhaustion > the interactive deadline >
// the retry budget. The deadline is the stronger stop condition for a
// long-lived in-connection request.
type SurvivalOptions struct {
	// Deadline is the total in-connection budget. Zero defaults to five
	// hours, matching the interactive recovery policy.

	Deadline time.Duration
	// RetryBase is the first backoff step. Zero defaults to 30 seconds.

	RetryBase time.Duration
	// RetryMax caps the backoff growth. 0 or values above 120s → 120s.
	RetryMax time.Duration
	// MaxRetries is the retry budget after the initial attempt. 0 → 100.
	MaxRetries int
	// KeepaliveInterval controls connection heartbeats while waiting. 0 → 15s.
	KeepaliveInterval time.Duration
	// RetryInterval makes recovery pacing fixed instead of exponential when set.
	// It is intended for long-lived no-capacity recovery and is jittered by the
	// same bounded jitter policy as the legacy backoff.
	RetryInterval time.Duration
	// NightMaxRetries overrides MaxRetries from NightStartHour (inclusive) until
	// midnight in NightLocation. Zero keeps the ordinary MaxRetries value.
	NightMaxRetries int
	NightStartHour  int
	NightLocation   *time.Location
}

func (o SurvivalOptions) withDefaults() SurvivalOptions {
	if o.Deadline <= 0 {
		o.Deadline = 5 * time.Hour

	}
	if o.RetryBase <= 0 {
		o.RetryBase = 30 * time.Second
		// A zero-value options struct opts into the current long-lived recovery
		// cadence. Explicit RetryBase values retain the historical exponential
		// behavior unless RetryInterval is also supplied.
		o.RetryInterval = 30 * time.Second
	}
	if o.RetryMax <= 0 || o.RetryMax > 2*time.Minute {
		o.RetryMax = 2 * time.Minute
	}
	if o.RetryBase > o.RetryMax && o.RetryInterval <= 0 {
		o.RetryBase = o.RetryMax
	}
	if o.RetryInterval < 0 {
		o.RetryInterval = 0
	}
	if o.MaxRetries <= 0 || o.MaxRetries > executors.MaxUpstreamAttemptLimit-1 {
		o.MaxRetries = executors.DefaultUpstreamAttemptLimit
	}
	if o.NightMaxRetries <= 0 || o.NightMaxRetries > executors.MaxUpstreamAttemptLimit-1 {
		o.NightMaxRetries = 600
	}
	if o.NightStartHour < 0 || o.NightStartHour > 23 {
		o.NightStartHour = 20
	}
	if o.NightLocation == nil {
		o.NightLocation = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	if o.KeepaliveInterval <= 0 {
		o.KeepaliveInterval = 15 * time.Second
	}
	return o
}

func (o SurvivalOptions) retriesFor(now time.Time) int {
	if o.NightMaxRetries <= 0 {
		return o.MaxRetries
	}
	local := now.In(o.NightLocation)
	if local.Hour() >= o.NightStartHour {
		return o.NightMaxRetries
	}
	return o.MaxRetries
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
	// History is the bounded, content-free decision history retained for the
	// lifetime of this request. It complements FinalAttempt: the latter is only
	// the most recent pass, while History prevents a refresh from reusing an
	// exhausted node/model combination.
	History errorsx.DecisionHistory
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
	// PrefixCache (FR-12 L2 wiring, design resume-blocked-long-stream-recovery
	// §3.3 point 1): overrides the process-wide CommittedPrefixCache the
	// gates observe committed semantic bytes into. nil (production wiring —
	// the field is not set by survival_wiring) falls back to the package
	// singleton; tests inject a bounded instance.
	PrefixCache *CommittedPrefixCache

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
	// RetryNotice reports one discarded recoverable attempt. It must use a
	// transport-only frame and never include upstream bodies or credentials.
	// Notice failures are observable but do not change the retry decision.
	RetryNotice func(ctx context.Context, attempt int, decision TaskDecision, wait time.Duration) error
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
	if err := ctx.Err(); err != nil {
		return err
	}
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

func (c *SurvivalCoordinator) recoveryWait(opts SurvivalOptions, decision TaskDecision, backoff time.Duration) time.Duration {
	// An explicit upstream recovery time is authoritative. It is constrained by
	// the request deadline in Run rather than silently replaced with RetryMax.
	if decision.NextRetryAfter > 0 {
		return decision.NextRetryAfter
	}
	if opts.RetryInterval > 0 {
		// RetryInterval is an operator-selected cadence, not a backoff seed. Keep
		// it exact so a configured 30-second policy remains 30 seconds.
		return opts.RetryInterval
	}
	return applyBackoffJitter(backoff, c.JitterRand)
}

func (c *SurvivalCoordinator) notifyRetry(ctx context.Context, attempt int, decision TaskDecision, wait time.Duration) {
	if c.RetryNotice == nil {
		return
	}
	if err := c.RetryNotice(ctx, attempt, decision, wait); err != nil {
		slog.Warn("survival retry notice failed", "attempt", attempt, "action", decision.Action.String(), "reason", decision.Reason, "error", err)
	}
}

func survivalCancellationDecision(err error) TaskDecision {
	if errors.Is(err, context.DeadlineExceeded) {
		return TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
	}
	return TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
}

// Run drives the recovery loop until the task reaches a terminal state,
// the deadline expires or ctx is cancelled. sw is the shared serialized
// writer over the real connection; the caller owns its construction.
func (c *SurvivalCoordinator) Run(ctx context.Context, sw *SerializedStreamWriter, params *executors.ExecParams) SurvivalResult {
	if params == nil || c.Exec == nil {
		return SurvivalResult{Decision: TaskDecision{Action: TaskActionFailClosed, Reason: "survival_unavailable"}}
	}
	if err := ctx.Err(); err != nil {
		return SurvivalResult{Decision: survivalCancellationDecision(err)}
	}

	opts := c.Options.withDefaults()
	startedAt := c.now()
	maxRetries := opts.retriesFor(startedAt)
	// MaxRetries is retries after the initial attempt, while the shared budget
	// counts actual upstream calls. Allocate room for both when coordinator owns
	// the budget; a caller-provided budget remains the stricter authority.
	if params.UpstreamAttempts == nil {
		params.UpstreamAttempts = executors.NewUpstreamAttemptBudget(maxRetries + 1)
	}
	deadline := startedAt.Add(opts.Deadline)
	backoff := opts.RetryBase

	// FR-12 L1 revocable window: hold the first HoldbackMaxChunks semantic
	// frames (or HoldbackWindow after the first frame, whichever first) in the
	// attempt-local buffer WITHOUT committing to the client. While the window is
	// open, an upstream interruption discards the buffer and fails over
	// transparently (client sees zero partial output) instead of escalating to
	// resume_blocked/committed_output. Unstable models (glm-5.2, minimax-m3)
	// drop the connection a few chunks in far more often than they fail after a
	// full response, so a real window converts most of those into invisible
	// retries. 2026-09-01 P0 fix: use model-specific holdback tuning to provide
	// extended windows for unstable models while keeping stable models at the
	// default 5s/20 chunks. Tunable via LLM_GATEWAY_RECOVERY_HOLDBACK_* env.
	hbWindow, hbChunks := RecoveryHoldbackForModel(params.Model)

	// FR-12 L2 committed-prefix aligned continuation (design
	// resume-blocked-long-stream-recovery §3.3 points 1/2): INERT unless
	// LLM_GATEWAY_RECOVERY_L2_ENABLED is set — with the switch closed (the
	// default) no gate observes, the cache is never consulted and the loop
	// below is byte-identical to the pre-L2 build. When enabled every gate
	// folds its wire-proven semantic frames into the committed-prefix cache
	// (request-scoped entry), and a committed_output resume_blocked verdict
	// first asks the recovery ladder for an aligned continuation replay
	// before falling to the error envelope. The entry is dropped when Run
	// returns (succeed / error / envelope — every Run exit is request
	// terminal).
	l2 := recoveryL2RuntimeFromEnv()
	if l2.enabled && c.PrefixCache != nil {
		l2.cache = c.PrefixCache
	}
	if l2.enabled {
		defer l2.cache.Remove(params.RequestID)
	}
	// l2Replay arms the NEXT attempt as an L2 aligned replay; l2RecoveryNo /
	// l2LastScoreBP feed the ladder's budget and miss-escalation semantics;
	// l2Used records that a replay's suffix actually reached the client.
	var l2Replay *ReplayAlignmentOptions
	var l2RecoveryNo int
	var l2LastScoreBP uint16
	var l2Used bool

	res := SurvivalResult{}
	// 2026-08-31 (P2-6 audit-data-closure): allocate res.History with zero
	// length and a fresh backing array so the append in
	// appendSurvivalHistory never aliases storage carried over from a prior
	// Run call. The earlier nil-only assignment was insufficient: append()
	// would silently reuse the capacity of any slice header the caller had
	// retained from a previous Run, causing new entries from Run B to
	// appear in the A.History slice the caller still held. Using make with
	// a small initial cap guarantees a fresh allocation and bounds the
	// first few appends before any growth.
	res.History.PriorAttempts = make([]errorsx.PriorAttempt, 0, 8)
	res.History.TriedNodes = nil
	res.History.TriedModels = nil
	res.History.Terminal = false
	res.History.LastSeq = 0
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
		if err := ctx.Err(); err != nil {
			res.Decision = survivalCancellationDecision(err)
			return res
		}
		if !c.now().Before(deadline) {
			res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
			c.renderTerminal(res.Decision, nil, false)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			return res
		}
		// 2026-09-01: attempt-start line. Without it the log only shows the
		// OUTCOME of each pass, so a request that ends in resume_blocked gives
		// no way to tell whether the L1 holdback window was even active (the
		// glm-5.2 / minimax-m3 failure class hinges on exactly that) nor how
		// much of the interactive deadline remained.
		log.Info("survival_attempt_start",
			"request_id", params.RequestID,
			"attempt", res.Attempts+1,
			"holdback_window_ms", hbWindow.Milliseconds(),
			"holdback_max_chunks", hbChunks,
			"l2_aligned_replay", l2Replay != nil,
			"deadline_remaining_sec", int(deadline.Sub(c.now()).Seconds()),
			"backoff_ms", backoff.Milliseconds(),
		)
		// P1-2 fix (2026-08-28): Pass ctx to gate for checkpoint context propagation.
		gateOpts := GateOptions{Mode: GateModeBuffered, RequestID: params.RequestID, HoldbackWindow: hbWindow, HoldbackMaxChunks: hbChunks, BeforeSemanticCommit: func(ctx context.Context, state CommitState) error {
			if c.BeforeSemanticCommit == nil {
				return nil
			}
			return c.BeforeSemanticCommit(ctx, state)
		}}
		if l2.enabled {
			// L2 wiring point 1: fold every wire-proven semantic frame into
			// the committed-prefix cache (Observe is only armed while L2 is
			// enabled — the default-off hot path never touches the cache).
			gateOpts.PrefixObserve = l2.cache.Observe
		}
		l2ReplayThisAttempt := l2Replay != nil
		if l2ReplayThisAttempt {
			// L2 aligned replay (design §3.3 point 3): the aligner's
			// suppression window replaces the L1 holdback — frames stay
			// discardable until prefix alignment proves no duplication.
			gateOpts.HoldbackWindow = 0
			gateOpts.HoldbackMaxChunks = 0
			gateOpts.ReplayAlignment = l2Replay
			l2Replay = nil
		}
		gate := NewAttemptCommitGate(ctx, c.Protocol, sw, gateOpts)

		gw := NewGateWriterWithResponse(gate, params.W)
		attemptParams := *params
		attemptParams.W = gw
		res.FinalAttempt = ExecuteAttempt(ctx, c.Exec, gate, &attemptParams)
		res.Attempts++
		if err := ctx.Err(); err != nil {
			if gate.State() < CommitStateContent {
				_ = gate.Discard()
			}
			res.Decision = survivalCancellationDecision(err)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			return res
		}
		if !c.now().Before(deadline) {
			if gate.State() < CommitStateContent {
				_ = gate.Discard()
			}
			res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
			c.renderTerminal(res.Decision, gate, false)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			return res
		}
		recordSurvivalAttempt(res.FinalAttempt)
		res.Decision = AggregateTaskOutcomeWithHistory(res.FinalAttempt, res.History)
		appendSurvivalHistory(&res.History, res.FinalAttempt, res.Attempts, res.Decision)
		if l2ReplayThisAttempt {
			// L2 replay verdict (design §3.3 point 3): an alignment miss —
			// or a stream that ended before a verdict could form — voids the
			// replay attempt (nothing client-visible was emitted from it)
			// and degrades to the existing resume_blocked envelope path
			// below; L3/L4 stay at their current default-off state.
			decided, alignment := gate.AlignmentOutcome()
			l2LastScoreBP = alignment.ScoreBP
			if decided && alignment.Aligned {
				l2Used = true
			} else {
				res.Decision = TaskDecision{Action: TaskActionResumeBlocked, Reason: "l2_alignment_miss"}
			}
		}

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
		log.Info("survival_attempt_outcome", append(survivalRouteLogAttrs(params.RequestID, res.Attempts, res.Decision),
			"committed", res.FinalAttempt.CommitState >= CommitStateContent,
			"commit_state", res.FinalAttempt.CommitState.String(),
			"kinds", strings.Join(attemptKinds, ","),
			"candidate_count", len(res.FinalAttempt.CandidateOutcomes),
			"action", res.Decision.Action.String(),
			"reason", res.Decision.Reason,
			"provider_id", lastProviderID,
			"raw_model", lastRawModel,
		)...)

		switch res.Decision.Action {
		case TaskActionSucceed:
			// Flush unconditionally. With the FR-12 L1 holdback window enabled
			// the gate may still be uncommitted (first N chunks buffered) when
			// the stream ends; GateWriter.Finish → FinishAttempt releases those
			// held frames to the client. finishGateWriter's committed-guard
			// would wrongly swallow them and the client would see an empty
			// body. FinishAttempt refuses a discarded gate, so this stays safe.
			if err := gw.Finish(); err != nil {
				finishBufBytes, finishHoldbackHeld, finishGateState := gate.Snapshot()
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				// 2026-09-01: the finish error itself was previously dropped, so a
				// success-path attempt that died while releasing held frames was
				// indistinguishable from a genuine client disconnect.
				log.Warn("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
					"finish_error", err.Error(),
					"buffer_bytes", finishBufBytes,
					"holdback_held", finishHoldbackHeld,
					"gate_state", finishGateState.String(),
					"provider_id", lastProviderID,
					"raw_model", lastRawModel,
				)
				return res
			}
			if err := gate.Commit(); err != nil {
				commitBufBytes, commitHoldbackHeld, commitGateState := gate.Snapshot()
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "client_disconnected"}
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				// 2026-09-01: this branch returned entirely silently, so a failed
				// final commit left no trace at all in the logs.
				log.Error("survival_task_ended",
					"attempt", res.Attempts,
					"action", res.Decision.Action.String(),
					"reason", res.Decision.Reason,
					"committed", res.FinalAttempt.CommitState >= CommitStateContent,
					"succeed", false,
					"commit_error", err.Error(),
					"buffer_bytes", commitBufBytes,
					"holdback_held", commitHoldbackHeld,
					"gate_state", commitGateState.String(),
					"provider_id", lastProviderID,
					"raw_model", lastRawModel,
				)
				return res
			}
			res.Succeed = true
			if l2Used {
				// R12.9: the request completed through at least one L2
				// aligned-continuation replay whose suffix reached the client.
				RecordStreamRecoverySuccess(RecoveryModeAligned)
			}

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
				c.renderTerminal(res.Decision, gate, false)
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
			if res.Attempts-1 >= maxRetries {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "retry_limit_exceeded"}
				c.renderTerminal(res.Decision, gate, false)
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
				c.renderTerminal(res.Decision, gate, false)
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
			log.Info("survival_attempt_discarded", append(survivalRouteLogAttrs(params.RequestID, res.Attempts, res.Decision),
				"buffer_bytes", bufferBytes,
				"holdback_held", holdbackHeld,
				"state", gateState,
				"provider_id", lastProviderID,
				"raw_model", lastRawModel,
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
			)...)
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
			if !c.now().Before(deadline) {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
				c.renderTerminal(res.Decision, gate, false)
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
			wait := c.recoveryWait(opts, res.Decision, backoff)
			remaining := deadline.Sub(c.now())
			if wait > remaining {
				wait = remaining
			}
			if wait <= 0 {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
				c.renderTerminal(res.Decision, gate, false)
				recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				return res
			}
			if recoveryStart.IsZero() {
				recoveryStart = c.now()
			}
			waitState := survivalStateRetryNow
			if res.Decision.Action == TaskActionWaitRecovery {
				waitState = survivalStateWaiting
			}
			c.notifyRetry(ctx, res.Attempts, res.Decision, wait)
			if c.Reschedule != nil {
				if err := c.Reschedule(ctx, c.now().Add(wait), res.Decision.Reason); err != nil {
					res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "durable_reschedule_failed"}
					c.renderTerminal(res.Decision, gate, false)
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
				res.Decision = survivalCancellationDecision(err)
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
			if err := ctx.Err(); err != nil {
				res.Decision = survivalCancellationDecision(err)
				recordSurvivalTransition(waitState, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				return res
			}
			if !c.now().Before(deadline) {
				res.Decision = TaskDecision{Action: TaskActionFailClosed, Reason: "deadline_exceeded"}
				c.renderTerminal(res.Decision, gate, false)
				recordSurvivalTransition(waitState, survivalTerminalToState(res.Decision), res.Decision.Reason)
				recordSurvivalRequestTerminal(c.Protocol, res.Decision)
				return res
			}
			if res.Decision.Action == TaskActionWaitRecovery && res.FinalAttempt != nil {
				observeSurvivalWait(res.FinalAttempt.LastKind(), wait)
			}
			recordSurvivalTransition(waitState, survivalStateRunning, "retry")
			if c.Refresh != nil {
				c.Refresh(ctx)
			}
			if opts.RetryInterval <= 0 {
				backoff *= 2
				if backoff > opts.RetryMax {
					backoff = opts.RetryMax
				}
			}
			log.Debug("survival_attempt_recovery_resumed",
				"attempt", res.Attempts,
				"wait_ms", wait.Milliseconds(),
				"action", res.Decision.Action.String(),
				"reason", res.Decision.Reason,
			)
			continue

		default: // ResumeBlocked / FailTerminal / FailClosed
			// L2 wiring point 2 (design §3.3): a committed_output
			// resume_blocked verdict first asks the FR-12 recovery ladder
			// (NextRecoveryActionCtx — emits survival_recovery_action with the
			// request correlation attrs) for an aligned continuation; only
			// when the ladder cannot offer one does the existing envelope
			// path below run. Inert while L2 is disabled (the default):
			// resume_blocked keeps its exact pre-L2 behavior. A replay that
			// already missed (reason l2_alignment_miss) never re-asks.
			if l2.enabled && res.Decision.Action == TaskActionResumeBlocked && res.Decision.Reason != "l2_alignment_miss" {
				if plan := c.l2AlignedReplayPlan(ctx, log, params, res, l2, l2RecoveryNo, l2LastScoreBP, deadline, maxRetries); plan != nil {
					// Keep the interrupted attempt's trailing partial bytes
					// on the wire (and observed) exactly as the envelope path
					// below would, then arm the aligned replay. The next loop
					// iteration re-checks ctx/deadline before executing it.
					_ = finishGateWriter(gw, gate)
					l2Replay = plan
					l2RecoveryNo++
					continue
				}
			}
			_ = finishGateWriter(gw, gate)
			// An L2 replay that missed leaves the LAST gate uncommitted (the
			// replay was voided), but the client already saw the interrupted
			// attempt's bytes — the terminal must render as committed so the
			// stream ends well-formed instead of hanging.
			c.renderTerminal(res.Decision, gate, res.Decision.Reason == "l2_alignment_miss")
			recordSurvivalTransition(survivalStateRunning, survivalTerminalToState(res.Decision), res.Decision.Reason)
			recordSurvivalResumeSafetyBlocked(res.Decision, res.FinalAttempt)
			recordSurvivalRequestTerminal(c.Protocol, res.Decision)
			// 2026-08-19: structured log alongside the SSE frame the client
			// receives. This is the single line operators should grep for
			// the "gateway_survival_resume_blocked / gateway request
			// survival ended: committed_output" failure class — it carries
			// the same fields the SSE envelope does plus the
			// request-correlation context the envelope cannot.
			log.Warn("survival_resume_blocked", append(survivalRouteLogAttrs(params.RequestID, res.Attempts, res.Decision),
				"action", res.Decision.Action.String(),
				// An L2 miss voids the replay gate, but the interrupted
				// attempt's bytes are client-visible — keep the flag true to
				// what the client actually saw.
				"committed", res.FinalAttempt.CommitState >= CommitStateContent || res.Decision.Reason == "l2_alignment_miss",
				"commit_state", res.FinalAttempt.CommitState.String(),
				"kinds", strings.Join(attemptKinds, ","),
				"provider_id", lastProviderID,
				"raw_model", lastRawModel,
			)...)
			return res
		}
	}
}

// l2AlignedReplayPlan consults the FR-12 ladder (NextRecoveryActionCtx — the
// ctx-aware variant, so the survival_recovery_action log line carries the
// request correlation attrs) for a committed_output resume_blocked verdict
// (design resume-blocked-long-stream-recovery §3.3 point 2). It returns a
// ReplayAlignmentOptions when an aligned-continuation replay may run NOW;
// nil keeps the existing resume_blocked envelope path (no committed-prefix
// entry, prefix beyond the replay buffer bound, upstream-attempt/retry
// budget or deadline exhausted, or the ladder's verdict is not aligned
// continuation — e.g. recovery budget exhausted, which with the default
// config already degrades to the error envelope).
func (c *SurvivalCoordinator) l2AlignedReplayPlan(
	ctx context.Context,
	log *streamLogger,
	params *executors.ExecParams,
	res SurvivalResult,
	l2 recoveryL2Runtime,
	recoveryNo int,
	lastScoreBP uint16,
	deadline time.Time,
	maxRetries int,
) *ReplayAlignmentOptions {
	if res.FinalAttempt == nil || res.FinalAttempt.FinalError == nil {
		return nil
	}
	if params.UpstreamAttempts != nil && params.UpstreamAttempts.Exhausted() {
		return nil
	}
	if res.Attempts-1 >= maxRetries {
		return nil
	}
	if !c.now().Before(deadline) {
		return nil
	}
	prefix, ok := l2.cache.Snapshot(params.RequestID)
	if !ok || prefix.TotalBytes <= 0 || prefix.TotalBytes > maxL2ReplayCommittedBytes {
		return nil
	}
	// The coordinator's own retry loop already consumed the same-node resend
	// budget, so report L0 exhausted to the ladder; CommittedChunks > 0 skips
	// the L1 branch; RecoveryMode/AlignmentScoreBP carry the previous replay's
	// verdict so a re-ask after an aligned replay that failed mid-suffix
	// keeps the ladder's miss-escalation semantics intact.
	state := &StreamRecoveryState{
		RecoveryNo:       uint16(recoveryNo),
		SameNodeRetries:  uint8(max(1, l2.cfg.SameNodeStreamRetries)),
		CommittedChunks:  1,
		CommittedBytes:   uint32(prefix.TotalBytes),
		AlignmentScoreBP: lastScoreBP,
	}
	if recoveryNo > 0 {
		state.RecoveryMode = RecoveryModeAligned
	}
	act := NextRecoveryActionCtx(ctx, state, l2.cfg, res.FinalAttempt.FinalError)
	if act.Kind != RecoveryActionAlignedContinuation {
		return nil
	}
	log.Info("survival_l2_replay_armed",
		"request_id", params.RequestID,
		"attempt", res.Attempts,
		"recovery_no", recoveryNo,
		"committed_bytes", prefix.TotalBytes,
	)
	return &ReplayAlignmentOptions{
		Committed: prefix,
		Aligner:   NewPrefixAligner(l2.cfg.AlignmentThresholdBP),
	}
}

// survivalRouteLogAttrs provides stable, content-free dimensions shared by every
// survival outcome/discard/resume-blocked event. attempt_id is deterministic for
// a request and coordinator pass, while the route fields remain queryable even
// when the router did not return a candidate.
func survivalRouteLogAttrs(requestID string, attempt int, decision TaskDecision) []any {
	attemptID := fmt.Sprintf("%s/%d", requestID, attempt)
	return []any{
		"request_id", requestID,
		"attempt_id", attemptID,
		"attempt", attempt,
		"route_override", "none",
		"fallback_reason", decision.Reason,
		"effective_route_status", "normal",
		"recovery_action", decision.Action.String(),
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

// renderTerminal hands the final protocol rendering to the Terminal seam.
// clientCommitted ORs in an out-of-band committed signal: an L2 replay miss
// voids the last gate (uncommitted by design) even though an earlier attempt
// of the same request already put bytes on the wire.
func (c *SurvivalCoordinator) renderTerminal(decision TaskDecision, gate *AttemptCommitGate, clientCommitted bool) {
	if c.Terminal == nil {
		return
	}
	committed := clientCommitted || (gate != nil && gate.Committed())
	c.Terminal(decision, committed)
}
