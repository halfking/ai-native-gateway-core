package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Tracker 工具执行追踪器。
//
// 提供从"工具调用开始"到"完成/失败/超时"全生命周期的记录 API。
// 任何存储错误都会被记录到日志但不会向上抛——追踪失败不能阻塞
// 真实的工具调用。
type Tracker struct {
	store  Store
	logger *slog.Logger
}

// NewTracker 构造一个 Tracker。logger 为 nil 时使用 slog.Default()。
func NewTracker(store Store, logger *slog.Logger) *Tracker {
	if store == nil {
		panic("toolexecution: nil store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{store: store, logger: logger}
}

// RecordStart 记录工具调用开始。
//
//   - 自动生成 ExecutionID（uuid v4）
//   - 状态置为 pending
//   - 设置 StartedAt = CreatedAt = time.Now()
//
// 返回 execution_id 供调用方在 RecordSuccess/Error/Timeout 时回传。
// 存储错误返回时 ExecutionID 仍会被填到 exec 上但 err 非 nil，
// 调用方应自行决定是否继续。
func (t *Tracker) RecordStart(ctx context.Context, exec *ToolExecution) (string, error) {
	if exec == nil {
		return "", errors.New("toolexecution: nil exec")
	}
	if exec.ExecutionID == "" {
		exec.ExecutionID = uuid.NewString()
	}
	now := time.Now().UTC()
	exec.StartedAt = now
	exec.CreatedAt = now
	exec.Status = StatusPending

	if err := t.store.Save(ctx, exec); err != nil {
		t.logger.Error("toolexecution: record start failed",
			"execution_id", exec.ExecutionID,
			"tool", exec.ToolName,
			"session_id", exec.SessionID,
			"error", err,
		)
		return exec.ExecutionID, err
	}
	return exec.ExecutionID, nil
}

// RecordSuccess 记录工具调用成功完成并写入结果。
//
// 终态字段：
//   - Status = success
//   - Result = result
//   - CompletedAt = time.Now()
//   - DurationMs = CompletedAt - StartedAt（毫秒）
//
// 记录不存在时直接返回 ErrNotFound（不创建新记录）。
func (t *Tracker) RecordSuccess(ctx context.Context, executionID string, result json.RawMessage) error {
	return t.updateTerminal(ctx, executionID, func(exec *ToolExecution) {
		exec.Status = StatusSuccess
		exec.Result = result
	})
}

// RecordError 记录工具调用执行失败。
//
// 终态字段：
//   - Status = error
//   - ErrorMessage = errorMsg
//   - ErrorType = errorType（为空时使用 ErrorTypeExecutionFail）
//   - CompletedAt = time.Now()
func (t *Tracker) RecordError(ctx context.Context, executionID, errorMsg, errorType string) error {
	if errorType == "" {
		errorType = ErrorTypeExecutionFail
	}
	return t.updateTerminal(ctx, executionID, func(exec *ToolExecution) {
		exec.Status = StatusError
		exec.ErrorMessage = errorMsg
		exec.ErrorType = errorType
	})
}

// RecordTimeout 记录工具调用超时。
//
// 终态字段：
//   - Status = timeout
//   - ErrorMessage = "Execution timeout"
//   - ErrorType = timeout
func (t *Tracker) RecordTimeout(ctx context.Context, executionID string) error {
	return t.updateTerminal(ctx, executionID, func(exec *ToolExecution) {
		exec.Status = StatusTimeout
		exec.ErrorMessage = "Execution timeout"
		exec.ErrorType = ErrorTypeTimeout
	})
}

// updateTerminal 共用的"写入终止态"逻辑。
func (t *Tracker) updateTerminal(
	ctx context.Context,
	executionID string,
	mutate func(*ToolExecution),
) error {
	if executionID == "" {
		return errors.New("toolexecution: empty executionID")
	}
	err := t.store.Update(ctx, executionID, func(exec *ToolExecution) error {
		if exec.IsTerminal() {
			// 防止重复覆盖——日志告警但不阻断。
			t.logger.Warn("toolexecution: update on terminal record",
				"execution_id", executionID,
				"current_status", exec.Status,
			)
		}
		mutate(exec)
		exec.CompletedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		t.logger.Error("toolexecution: update terminal failed",
			"execution_id", executionID,
			"error", err,
		)
		return err
	}
	return nil
}

// GetBySession 获取某会话下的所有执行记录（按时间倒序）。
func (t *Tracker) GetBySession(ctx context.Context, sessionID string) ([]*ToolExecution, error) {
	return t.store.ListBySession(ctx, sessionID)
}

// GetByIdentity 获取某客户端最近 limit 条执行记录。
func (t *Tracker) GetByIdentity(ctx context.Context, identityHash string, limit int) ([]*ToolExecution, error) {
	return t.store.ListByIdentity(ctx, identityHash, limit)
}

// GetByExecutionID 获取单条执行记录。
func (t *Tracker) GetByExecutionID(ctx context.Context, executionID string) (*ToolExecution, error) {
	return t.store.Get(ctx, executionID)
}

// GetStats 获取某工具某天的统计。
func (t *Tracker) GetStats(ctx context.Context, toolName string, date time.Time) (*ToolUsageStats, error) {
	return t.store.GetStats(ctx, toolName, date)
}

// ListStats 返回某工具在时间窗口内的统计列表。
// toolName 为空时返回所有工具的统计。
func (t *Tracker) ListStats(ctx context.Context, toolName string, startTime, endTime time.Time, limit int) ([]*ToolUsageStats, error) {
	return t.store.ListStats(ctx, toolName, startTime, endTime, limit)
}
