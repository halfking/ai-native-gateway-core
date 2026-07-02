// Package clientprofile — EventEmitter（Task E）。
//
// EventEmitter 把 session/streaming 层的强类型上下文转换为
// analysis.AnalysisEvent，并通过 bus.Publisher 投递到事件总线。
//
// 设计原则：
//   - 发送失败不视为致命错误（task 设计：Publish 仅记录 warn，
//     业务主流程继续）
//   - 字段命名与 ProfileWorker.convertEvent 完全对齐
//   - 任务类型推断走关键字匹配（轻量，不调用 LLM）
package clientprofile

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// EventBus 抽象出 analysis 事件总线依赖，避免 clientprofile 包反向依赖
// domains/analysis/bus（防止循环依赖）。
type EventBus interface {
	Publish(ctx context.Context, evt analysis.AnalysisEvent) error
}

// EventEmitter 把客户端行为事件投递到事件总线。
type EventEmitter struct {
	bus    EventBus
	logger *slog.Logger
}

// NewEventEmitter 构造发射器。bus == nil 时所有 Emit 方法为 no-op。
func NewEventEmitter(bus EventBus, logger *slog.Logger) *EventEmitter {
	if logger == nil {
		logger = slog.Default()
	}
	return &EventEmitter{bus: bus, logger: logger}
}

// EmitSessionStarted 发送会话开始事件（analysis.EventSessionClosed 复用为会话统计入口）。
//
// 实际映射：把 "session started" 转成 session.closed 的对偶事件，便于
// 现有 worker（session.closed 订阅者）无需新增类型即可消费。
func (e *EventEmitter) EmitSessionStarted(
	ctx context.Context,
	sc *session.SessionContext,
	identityHash string,
) error {
	if e == nil || e.bus == nil || sc == nil {
		return nil
	}
	evt := analysis.AnalysisEvent{
		EventID:    uuid.NewString(),
		Type:       analysis.EventSessionClosed,
		TenantID:   sc.TenantID,
		SessionID:  sc.SessionID,
		RequestID:  sc.RequestID,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": identityHash,
			"session_id":    sc.SessionID,
			"model":         modelOrFallback(sc),
		},
	}
	return e.publish(ctx, evt, "session.started")
}

// EmitRequestCompleted 发送请求完成事件。
func (e *EventEmitter) EmitRequestCompleted(
	ctx context.Context,
	sc *session.SessionContext,
	identityHash string,
	success bool,
	tokensUsed int,
	latencyMs int64,
) error {
	if e == nil || e.bus == nil || sc == nil {
		return nil
	}
	evt := analysis.AnalysisEvent{
		EventID:    uuid.NewString(),
		Type:       analysis.EventRequestCompleted,
		TenantID:   sc.TenantID,
		SessionID:  sc.SessionID,
		RequestID:  sc.RequestID,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": identityHash,
			"session_id":    sc.SessionID,
			"request_id":    sc.RequestID,
			"model":         modelOrFallback(sc),
			"success":       success,
			"tokens_used":   tokensUsed,
			"latency_ms":    latencyMs,
			"task_type":     InferTaskType(sc),
		},
	}
	return e.publish(ctx, evt, "request.completed")
}

// EmitApprovalDecided 发送审批决议事件（通过/拒绝）。
func (e *EventEmitter) EmitApprovalDecided(
	ctx context.Context,
	sc *session.SessionContext,
	identityHash string,
	approved bool,
	approvalID string,
) error {
	if e == nil || e.bus == nil || sc == nil {
		return nil
	}
	evt := analysis.AnalysisEvent{
		EventID:    uuid.NewString(),
		Type:       analysis.EventApprovalDecided,
		TenantID:   sc.TenantID,
		SessionID:  sc.SessionID,
		RequestID:  sc.RequestID,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": identityHash,
			"session_id":    sc.SessionID,
			"request_id":    sc.RequestID,
			"approval_id":   approvalID,
			"approved":      approved,
		},
	}
	return e.publish(ctx, evt, "approval.decided")
}

// EmitFailureDetected 发送失败事件。
func (e *EventEmitter) EmitFailureDetected(
	ctx context.Context,
	sc *session.SessionContext,
	identityHash string,
	reason string,
) error {
	if e == nil || e.bus == nil || sc == nil {
		return nil
	}
	evt := analysis.AnalysisEvent{
		EventID:    uuid.NewString(),
		Type:       analysis.EventFailureDetected,
		TenantID:   sc.TenantID,
		SessionID:  sc.SessionID,
		RequestID:  sc.RequestID,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": identityHash,
			"session_id":    sc.SessionID,
			"request_id":    sc.RequestID,
			"model":         modelOrFallback(sc),
			"success":       false,
			"reason":        reason,
		},
	}
	return e.publish(ctx, evt, "failure.detected")
}

// EmitToolCompleted 发送工具完成事件（任务类型推断线索）。
func (e *EventEmitter) EmitToolCompleted(
	ctx context.Context,
	sc *session.SessionContext,
	identityHash string,
	toolName string,
	success bool,
) error {
	if e == nil || e.bus == nil || sc == nil {
		return nil
	}
	evt := analysis.AnalysisEvent{
		EventID:    uuid.NewString(),
		Type:       analysis.EventToolCompleted,
		TenantID:   sc.TenantID,
		SessionID:  sc.SessionID,
		RequestID:  sc.RequestID,
		OccurredAt: time.Now(),
		Payload: map[string]any{
			"identity_hash": identityHash,
			"session_id":    sc.SessionID,
			"request_id":    sc.RequestID,
			"tool_name":     toolName,
			"success":       success,
			"task_type":     TaskTypeUnknown,
		},
	}
	return e.publish(ctx, evt, "tool.completed")
}

// publish 统一发送并记录日志。失败仅 warn。
func (e *EventEmitter) publish(ctx context.Context, evt analysis.AnalysisEvent, alias string) error {
	if err := e.bus.Publish(ctx, evt); err != nil {
		e.logger.Warn("clientprofile.EventEmitter: publish failed",
			"alias", alias,
			"event_id", evt.EventID,
			"type", evt.Type,
			"error", err)
		return err
	}
	return nil
}

// modelOrFallback 提取模型名（优先 UpstreamModel，再 ClientModel）。
func modelOrFallback(sc *session.SessionContext) string {
	if sc.UpstreamModel != "" {
		return sc.UpstreamModel
	}
	return sc.ClientModel
}

// InferTaskType 从 SessionContext 推断任务类型（轻量关键字匹配）。
//
// 规则（与 IntentWorker.classifyIntent 保持一致）：
//   - 出现 code/function/class/var → code
//   - 出现 why/explain/reason → reasoning
//   - 出现 hello/hi/你好 → chat
//   - 其它 → unknown
func InferTaskType(sc *session.SessionContext) string {
	if sc == nil || sc.ClientIR == nil {
		return TaskTypeUnknown
	}
	// 拼接最后 4 条 user/assistant 文本作为推断样本
	const sampleMax = 4
	texts := make([]string, 0, sampleMax)
	for _, msg := range sc.ClientIR.Messages {
		if len(texts) >= sampleMax {
			break
		}
		for _, blk := range msg.Content {
			if blk.Type == "text" && blk.Text != "" {
				texts = append(texts, blk.Text)
			}
		}
	}
	joined := strings.ToLower(strings.Join(texts, "\n"))
	switch {
	case containsAny(joined, []string{"code", "function", "var ", "class ", "def ", "import "}):
		return TaskTypeCode
	case containsAny(joined, []string{"why", "explain", "reason", "because", "如何", "为什么"}):
		return TaskTypeReasoning
	case containsAny(joined, []string{"hello", "hi ", "你好", "嗨", "早上好"}):
		return TaskTypeChat
	default:
		return TaskTypeUnknown
	}
}

func containsAny(s string, keywords []string) bool {
	for _, k := range keywords {
		if k == "" {
			continue
		}
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
