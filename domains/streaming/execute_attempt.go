package streaming

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// execute_attempt.go — SR-05 (doc 18 §5.1, §17 Phase 0/1)
//
// ExecuteAttempt is the fixed-semantics entry for ONE bounded attempt: it
// runs the executor exactly once (SurvivalAttempt forces the executor's
// internal retry ladder — probe holds, sync retry, model fallback, async
// demotion — off), then folds whatever happened into the AttemptResult
// contract. The SurvivalCoordinator owns every retry decision; nothing
// below this line may recurse, spawn async goroutines or re-enter Execute.

// AttemptExecutor is the single-method executor surface ExecuteAttempt
// needs. Satisfied by *executors.Executor; narrow on purpose so tests (and
// later the coordinator) can substitute a bounded fake.
type AttemptExecutor interface {
	Execute(params *executors.ExecParams) (*executors.ExecuteResult, error)
}

// ExecuteAttempt runs one bounded attempt and returns its structured
// outcome. gate is the attempt's commit gate (may be nil only when request
// survival is disabled — then commit state reads as none and SafeRetry
// degrades to false so no caller can transparently retry untracked output).
// params is copied shallowly; the caller's slice is not mutated except for
// the SurvivalAttempt flag.
func ExecuteAttempt(ctx context.Context, exec AttemptExecutor, gate *AttemptCommitGate, params *executors.ExecParams) *AttemptResult {
	if params == nil {
		return &AttemptResult{
			Success:     false,
			CommitState: CommitStateNone,
			SafeRetry:   false,
			FinalError:  fmt.Errorf("execute attempt: nil params"),
		}
	}
	attemptParams := *params
	attemptParams.SurvivalAttempt = true
	if params.R != nil {
		attemptParams.R = params.R.WithContext(ctx)
	}

	result, err := exec.Execute(&attemptParams)
	state := commitStateOf(gate)

	if err == nil {
		rt := ResponseTypeNonStream
		if attemptParams.IsStream {
			rt = ResponseTypeStream
		}
		return &AttemptResult{
			Success:      true,
			ExecResult:   result,
			CommitState:  state,
			ResponseType: rt,
			SafeRetry:    false,
			FinalError:   nil,
		}
	}

	res := &AttemptResult{
		Success:           false,
		CandidateOutcomes: foldCandidateOutcomes(err, params.RequestID, params.RoutingTracker),
		CommitState:       state,
		FinalError:        err,
	}
	res.SafeRetry = !gateCommitted(gate) && state < CommitStateContent
	return res
}

// commitStateOf reads the gate's final state; nil gate (survival disabled)
// reads as none.
func commitStateOf(gate *AttemptCommitGate) CommitState {
	if gate == nil {
		return CommitStateNone
	}
	return gate.State()
}

func gateCommitted(gate *AttemptCommitGate) bool {
	return gate != nil && gate.Committed()
}

// foldCandidateOutcomes converts the executor's ExecuteError (the only
// failure shape Execute returns) into per-candidate outcomes. A candidate
// walk that never started (no candidates planned) synthesizes the single
// no_available_channel outcome so the task aggregator sees a wait-recovery
// signal instead of an empty result (which fail-closes).
//
// 2026-09-04: request_id propagation for chain traceability.
// 2026-09-05 审计闭环2：dispatch_v2 路径从不填充 ExecuteError.Attempts
// （全仓无追加点），导致真实多候选失败在聚合器眼里是
// no_candidate_outcomes → fail-closed，且丢失全部 per-candidate 诊断。
// 现在当 Attempts 为空时，从 RoutingAttemptsTracker 的真实失败尝试合成
// 候选结果，并统一补齐 request_id / attempt_seq / supplier /
// http_status / retryable / latency / stage 诊断维度。
func foldCandidateOutcomes(err error, requestID string, tracker *executors.RoutingAttemptsTracker) []CandidateOutcome {
	execErr, ok := err.(*executors.ExecuteError)
	if !ok {
		kind := errorsx.ClassifyError(err, nil)
		// Log non-ExecuteError failures for debugging
		slog.Warn("fold_candidate_outcomes_non_exec_error",
			"error_type", fmt.Sprintf("%T", err),
			"error", err.Error(),
			"classified_kind", string(kind),
		)
		return []CandidateOutcome{{Kind: kind, Err: err, RequestID: requestID}}
	}

	outcomes := make([]CandidateOutcome, 0, len(execErr.Attempts))

	// Enhanced logging: record all attempted candidates for observability
	attemptSummary := make([]map[string]any, 0, len(execErr.Attempts))
	for i, a := range execErr.Attempts {
		candidateID := "provider:" + strconv.Itoa(a.ProviderID) + "/model:" + a.RawModel
		retryable := errorsx.ProjectRecovery(a.Kind).GenericRetryable
		outcomes = append(outcomes, CandidateOutcome{
			CandidateID:  candidateID,
			CredentialID: strconv.Itoa(a.CredentialID),
			ProviderID:   a.ProviderID,
			Kind:         a.Kind,
			Err:          execErr.LastErr,
			RequestID:    requestID,
			AttemptSeq:   i + 1,
			Supplier:     a.Supplier,
			HTTPStatus:   a.HTTPStatus,
			Retryable:    &retryable,
			LatencyMs:    a.LatencyMs,
			Stage:        a.Stage,
		})

		attemptSummary = append(attemptSummary, map[string]any{
			"provider_id":   a.ProviderID,
			"credential_id": a.CredentialID,
			"raw_model":     a.RawModel,
			"kind":          string(a.Kind),
		})
	}

	// dispatch_v2 路径：ExecuteError.Attempts 从未被生产代码填充。从路由
	// 追踪器恢复真实失败候选（含 latency/http_status/kind/stage）。
	if len(outcomes) == 0 {
		for _, fa := range tracker.FailedAttempts() {
			kind := errorsx.ErrorKind(fa.ErrorKind)
			if kind == "" {
				kind = classifyRoutingAttemptKind(fa)
			}
			retryable := errorsx.ProjectRecovery(kind).GenericRetryable
			outcomes = append(outcomes, CandidateOutcome{
				CandidateID:  "provider:" + strconv.Itoa(int(fa.ProviderID)) + "/model:" + fa.RawModel,
				CredentialID: strconv.FormatInt(fa.CredentialID, 10),
				ProviderID:   int(fa.ProviderID),
				Kind:         kind,
				Err:          execErr.LastErr,
				RequestID:    requestID,
				AttemptSeq:   fa.Seq,
				Supplier:     fa.ProviderName,
				HTTPStatus:   fa.HTTPStatus,
				Retryable:    &retryable,
				LatencyMs:    fa.LatencyMs,
				Stage:        fa.Stage,
			})
		}
		if len(outcomes) > 0 {
			slog.Info("fold_candidate_outcomes_from_tracker",
				"request_id", requestID,
				"outcome_count", len(outcomes),
				"last_kind", string(execErr.LastKind),
			)
			return outcomes
		}
	}

	if len(outcomes) == 0 {
		kind := execErr.LastKind
		if kind == "" {
			kind = errorsx.KindNoAvailableChannel
		}

		// Log when no candidates were attempted
		logAttrs := []any{
			"last_kind", string(execErr.LastKind),
			"synthesized_kind", string(kind),
			"last_error", func() string {
				if execErr.LastErr != nil {
					return execErr.LastErr.Error()
				}
				return "nil"
			}(),
		}
		if requestID != "" {
			logAttrs = append(logAttrs, "request_id", requestID)
		}
		slog.Warn("fold_candidate_outcomes_no_attempts", logAttrs...)

		return []CandidateOutcome{{Kind: kind, Err: execErr.LastErr, RequestID: requestID}}
	}

	// Log the complete candidate outcome fold for debugging
	logAttrs := []any{
		"attempt_count", len(execErr.Attempts),
		"outcome_count", len(outcomes),
		"attempts", attemptSummary,
		"last_kind", string(execErr.LastKind),
	}
	if requestID != "" {
		logAttrs = append(logAttrs, "request_id", requestID)
	}
	slog.Info("fold_candidate_outcomes_complete", logAttrs...)

	return outcomes
}

// classifyRoutingAttemptKind 从追踪器记录的 result / http_status 推导
// errorsx kind（tracker 条目没有显式 kind 时的兜底分类）。
func classifyRoutingAttemptKind(a executors.RoutingAttempt) errorsx.ErrorKind {
	switch a.Result {
	case "timeout":
		return errorsx.KindTimeout
	case "canceled":
		return errorsx.KindCanceled
	case "rate_limit":
		return errorsx.KindRateLimit
	case "unauthorized":
		return errorsx.KindAuth
	case "model_not_found":
		return errorsx.KindModelNotFound
	default:
		if a.HTTPStatus >= 500 {
			return errorsx.KindUpstreamOverloaded
		}
		return errorsx.KindTransient
	}
}
