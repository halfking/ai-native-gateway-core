package streaming

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// attempt_outcome.go — SR-W1 (doc 18 §5.1 ExecuteAttempt)
//
// The AttemptResult contract is the structured outcome one bounded attempt
// returns to the survival coordinator. It deliberately does NOT extend the
// per-bridge StreamOutcome: bridges report transport-level stream facts,
// while AttemptResult carries the task-level facts the coordinator needs —
// every candidate outcome, the attempt's commit state, and whether a safe
// transparent retry is still possible.

// ResponseType records what kind of response the attempt produced.
type ResponseType int

const (
	// ResponseTypeStream is a streaming (SSE) response.
	ResponseTypeStream ResponseType = iota
	// ResponseTypeNonStream is a buffered non-streaming response.
	ResponseTypeNonStream
)

// String implements fmt.Stringer.
func (r ResponseType) String() string {
	if r == ResponseTypeNonStream {
		return "non_stream"
	}
	return "stream"
}

// CandidateOutcome describes what happened with one candidate inside an
// attempt (one attempt may rotate through several credentials).
type CandidateOutcome struct {
	CandidateID  string
	CredentialID string
	// ProviderID is the upstream provider of the candidate (0 when the
	// outcome was synthesized, e.g. no candidate was ever planned); the
	// survival metrics use it as the provider label (SR-13).
	ProviderID int
	// Kind is the errorsx.ErrorKind of the failure; empty when the
	// candidate succeeded (Success=true on the AttemptResult).
	Kind       errorsx.ErrorKind
	Err        error
	RetryAfter time.Duration
}

// AttemptResult is the structured result of one bounded attempt.
type AttemptResult struct {
	Success           bool
	CandidateOutcomes []CandidateOutcome
	// ExecResult is the raw executor result on success (nil on failure);
	// the coordinator hands it back so the handler's shared post-loop
	// processing (telemetry, traces, session bookkeeping) runs unchanged.
	ExecResult *executors.ExecuteResult
	// CommitState mirrors the AttemptCommitGate state at the end of the
	// attempt: none/metadata ⇒ buffer discardable, content+ ⇒ client
	// already saw semantic output.
	CommitState  CommitState
	ResponseType ResponseType
	FinalError   error
	// SafeRetry is the coordinator-facing verdict: true when a transparent
	// retry cannot duplicate client-visible output.
	SafeRetry  bool
	RetryAfter time.Duration
}

// LastKind returns the error kind of the last candidate outcome, or "" when
// the attempt succeeded. Convenience for logging; task decisions must use
// AggregateTaskOutcome, never LastKind alone (doc 18 §5.1).
func (r *AttemptResult) LastKind() errorsx.ErrorKind {
	if len(r.CandidateOutcomes) == 0 {
		return ""
	}
	return r.CandidateOutcomes[len(r.CandidateOutcomes)-1].Kind
}

// TaskAction is what the coordinator should do with the task after an
// attempt.
type TaskAction int

const (
	// TaskActionSucceed: the attempt completed; commit the task result.
	TaskActionSucceed TaskAction = iota
	// TaskActionRetryNow: connection-internal recovery — refresh candidates
	// and retry immediately.
	TaskActionRetryNow
	// TaskActionWaitRecovery: keep the request alive but wait for the
	// recovery window (quota reset, provider back, capacity).
	TaskActionWaitRecovery
	// TaskActionResumeBlocked: output already committed; transparent retry
	// would duplicate client-visible content (doc 18 §10.3).
	TaskActionResumeBlocked
	// TaskActionFailTerminal: permanent failure — render the protocol error
	// and end the task.
	TaskActionFailTerminal
	// TaskActionFailClosed: unmapped/unknown condition — end the task
	// safely instead of guessing.
	TaskActionFailClosed
)

// String implements fmt.Stringer for logs, metrics labels and transitions.
func (a TaskAction) String() string {
	switch a {
	case TaskActionSucceed:
		return "succeed"
	case TaskActionRetryNow:
		return "retry_now"
	case TaskActionWaitRecovery:
		return "wait_recovery"
	case TaskActionResumeBlocked:
		return "resume_blocked"
	case TaskActionFailTerminal:
		return "fail_terminal"
	case TaskActionFailClosed:
		return "fail_closed"
	default:
		return "unknown"
	}
}

// TaskDecision is the aggregated verdict for a task after one attempt.
type TaskDecision struct {
	Action TaskAction
	// Reason is a stable, low-cardinality machine reason (kind name or
	// "unmapped_kind").
	Reason string
	// NextRetryAfter is the suggested backoff for RetryNow/WaitRecovery.
	NextRetryAfter time.Duration
}

// taskActionForKind is retained as the compatibility projection for callers
// that only need the legacy task-level action. The authoritative single-kind
// interpretation now lives in errorsx.DecideNextAction; AggregateTaskOutcome
// uses centralActionForTask below so request/route context and commit state are
// evaluated consistently before this projection is applied.
var taskActionForKind = map[errorsx.ErrorKind]TaskAction{
	// Connection-internal immediate recovery.
	errorsx.KindTransient:          TaskActionRetryNow,
	errorsx.KindTimeout:            TaskActionRetryNow,
	errorsx.KindNetwork:            TaskActionRetryNow,
	errorsx.KindConcurrent:         TaskActionRetryNow,
	errorsx.KindUpstreamOverloaded: TaskActionRetryNow,
	errorsx.KindEmptyResponse:      TaskActionRetryNow,
	// Credential-level failure: rotating to a sibling candidate is the
	// normal recovery.
	errorsx.KindAuth: TaskActionRetryNow,
	// Pre-commit stream timeouts are transparently retryable; the commit
	// state check below upgrades committed cases to ResumeBlocked.
	errorsx.KindStreamTimeout: TaskActionRetryNow,

	// Wait-for-recovery windows.
	errorsx.KindRateLimit:          TaskActionWaitRecovery,
	errorsx.KindQuota:              TaskActionWaitRecovery,
	errorsx.KindQuotaPeriodic:      TaskActionWaitRecovery,
	errorsx.KindQuotaBalance:       TaskActionWaitRecovery,
	errorsx.KindUpstreamDown:       TaskActionWaitRecovery,
	errorsx.KindNoAvailableChannel: TaskActionWaitRecovery,

	// Terminal for the task (upstream- or request-determined).
	errorsx.KindAuthRevoked:         TaskActionFailTerminal,
	errorsx.KindQuotaPermanent:      TaskActionFailTerminal,
	errorsx.KindModelNotFound:       TaskActionFailTerminal,
	errorsx.KindModelDeprecated:     TaskActionFailTerminal,
	errorsx.KindContextLength:       TaskActionFailTerminal,
	errorsx.KindUnsupportedFeature:  TaskActionFailTerminal,
	errorsx.KindContentFilter:       TaskActionFailTerminal,
	errorsx.KindConversion:          TaskActionFailTerminal,
	errorsx.KindUpstreamContextLoss: TaskActionFailTerminal,
	errorsx.KindToolCallIdMismatch:  TaskActionFailTerminal,
	// Client-determined terminal.
	errorsx.KindCanceled:  TaskActionFailTerminal,
	errorsx.KindClientBug: TaskActionFailTerminal,
}

// AggregateTaskOutcome folds one AttemptResult into a TaskDecision.
//
// Ordering: success > unmapped (fail closed) > terminal > committed-output
// block > retry-now > wait-recovery. Mixed candidates prefer the strongest
// recovery still available; a single terminal candidate fails the task.
func centralActionForTask(kind errorsx.ErrorKind, committed bool, retryAfter time.Duration) TaskDecision {
	commit := errorsx.ActionCommitNone
	if committed {
		commit = errorsx.ActionCommitContent
	}
	action := errorsx.DecideNextAction(errorsx.DecisionContext{
		Kind:        kind,
		CommitState: commit,
		RetryAfter:  retryAfter,
		// Candidate availability and attempt budgets are aggregated by this
		// layer; the central policy supplies only the kind-level action.
		HasAlternateNode:   true,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 0,
	})
	switch action.Action {
	case errorsx.ActionRetrySameNode, errorsx.ActionSwitchNode:
		return TaskDecision{Action: TaskActionRetryNow, Reason: action.ReasonCode, NextRetryAfter: action.RetryAfter}
	case errorsx.ActionWaitRecovery:
		return TaskDecision{Action: TaskActionWaitRecovery, Reason: action.ReasonCode, NextRetryAfter: action.RetryAfter}
	case errorsx.ActionResumeBlocked:
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: action.ReasonCode}
	case errorsx.ActionFailTerminal:
		return TaskDecision{Action: TaskActionFailTerminal, Reason: action.ReasonCode}
	case errorsx.ActionClientCanceled:
		return TaskDecision{Action: TaskActionFailTerminal, Reason: action.ReasonCode}
	default:
		return TaskDecision{Action: TaskActionFailClosed, Reason: action.ReasonCode}
	}
}

func appendSurvivalHistory(history *errorsx.DecisionHistory, result *AttemptResult, attemptNo int, decision TaskDecision) {
	if history == nil || result == nil {
		return
	}
	for _, candidate := range result.CandidateOutcomes {
		if candidate.Kind == "" {
			continue
		}
		history.LastSeq++
		history.PriorAttempts = append(history.PriorAttempts, errorsx.PriorAttempt{
			Seq: history.LastSeq, AttemptNo: attemptNo, Model: candidateModel(candidate.CandidateID),
			ProviderID: candidate.ProviderID, CredentialID: candidateCredential(candidate.CredentialID), Kind: candidate.Kind,
			Action: taskActionToCentral(decision.Action), RetryAfter: candidate.RetryAfter, Committed: result.CommitState >= CommitStateContent,
		})
	}
	if len(history.PriorAttempts) > 128 {
		history.PriorAttempts = history.PriorAttempts[len(history.PriorAttempts)-128:]
	}
}

func candidateModel(id string) string {
	if i := strings.Index(id, "/model:"); i >= 0 {
		return id[i+len("/model:"):]
	}
	return id
}

func candidateCredential(id string) int {
	n, _ := strconv.Atoi(id)
	return n
}

func taskActionToCentral(action TaskAction) errorsx.NextAction {
	switch action {
	case TaskActionRetryNow:
		return errorsx.ActionRetrySameNode
	case TaskActionWaitRecovery:
		return errorsx.ActionWaitRecovery
	case TaskActionResumeBlocked:
		return errorsx.ActionResumeBlocked
	case TaskActionFailTerminal:
		return errorsx.ActionFailTerminal
	default:
		return ""
	}
}

func AggregateTaskOutcomeWithHistory(r *AttemptResult, history errorsx.DecisionHistory) TaskDecision {
	if r == nil {
		return TaskDecision{Action: TaskActionFailClosed, Reason: "nil_attempt_result"}
	}
	if r.Success {
		return TaskDecision{Action: TaskActionSucceed, Reason: "success"}
	}
	committed := r.CommitState >= CommitStateContent
	var hasRetry, hasWait, hasTerminal, hasUnknown, hasBlocked bool
	var maxRetryAfter time.Duration
	var unknownKind errorsx.ErrorKind
	for _, co := range r.CandidateOutcomes {
		central := centralActionForTaskWithHistory(co.Kind, committed, co.RetryAfter, history)
		switch central.Action {
		case TaskActionResumeBlocked:
			hasBlocked = true
		case TaskActionRetryNow:
			hasRetry = true
		case TaskActionWaitRecovery:
			hasWait = true
		case TaskActionFailTerminal:
			hasTerminal = true
		case TaskActionFailClosed:
			hasUnknown = true
			if unknownKind == "" {
				unknownKind = co.Kind
			}
		}
		// An explicit upstream Retry-After is authoritative. When it is absent,
		// the coordinator's configured RetryBase remains the pacing owner; do not
		// replace that request-level setting with a policy default here.
		if co.RetryAfter > maxRetryAfter {
			maxRetryAfter = co.RetryAfter
		}
	}
	if hasUnknown {
		return TaskDecision{Action: TaskActionFailClosed, Reason: fmt.Sprintf("unmapped_kind:%s", unknownKind)}
	}
	if hasTerminal {
		return TaskDecision{Action: TaskActionFailTerminal, Reason: "terminal_candidate"}
	}
	if hasBlocked {
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
	}
	if committed && (hasRetry || hasWait) {
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
	}
	if hasRetry {
		return TaskDecision{Action: TaskActionRetryNow, Reason: "recoverable_candidate", NextRetryAfter: maxRetryAfter}
	}
	if hasWait {
		return TaskDecision{Action: TaskActionWaitRecovery, Reason: "wait_recovery_window", NextRetryAfter: maxRetryAfter}
	}
	return TaskDecision{Action: TaskActionFailClosed, Reason: "no_candidate_outcomes"}
}

func centralActionForTaskWithHistory(kind errorsx.ErrorKind, committed bool, retryAfter time.Duration, history errorsx.DecisionHistory) TaskDecision {
	commit := errorsx.ActionCommitNone
	if committed {
		commit = errorsx.ActionCommitContent
	}
	central := errorsx.DecideNextAction(errorsx.DecisionContext{
		Kind: kind, CommitState: commit, RetryAfter: retryAfter,
		HasAlternateNode: true, RemainingAttempts: 1, MaxSameNodeRetries: 1,
		History: history,
	})
	if committed && central.Action == errorsx.ActionFailClosed {
		// The policy layer cannot infer a route identity for synthetic survival
		// candidate records. Preserve the authoritative commit safety rule here:
		// committed recoverable output is never transparently retried.
		central.Action = errorsx.ActionResumeBlocked
		central.ReasonCode = "committed_output"
	}
	switch central.Action {
	case errorsx.ActionRetrySameNode, errorsx.ActionSwitchNode:
		return TaskDecision{Action: TaskActionRetryNow, Reason: central.ReasonCode, NextRetryAfter: central.RetryAfter}
	case errorsx.ActionWaitRecovery:
		return TaskDecision{Action: TaskActionWaitRecovery, Reason: central.ReasonCode, NextRetryAfter: central.RetryAfter}
	case errorsx.ActionResumeBlocked:
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: central.ReasonCode}
	case errorsx.ActionFailTerminal:
		return TaskDecision{Action: TaskActionFailTerminal, Reason: central.ReasonCode}
	default:
		return TaskDecision{Action: TaskActionFailClosed, Reason: central.ReasonCode}
	}
}

func AggregateTaskOutcome(r *AttemptResult) TaskDecision {
	if r == nil {
		return TaskDecision{Action: TaskActionFailClosed, Reason: "nil_attempt_result"}
	}
	if r.Success {
		return TaskDecision{Action: TaskActionSucceed, Reason: "success"}
	}

	committed := r.CommitState >= CommitStateContent

	var (
		hasRetry, hasWait, hasTerminal, hasUnknown bool
		maxRetryAfter                              time.Duration
		unknownKind                                errorsx.ErrorKind
	)
	for _, co := range r.CandidateOutcomes {
		central := centralActionForTask(co.Kind, false, co.RetryAfter)
		switch central.Action {
		case TaskActionRetryNow:
			hasRetry = true
		case TaskActionWaitRecovery:
			hasWait = true
		case TaskActionFailTerminal:
			hasTerminal = true
		case TaskActionFailClosed:
			hasUnknown = true
			if unknownKind == "" {
				unknownKind = co.Kind
			}
		}
		if co.RetryAfter > maxRetryAfter {
			maxRetryAfter = co.RetryAfter
		}
	}

	switch {
	case hasUnknown:
		// Fail closed: never guess the recoverability of an unmapped kind.
		return TaskDecision{
			Action: TaskActionFailClosed,
			Reason: fmt.Sprintf("unmapped_kind:%s", unknownKind),
		}
	case hasTerminal:
		return TaskDecision{
			Action: TaskActionFailTerminal,
			Reason: "terminal_candidate",
		}
	case committed && (hasRetry || hasWait):
		// Recoverable failure, but client-visible output was already
		// committed — a transparent restart would duplicate it.
		return TaskDecision{
			Action: TaskActionResumeBlocked,
			Reason: "committed_output",
		}
	case hasRetry:
		return TaskDecision{
			Action:         TaskActionRetryNow,
			Reason:         "recoverable_candidate",
			NextRetryAfter: maxRetryAfter,
		}
	case hasWait:
		return TaskDecision{
			Action:         TaskActionWaitRecovery,
			Reason:         "wait_recovery_window",
			NextRetryAfter: maxRetryAfter,
		}
	default:
		// No outcomes at all and not successful — treat as fail closed.
		return TaskDecision{
			Action: TaskActionFailClosed,
			Reason: "no_candidate_outcomes",
		}
	}
}
