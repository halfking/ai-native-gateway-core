// execution_recorder.go — 执行结果记录器实现
package executors

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
)

const executionStateWriteTimeout = 5 * time.Second

// stateWriteContext detaches credential state persistence from the request
// connection while retaining request-scoped values for tracing and tenancy.
func stateWriteContext(parent context.Context) (context.Context, context.CancelFunc) {
	return runctx.DetachedTimeout(parent, executionStateWriteTimeout)
}

// ExecutionRecorderImpl 执行结果记录器实现
type ExecutionRecorderImpl struct {
	StateWriter *credential.Writer
}

// NewExecutionRecorder 创建新的记录器
func NewExecutionRecorder(writer *credential.Writer) *ExecutionRecorderImpl {
	return &ExecutionRecorderImpl{
		StateWriter: writer,
	}
}

// RecordOutcome 记录单次执行结果
func (h *ExecutionRecorderImpl) RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error {
	if h == nil || h.StateWriter == nil {
		return nil
	}
	writeCtx, cancel := stateWriteContext(ctx)
	defer cancel()

	if outcome.Success {
		if err := h.StateWriter.RestoreOnSuccess(writeCtx, outcome.CredentialID, outcome.CanonicalModel); err != nil {
			slog.Warn("execution_recorder: RestoreOnSuccess failed",
				"error", err,
				"credential_id", outcome.CredentialID,
				"model", outcome.CanonicalModel,
			)
			return err
		}

		slog.Debug("execution_recorder: success recorded",
			"credential_id", outcome.CredentialID,
			"model", outcome.CanonicalModel,
			"latency_ms", outcome.LatencyMs,
			"ttfb_ms", outcome.TTFBMs,
		)
	} else {
		failure := credential.Failure{
			Kind:   outcome.ErrorKind,
			Detail: outcome.ErrorDetail,
		}

		if err := h.StateWriter.WriteOnError(writeCtx, outcome.CredentialID, outcome.CanonicalModel, failure); err != nil {
			slog.Warn("execution_recorder: WriteOnError failed",
				"error", err,
				"credential_id", outcome.CredentialID,
				"model", outcome.CanonicalModel,
				"error_kind", outcome.ErrorKind,
			)
			return err
		}

		slog.Debug("execution_recorder: failure recorded",
			"credential_id", outcome.CredentialID,
			"model", outcome.CanonicalModel,
			"error_kind", outcome.ErrorKind,
			"latency_ms", outcome.LatencyMs,
		)
	}

	return nil
}
