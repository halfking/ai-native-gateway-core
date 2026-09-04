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
		CandidateOutcomes: foldCandidateOutcomes(err, params.RequestID),
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
// 2026-09-04: Enhanced with request_id propagation for chain traceability.
func foldCandidateOutcomes(err error, requestID string) []CandidateOutcome {
	execErr, ok := err.(*executors.ExecuteError)
	if !ok {
		kind := errorsx.ClassifyError(err, nil)
		// Log non-ExecuteError failures for debugging
		slog.Warn("fold_candidate_outcomes_non_exec_error",
			"error_type", fmt.Sprintf("%T", err),
			"error", err.Error(),
			"classified_kind", string(kind),
		)
		return []CandidateOutcome{{Kind: kind, Err: err}}
	}

	outcomes := make([]CandidateOutcome, 0, len(execErr.Attempts))

	// Enhanced logging: record all attempted candidates for observability
	attemptSummary := make([]map[string]any, 0, len(execErr.Attempts))
	for _, a := range execErr.Attempts {
		candidateID := "provider:" + strconv.Itoa(a.ProviderID) + "/model:" + a.RawModel
		outcomes = append(outcomes, CandidateOutcome{
			CandidateID:  candidateID,
			CredentialID: strconv.Itoa(a.CredentialID),
			ProviderID:   a.ProviderID,
			Kind:         a.Kind,
			Err:          execErr.LastErr,
		})

		attemptSummary = append(attemptSummary, map[string]any{
			"provider_id":   a.ProviderID,
			"credential_id": a.CredentialID,
			"raw_model":     a.RawModel,
			"kind":          string(a.Kind),
		})
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

		return []CandidateOutcome{{Kind: kind, Err: execErr.LastErr}}
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
