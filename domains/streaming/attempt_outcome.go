package streaming

import (
	"fmt"
	"log/slog"
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

	// —— 2026-09-05 审计闭环2：诊断维度补齐 ——
	// 以下字段从 fold 阶段的请求上下文与路由追踪器填充；合成路径
	// （durable runner / no-candidate）可以全部为零值。它们驱动
	// supplier_errors_hot 持久化投影与前端结构化展示。
	// RequestID 是本次请求 id（候选级冗余，方便日志与表行互查）。
	RequestID string
	// AttemptSeq 是该候选在本请求内的序号（1 起；按 fold 顺序）。
	AttemptSeq int
	// Supplier 是 provider catalog code（低基数；未知为空）。
	Supplier string
	// HTTPStatus 是上游 HTTP 状态（0 = 纯网络错误）。
	HTTPStatus int
	// Retryable 三态：nil=未分类。
	Retryable *bool
	// LatencyMs 是该候选的尝试耗时（0 = 未知）。
	LatencyMs int64
	// Stage 是失败阶段枚举（preflight/connect/upstream/stream；空=未知）。
	Stage string
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
// centralActionForTask folds the legacy per-kind task action table with the
// central policy's commit-aware override. The kind → action table remains
// authoritative for the streaming context (where retry-budget, history-loop
// protection, and credential-scope switches are not the right granularity);
// the central policy only contributes the commit-block and disconnect rules
// that the central spec hard-pins across all phases.
func centralActionForTask(kind errorsx.ErrorKind, committed bool, retryAfter time.Duration) TaskDecision {
	// Central policy owns: client disconnect, unmapped kinds, and the
	// "committed recoverable output is never transparently retried" rule.
	central := errorsx.DecideNextAction(errorsx.DecisionContext{
		Kind:               kind,
		CommitState:        commitStateCentral(committed),
		RetryAfter:         retryAfter,
		HasAlternateNode:   true,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 0,
	})
	if committed && central.Action == errorsx.ActionResumeBlocked {
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
	}
	if central.Action == errorsx.ActionClientCanceled {
		return TaskDecision{Action: TaskActionFailTerminal, Reason: central.ReasonCode}
	}
	if central.Action == errorsx.ActionFailClosed {
		return TaskDecision{Action: TaskActionFailClosed, Reason: central.ReasonCode}
	}
	action, mapped := taskActionForKind[kind]
	if !mapped {
		return TaskDecision{Action: TaskActionFailClosed, Reason: fmt.Sprintf("unmapped_kind:%s", kind)}
	}
	switch action {
	case TaskActionRetryNow:
		return TaskDecision{Action: TaskActionRetryNow, Reason: string(kind), NextRetryAfter: retryAfter}
	case TaskActionWaitRecovery:
		return TaskDecision{Action: TaskActionWaitRecovery, Reason: string(kind), NextRetryAfter: retryAfter}
	default:
		return TaskDecision{Action: action, Reason: string(kind)}
	}
}

func commitStateCentral(committed bool) errorsx.ActionCommitState {
	if committed {
		return errorsx.ActionCommitContent
	}
	return errorsx.ActionCommitNone
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
	n, err := strconv.Atoi(id)
	if err != nil {
		// Synthetic candidate IDs always come from execute_attempt.go's
		// "strconv.Itoa(ProviderID) + /model: + RawModel" composition, so a
		// parse failure here signals an upstream contract drift rather than
		// adversarial input. ActionNode treats CredentialID<=0 as invalid and
		// skips the entry — that is the safer side, but we still want to
		// surface the anomaly so operators can fix the producer.
		slog.Warn("survival: invalid candidate credential id",
			"candidate_id", id, "error", err.Error())
		return 0
	}
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
	
	// Enhanced logging for debugging retry decisions
	candidateSummary := make([]map[string]any, 0, len(r.CandidateOutcomes))
	for _, co := range r.CandidateOutcomes {
		central := centralActionForTaskWithHistory(co.Kind, committed, co.RetryAfter, history)
		
		// Log each candidate outcome for observability
		candidateSummary = append(candidateSummary, map[string]any{
			"provider_id":   co.ProviderID,
			"credential_id": co.CredentialID,
			"kind":          string(co.Kind),
			"action":        central.Action.String(),
			"retry_after":   co.RetryAfter.String(),
		})
		
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
	
	// Determine final decision
	var decision TaskDecision
	if hasUnknown {
		decision = TaskDecision{Action: TaskActionFailClosed, Reason: fmt.Sprintf("unmapped_kind:%s", unknownKind)}
	} else if hasTerminal {
		decision = TaskDecision{Action: TaskActionFailTerminal, Reason: "terminal_candidate"}
	} else if hasBlocked {
		decision = TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
	} else if committed && (hasRetry || hasWait) {
		decision = TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
	} else if hasRetry {
		decision = TaskDecision{Action: TaskActionRetryNow, Reason: "recoverable_candidate", NextRetryAfter: maxRetryAfter}
	} else if hasWait {
		decision = TaskDecision{Action: TaskActionWaitRecovery, Reason: "wait_recovery_window", NextRetryAfter: maxRetryAfter}
	} else {
		decision = TaskDecision{Action: TaskActionFailClosed, Reason: "no_candidate_outcomes"}
	}
	
	// Enhanced structured logging for observability
	slog.Info("survival_decision_aggregate",
		"commit_state", r.CommitState.String(),
		"committed", committed,
		"candidate_count", len(r.CandidateOutcomes),
		"candidates", candidateSummary,
		"decision_action", decision.Action.String(),
		"decision_reason", decision.Reason,
		"retry_after", decision.NextRetryAfter.String(),
		"has_retry", hasRetry,
		"has_wait", hasWait,
		"has_terminal", hasTerminal,
		"has_blocked", hasBlocked,
		"has_unknown", hasUnknown,
		"history_prior_attempts", len(history.PriorAttempts),
	)
	
	return decision
}

// centralActionForTaskWithHistory folds the legacy per-kind task action
// table with the central policy's commit-aware override and the bounded
// request history kept by SurvivalCoordinator. The history primarily
// prevents a refresh from cycling back into an exhausted route; the central
// policy still owns the commit-block and disconnect rules that the central
// spec hard-pins across all phases.
func centralActionForTaskWithHistory(kind errorsx.ErrorKind, committed bool, retryAfter time.Duration, history errorsx.DecisionHistory) TaskDecision {
	// Central policy owns: client disconnect, unmapped kinds, terminal kinds,
	// committed-output protection, and history-driven loop detection.
	central := errorsx.DecideNextAction(errorsx.DecisionContext{
		Kind:               kind,
		CommitState:        commitStateCentral(committed),
		RetryAfter:         retryAfter,
		HasAlternateNode:   true,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 0,
		History:            history,
	})
	if central.Action == errorsx.ActionResumeBlocked {
		return TaskDecision{Action: TaskActionResumeBlocked, Reason: central.ReasonCode}
	}
	if central.Action == errorsx.ActionClientCanceled {
		return TaskDecision{Action: TaskActionFailTerminal, Reason: central.ReasonCode}
	}
	if central.Action == errorsx.ActionFailClosed {
		// Synthetic candidate records carry no route identity, so the central
		// policy may legitimately return fail-closed on a recoverable kind. In
		// the streaming context a recoverable kind must still drive a refresh,
		// so fall back to the legacy task table for the kind-level action.
		if committed {
			return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
		}
	} else if central.Action == errorsx.ActionFailTerminal {
		return TaskDecision{Action: TaskActionFailTerminal, Reason: central.ReasonCode}
	}
	action, mapped := taskActionForKind[kind]
	if !mapped {
		return TaskDecision{Action: TaskActionFailClosed, Reason: fmt.Sprintf("unmapped_kind:%s", kind)}
	}
	switch action {
	case TaskActionRetryNow:
		return TaskDecision{Action: TaskActionRetryNow, Reason: string(kind), NextRetryAfter: retryAfter}
	case TaskActionWaitRecovery:
		return TaskDecision{Action: TaskActionWaitRecovery, Reason: string(kind), NextRetryAfter: retryAfter}
	default:
		return TaskDecision{Action: action, Reason: string(kind)}
	}
}

// AggregateTaskOutcome folds one AttemptResult into a TaskDecision WITHOUT
// history-based loop detection. This is the legacy entry point retained for
// durable_recovery_worker.go compatibility; new callers should use
// AggregateTaskOutcomeWithHistory for the history-aware policy.
//
// DEPRECATED: Callers should migrate to AggregateTaskOutcomeWithHistory to
// benefit from loop detection and consistent central policy application.
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
	
	// Log deprecation warning in debug builds
	slog.Debug("using_deprecated_aggregate_task_outcome",
		"note", "caller should migrate to AggregateTaskOutcomeWithHistory",
		"committed", committed,
		"candidate_count", len(r.CandidateOutcomes),
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

	var decision TaskDecision
	switch {
	case hasUnknown:
		// Fail closed: never guess the recoverability of an unmapped kind.
		decision = TaskDecision{
			Action: TaskActionFailClosed,
			Reason: fmt.Sprintf("unmapped_kind:%s", unknownKind),
		}
	case hasTerminal:
		decision = TaskDecision{
			Action: TaskActionFailTerminal,
			Reason: "terminal_candidate",
		}
	case committed && (hasRetry || hasWait):
		// Recoverable failure, but client-visible output was already
		// committed — a transparent restart would duplicate it.
		decision = TaskDecision{
			Action: TaskActionResumeBlocked,
			Reason: "committed_output",
		}
	case hasRetry:
		decision = TaskDecision{
			Action:         TaskActionRetryNow,
			Reason:         "recoverable_candidate",
			NextRetryAfter: maxRetryAfter,
		}
	case hasWait:
		decision = TaskDecision{
			Action:         TaskActionWaitRecovery,
			Reason:         "wait_recovery_window",
			NextRetryAfter: maxRetryAfter,
		}
	default:
		// No outcomes at all and not successful — treat as fail closed.
		decision = TaskDecision{
			Action: TaskActionFailClosed,
			Reason: "no_candidate_outcomes",
		}
	}
	
	// Log decision for consistency with WithHistory version
	slog.Debug("survival_decision_aggregate_nohistory",
		"committed", committed,
		"decision_action", decision.Action.String(),
		"decision_reason", decision.Reason,
		"has_retry", hasRetry,
		"has_wait", hasWait,
	)
	
	return decision
}
