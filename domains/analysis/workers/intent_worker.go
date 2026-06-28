// Package workers 包含 V4 治理平台的异步分析 worker 实现（PR-V4-06 引入首个）。
//
// 当前实现：
//   - IntentWorker：订阅 request.completed，对 payload 中的 user_content 做
//     轻量意图分类，仅 in-memory 累计命中次数（不写库；DB 写入留给
//     PR-V4-07 assets 层）。
package workers

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

// IntentWorker 意图分类 worker（PR-V4-06 占位实现）。
//
// 行为：
//   - 订阅 EventRequestCompleted
//   - 从 payload 中提取 user_content 字符串（缺失则跳过）
//   - 按关键词命中分类为 chat / code / reasoning / unclassified
//   - 累加到内部 counter；不写库
//
// PR-V4-07 起将累加结果写入 assets.IntentAggregateStore。
type IntentWorker struct {
	logger *slog.Logger

	mu     sync.Mutex
	counts map[analysis.IntentKind]int
}

// NewIntentWorker 构造 worker。
func NewIntentWorker(logger *slog.Logger) *IntentWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &IntentWorker{
		logger: logger,
		counts: make(map[analysis.IntentKind]int),
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
	w.counts[kind]++
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
func (w *IntentWorker) Snapshot() map[analysis.IntentKind]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[analysis.IntentKind]int, len(w.counts))
	for k, v := range w.counts {
		out[k] = v
	}
	return out
}

// extractUserContent 从事件 payload 中提取 user_content 字段。
//
// 容忍多种 payload 形态：
//   - map[string]any{"user_content": string}
//   - 结构体（通过 type switch / 反射留给后续 PR；当前只支持 map）
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

// classifyIntent 轻量关键词分类（生产应替换为 ML 模型调用）。
//
// 与 v3 IntentAnalyzer 规则保持一致以保证可对比：
//   - code: "code" / "function" / "var " / "class "
//   - reasoning: "why" / "explain" / "reason" / "because"
//   - chat: "hello" / "hi" / "你好"
//   - 否则 unclassified
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
