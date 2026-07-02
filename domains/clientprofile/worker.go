// Package clientprofile — ProfileWorker（Task E）。
//
// ProfileWorker 是事件总线的订阅者，把 analysis 层事件（request.completed /
// session.closed / approval.decided / failure.detected / tool.completed）
// 转换为 ClientBehaviorEvent，并异步触发 Aggregator.UpdateProfile 更新客户端画像。
//
// 设计要点：
//   - 不阻塞事件总线：Handle 把转换 + UpdateProfile 丢到独立 goroutine
//   - 转换失败/缺字段：返回 nil（不要把无效事件回灌到总线）
//   - 字段映射全部走 evt.Payload 强类型断言（map[string]any）
//   - 维护 in-memory pending 缓冲，提供 FlushAndReset 用于测试 / 周期刷写
package clientprofile

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ProfileWorker 订阅 analysis 事件，异步更新客户端画像。
type ProfileWorker struct {
	aggregator *Aggregator
	logger     *slog.Logger

	// asyncUpdate 控制是否把 UpdateProfile 投递到独立 goroutine。
	// 默认 true；测试可置 false 以同步验证。
	asyncUpdate bool

	// updateTimeout 单次 UpdateProfile 的超时时间。
	updateTimeout time.Duration

	mu      sync.Mutex
	pending map[string]*ClientBehaviorEvent
}

// NewProfileWorker 构造 worker。logger 为 nil 时使用 slog.Default()。
func NewProfileWorker(aggregator *Aggregator, logger *slog.Logger) *ProfileWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProfileWorker{
		aggregator:    aggregator,
		logger:        logger,
		asyncUpdate:   true,
		updateTimeout: 10 * time.Second,
		pending:       make(map[string]*ClientBehaviorEvent),
	}
}

// Name 实现 analysis.Worker。
func (w *ProfileWorker) Name() string { return "client_profile_worker" }

// SubscribedTypes 订阅的 analysis 事件类型。
//
// 与 domain/analysis 的常量保持一致；事件名走 EventType 字符串，bus
// 路由时按字符串匹配。
func (w *ProfileWorker) SubscribedTypes() []analysis.EventType {
	return []analysis.EventType{
		analysis.EventRequestCompleted, // 请求完成 → 总请求数/模型/任务类型
		analysis.EventSessionClosed,    // 会话关闭 → 总会话数
		analysis.EventApprovalDecided,  // 审批决议 → 审批率
		analysis.EventFailureDetected,  // 失败检测 → 错误率
		analysis.EventToolCompleted,    // 工具执行 → 任务类型推断线索
	}
}

// Handle 把 analysis 事件转换为 ClientBehaviorEvent，并异步触发更新。
//
// 返回值约定：
//   - 转换失败 / payload 缺失：返回 nil（不污染总线）
//   - 缺 identity_hash：返回 nil（无法归属到画像）
//   - UpdateProfile 异步执行：失败仅日志，不影响主流程
func (w *ProfileWorker) Handle(ctx context.Context, evt analysis.AnalysisEvent) error {
	if w.aggregator == nil {
		return nil
	}
	if !w.shouldProcess(evt.Type) {
		return nil
	}
	behavior, err := w.convertEvent(evt)
	if err != nil {
		w.logger.Debug("client_profile_worker: skip event",
			"event_id", evt.EventID,
			"type", evt.Type,
			"reason", err.Error())
		return nil
	}
	if behavior.IdentityHash == "" {
		w.logger.Debug("client_profile_worker: skip event without identity_hash",
			"event_id", evt.EventID, "type", evt.Type)
		return nil
	}

	// 先把事件挂到 pending（用于 FlushAndReset / 调试）。
	w.bufferEvent(behavior)

	if !w.asyncUpdate {
		return w.aggregator.UpdateProfile(ctx, behavior)
	}

	// 异步触发：派生 context 避免上游 ctx 取消时中断画像更新。
	go func(b *ClientBehaviorEvent) {
		updateCtx, cancel := context.WithTimeout(context.Background(), w.updateTimeout)
		defer cancel()
		if uErr := w.aggregator.UpdateProfile(updateCtx, b); uErr != nil {
			w.logger.Warn("client_profile_worker: UpdateProfile failed",
				"event_id", b.EventID,
				"identity_hash", safePrefix(b.IdentityHash),
				"event_type", b.EventType,
				"error", uErr)
		}
	}(behavior)

	return nil
}

// SetAsync 控制是否异步更新。true = 默认行为；测试可置 false。
func (w *ProfileWorker) SetAsync(async bool) { w.asyncUpdate = async }

// SetUpdateTimeout 设置单次 UpdateProfile 超时。<=0 恢复默认值。
func (w *ProfileWorker) SetUpdateTimeout(d time.Duration) {
	if d <= 0 {
		d = 10 * time.Second
	}
	w.updateTimeout = d
}

// FlushAndReset 同步刷新 pending 缓冲并清空。
//
// 适用于测试 / 优雅停机场景。生产环境 UpdateProfile 已经由 goroutine
// 直接调用，本方法主要用于校验 in-flight 事件。
func (w *ProfileWorker) FlushAndReset(ctx context.Context) error {
	w.mu.Lock()
	pending := w.pending
	w.pending = make(map[string]*ClientBehaviorEvent)
	w.mu.Unlock()

	var firstErr error
	for _, event := range pending {
		if err := w.aggregator.UpdateProfile(ctx, event); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			w.logger.Warn("client_profile_worker: flush UpdateProfile failed",
				"event_id", event.EventID,
				"identity_hash", safePrefix(event.IdentityHash),
				"error", err)
		}
	}
	return firstErr
}

// PendingCount 返回当前待处理事件数（仅供 telemetry / 测试）。
func (w *ProfileWorker) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

func (w *ProfileWorker) shouldProcess(t analysis.EventType) bool {
	for _, subscribed := range w.SubscribedTypes() {
		if subscribed == t {
			return true
		}
	}
	return false
}

func (w *ProfileWorker) bufferEvent(b *ClientBehaviorEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[b.EventID] = b
}

func (w *ProfileWorker) convertEvent(evt analysis.AnalysisEvent) (*ClientBehaviorEvent, error) {
	payload, ok := evt.Payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload is not map[string]any: %T", evt.Payload)
	}

	behavior := &ClientBehaviorEvent{
		EventID:   evt.EventID,
		TenantID:  evt.TenantID,
		SessionID: evt.SessionID,
		RequestID: evt.RequestID,
		Timestamp: evt.OccurredAt,
	}
	if behavior.Timestamp.IsZero() {
		behavior.Timestamp = time.Now()
	}

	// 通用字段提取（缺字段保持零值，调用方按需判断）。
	behavior.IdentityHash = stringField(payload, "identity_hash")
	if behavior.SessionID == "" {
		behavior.SessionID = stringField(payload, "session_id")
	}
	if behavior.RequestID == "" {
		behavior.RequestID = stringField(payload, "request_id")
	}
	behavior.Model = stringField(payload, "model")
	behavior.TaskType = stringField(payload, "task_type")
	if behavior.TaskType == "" {
		behavior.TaskType = stringField(payload, "intent")
	}
	behavior.TokensUsed = intField(payload, "tokens_used")
	behavior.LatencyMs = int64Field(payload, "latency_ms")
	behavior.Success = boolField(payload, "success")

	// 按事件类型映射 EventType 与 Success 默认值。
	switch evt.Type {
	case analysis.EventRequestCompleted:
		behavior.EventType = EventTypeRequestCompleted
		// request.completed 缺 success 字段视为 true
		if _, has := payload["success"]; !has {
			behavior.Success = true
		}
	case analysis.EventSessionClosed:
		behavior.EventType = EventTypeSessionStart
	case analysis.EventApprovalDecided:
		behavior.EventType = EventTypeApprovalRequired
		behavior.Success = boolField(payload, "approved")
	case analysis.EventFailureDetected:
		behavior.EventType = EventTypeError
		behavior.Success = false
	case analysis.EventToolCompleted:
		// 工具完成事件复用 request_completed，附加 task_type 推断线索
		behavior.EventType = EventTypeRequestCompleted
		if behavior.TaskType == "" {
			if name := stringField(payload, "tool_name"); name != "" {
				behavior.TaskType = TaskTypeUnknown
			}
		}
	default:
		return nil, fmt.Errorf("unsupported event type: %s", evt.Type)
	}

	return behavior, nil
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intField(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func int64Field(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func boolField(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func safePrefix(s string) string {
	if len(s) >= 16 {
		return s[:16]
	}
	return s
}
