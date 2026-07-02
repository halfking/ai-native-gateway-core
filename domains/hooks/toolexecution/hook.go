// Package toolexecutionhook 把工具执行追踪器 (toolexecution.Tracker) 接入
// 请求 Pipeline 与工具调用拦截 (domains/hooks/tools) 流程。
//
// 主要职责：
//   - 在工具调用前记录 start（pending）
//   - 在工具调用后根据结果记录 success / error / timeout
//   - 把 execution_id 存入 PipelineRequest.Metadata["tool_execution_id"]
//     以便后续阶段（如审计、指标）按 id 关联
//
// 该包不主动触发工具调用——它只是一层薄包装，由调用方（一般是
// ToolOrchestrator 或 streaming executor）按生命周期调用。
package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // pipeline hook, historical
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // pipeline hook, historical
	"github.com/kaixuan/llm-gateway-go/domains/toolexecution"
)

// MetadataExecutionIDKey 在 PipelineRequest.Metadata 中存储 execution_id。
// 后续阶段（如 audit、analytics）可据此关联到同一次工具调用。
const MetadataExecutionIDKey = "tool_execution_id"

// IdentityHashKey 在 PipelineRequest.Metadata 中存储客户端身份哈希。
// 与 domains/identity/hook.go 使用的 key 保持一致。
const IdentityHashKey = "identity_hash"

// SessionInfo 调用方在 BeforeToolCall 时提供的会话信息抽象。
//
// 设计动机：避免本包直接依赖 domains/session（保持包轻量、避免
// session 包重构影响）。调用方用一个小适配器把 SessionContext 转成
// 本接口即可。
type SessionInfo interface {
	GetSessionID() string
	GetRequestID() string
	GetTenantID() string
	GetClientModel() string
	GetIdentityHash() string
}

// Hook 工具执行追踪 hook。
type Hook struct {
	tracker *toolexecution.Tracker
	logger  *slog.Logger
}

// NewHook 构造一个 hook；tracker 必填，logger 为 nil 时使用 slog.Default()。
func NewHook(tracker *toolexecution.Tracker, logger *slog.Logger) *Hook {
	if tracker == nil {
		panic("toolexecutionhook: nil tracker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Hook{tracker: tracker, logger: logger}
}

// Name 返回 hook 名称。
func (h *Hook) Name() string { return "toolexecution.track" }

// Priority 返回执行优先级。
func (h *Hook) Priority() int { return 60 }

// Enabled 报告是否启用。
func (h *Hook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 不直接做任何事——真正的 start/end 追踪由调用方在
// 工具执行前后通过 BeforeToolCall/AfterToolCall 显式触发。
func (h *Hook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	return nil
}

// OnError hook 自身的错误处理（追踪失败不影响主流程）。
func (h *Hook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// BeforeToolCall 工具调用前记录 start。
//
// 参数：
//   - info: 当前请求的会话信息（实现 SessionInfo 接口的对象）
//   - toolName: 工具名
//   - toolCallID: LLM 返回的 tool_call_id / tool_use_id
//   - args: 工具参数
//
// 返回 execution_id 供调用方在 AfterToolCall 回传。
//
// 注意：追踪失败不会阻塞工具调用——错误会被记录到日志但不会向上抛。
func (h *Hook) BeforeToolCall(
	ctx context.Context,
	info SessionInfo,
	toolName, toolCallID string,
	args json.RawMessage,
) (string, error) {
	if info == nil {
		return "", errors.New("toolexecutionhook: nil session info")
	}
	exec := &toolexecution.ToolExecution{
		SessionID:    info.GetSessionID(),
		RequestID:    info.GetRequestID(),
		TenantID:     info.GetTenantID(),
		ToolName:     toolName,
		ToolCallID:   toolCallID,
		Arguments:    args,
		IdentityHash: info.GetIdentityHash(),
		Model:        info.GetClientModel(),
	}
	id, err := h.tracker.RecordStart(ctx, exec)
	if err != nil {
		h.logger.Warn("toolexecutionhook: record start returned error",
			"tool", toolName, "execution_id", id, "error", err,
		)
	}
	return id, err
}

// AfterToolCall 工具调用后记录终态。
//
// 参数：
//   - executionID: BeforeToolCall 返回的 id
//   - result: 工具结果（任意类型，内部 json.Marshal 后存储）
//   - callErr: 工具执行错误，nil 表示成功
//
// 不会向上抛错；所有错误仅记录到日志。
func (h *Hook) AfterToolCall(
	ctx context.Context,
	executionID string,
	result any,
	callErr error,
) {
	if executionID == "" {
		h.logger.Warn("toolexecutionhook: empty executionID on after")
		return
	}
	if callErr != nil {
		errType := classifyError(callErr)
		if err := h.tracker.RecordError(ctx, executionID, callErr.Error(), errType); err != nil {
			h.logger.Error("toolexecutionhook: record error failed",
				"execution_id", executionID, "error", err,
			)
		}
		return
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		if recErr := h.tracker.RecordError(ctx, executionID,
			"toolexecutionhook: marshal result: "+err.Error(),
			toolexecution.ErrorTypeExecutionFail,
		); recErr != nil {
			h.logger.Error("toolexecutionhook: record error failed",
				"execution_id", executionID, "error", recErr,
			)
		}
		return
	}
	if err := h.tracker.RecordSuccess(ctx, executionID, resultJSON); err != nil {
		h.logger.Error("toolexecutionhook: record success failed",
			"execution_id", executionID, "error", err,
		)
	}
}

// RecordTimeout 显式记录一次超时（用于外部超时检测器调用）。
func (h *Hook) RecordTimeout(ctx context.Context, executionID string) {
	if err := h.tracker.RecordTimeout(ctx, executionID); err != nil {
		h.logger.Error("toolexecutionhook: record timeout failed",
			"execution_id", executionID, "error", err,
		)
	}
}

// ClassifyErrorForTest 暴露给测试的分类函数。生产代码不应直接调用。
func ClassifyErrorForTest(err error) string { return classifyError(err) }

// classifyError 简单的错误分类：基于错误信息关键字判断 error_type。
// 这是一个保守实现——真正的错误分类应在调用方构造时显式指定。
func classifyError(err error) string {
	if err == nil {
		return toolexecution.ErrorTypeExecutionFail
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "timeout", "deadline", "超时"):
		return toolexecution.ErrorTypeTimeout
	case containsAny(msg, "network", "connection", "dial", "网络"):
		return toolexecution.ErrorTypeNetwork
	case containsAny(msg, "invalid", "argument", "参数"):
		return toolexecution.ErrorTypeInvalidArgs
	default:
		return toolexecution.ErrorTypeExecutionFail
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf 简单子串查找，避免引入 strings 以保持包轻量。
func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ pipeline.Hook = (*Hook)(nil)
