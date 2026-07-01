// Package workers 包含 V4 治理平台的异步分析 worker 实现。
//
// 当前实现：
//   - IntentWorker：订阅 request.completed，对 payload 中的 user_content 做
//     轻量意图分类，按 (tenant_id, intent_kind) 累计；
//     FlushAndReset 把累计 delta upsert 到 assets.IntentAggregateStore
//     然后清零（PR-V4-10）。
package workers

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/assets"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// IntentWorker 意图分类 worker。
type IntentWorker struct {
	logger *slog.Logger

	mu     sync.Mutex
	counts map[string]map[analysis.IntentKind]int64
}

// NewIntentWorker 构造 worker。
func NewIntentWorker(logger *slog.Logger) *IntentWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &IntentWorker{
		logger: logger,
		counts: make(map[string]map[analysis.IntentKind]int64),
	}
}

// Name 返回 worker 名。
func (w *IntentWorker) Name() string { return "intent_worker" }

// SubscribedTypes 返回订阅的事件类型。
func (w *IntentWorker) SubscribedTypes() []analysis.EventType {
	return []analysis.EventType{analysis.EventRequestCompleted}
}

// Handle 处理一条事件；返回 error 不影响其他事件。
func (w *IntentWorker) Handle(ctx context.Context, evt analysis.AnalysisEvent) error {
	if evt.Type != analysis.EventRequestCompleted {
		return nil
	}
	content, ok := extractUserContent(evt.Payload)
	if !ok || content == "" {
		return nil
	}
	kind := classifyIntent(content)
	w.mu.Lock()
	tBucket, ok := w.counts[evt.TenantID]
	if !ok {
		tBucket = make(map[analysis.IntentKind]int64, 4)
		w.counts[evt.TenantID] = tBucket
	}
	tBucket[kind]++
	w.mu.Unlock()
	w.logger.Debug("intent_worker: classified",
		"event_id", evt.EventID,
		"tenant_id", evt.TenantID,
		"kind", kind,
		"content_len", len(content),
	)
	return nil
}

// Snapshot 返回当前 in-memory 计数快照（仅供 telemetry / 测试）。
func (w *IntentWorker) Snapshot() map[string]map[analysis.IntentKind]int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]map[analysis.IntentKind]int64, len(w.counts))
	for tenant, bucket := range w.counts {
		copied := make(map[analysis.IntentKind]int64, len(bucket))
		for k, v := range bucket {
			copied[k] = v
		}
		out[tenant] = copied
	}
	return out
}

// FlushAndReset 把累计 delta 写入 store 并清零。
func (w *IntentWorker) FlushAndReset(ctx context.Context, store assets.IntentAggregateStore) error {
	if store == nil {
		w.mu.Lock()
		w.counts = make(map[string]map[analysis.IntentKind]int64)
		w.mu.Unlock()
		return nil
	}

	w.mu.Lock()
	snap := w.counts
	w.counts = make(map[string]map[analysis.IntentKind]int64)
	w.mu.Unlock()

	var firstErr error
	for tenant, bucket := range snap {
		if tenant == "" {
			continue
		}
		for kind, delta := range bucket {
			if delta <= 0 {
				continue
			}
			if err := store.Increment(ctx, tenant, kind, delta); err != nil && firstErr == nil {
				firstErr = err
				w.logger.Warn("intent_worker: flush Increment failed",
					"tenant_id", tenant, "kind", kind, "delta", delta, "error", err)
			}
		}
	}
	return firstErr
}

// extractUserContent 从事件 payload 中提取 user_content 字段。
func extractUserContent(payload any) (string, bool) {
	if payload == nil {
		return "", false
	}
	if m, ok := payload.(map[string]any); ok {
		if s, ok := m["user_content"].(string); ok {
			return s, true
		}
	}
	return "", false
}

// classifyIntent 轻量关键词分类。
func classifyIntent(content string) analysis.IntentKind {
	lc := strings.ToLower(content)
	switch {
	case containsAny(lc, []string{"code", "function", "var ", "class "}):
		return analysis.IntentCode
	case containsAny(lc, []string{"why", "explain", "reason", "because"}):
		return analysis.IntentReasoning
	case containsAny(lc, []string{"hello", "hi", "你好"}):
		return analysis.IntentChat
	default:
		return analysis.IntentUnclassified
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
