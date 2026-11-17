package securityevent

// 各检测面 → 统一契约的适配器。
//
// 适配器只做三件事：**归一化严重度**、**映射处置动作**、**把原始细节原样
// 放进 Metadata**。它不重新裁决"要不要拦"，也不丢任何原始字段 —— 归一化是
// 为了让消费侧能跨检测面比较，不是为了让原始语义消失。

import (
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/anomaly"
)

// FromAnomaly 把 internal/anomaly 的检测结果适配成统一契约。
//
// action 由调用方给出：检测面自己知道这次是"只观察"、"打了标记"还是
// "真的拦了"，适配器不猜。
func FromAnomaly(result anomaly.AnomalyResult, scope Scope, action Action, detectedAt time.Time) Event {
	severity, known := NormalizeSeverity(result.Severity)
	if !known {
		// 未知刻度**不**当成 info —— 那是"把没看懂当成没问题"，是最危险
		// 的误读方向。未知的刻度按 high 兜底（宁可多报），并把原值留在
		// raw_severity 里让人能回头查。
		severity = SeverityHigh
	}
	metrics := make(map[string]any, len(result.Metrics)+4)
	for key, value := range result.Metrics {
		metrics[key] = value
	}
	metrics["raw_severity"] = result.Severity
	metrics["raw_action"] = string(action)
	metrics["observe_only"] = result.ObserveOnly

	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	idKey := strings.Join([]string{
		result.Code,
		scope.SessionID,
		scope.RequestID,
		strconv.FormatInt(scope.APIKeyID, 10),
		detectedAt.UTC().Format(time.RFC3339Nano),
	}, "|")

	return NewEvent(
		EventTypeAnomalyDetected,
		SourceAnomaly,
		scope,
		result.Code,
		severity,
		action,
		result.Message,
		idKey,
		detectedAt,
		metrics,
	)
}

// FromAction 把一个检测面的原始动作折算成统一动作。
//
// 原始动作保留在 raw_action 里；未知动作按 flagged 兜底（"看到了但不一定
// 拦了"），并把原值留在 metadata。
func ActionFromRaw(raw string) Action {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "observed", "observe", "observe_only":
		return ActionObserved
	case "blocked", "block", "deny", "denied", "reject", "rejected":
		return ActionBlocked
	case "flagged", "flag", "warn", "warning", "pass", "passed", "allow", "allowed":
		return ActionFlagged
	default:
		return ActionFlagged
	}
}
