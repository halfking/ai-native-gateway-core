package sessioninspector

import (
	"time"
)

// TokenLimitInspector token 超限检查器。
//
// 当 snap.TokenCount > maxTokens 时返回 Warning 级别 Finding。
type TokenLimitInspector struct {
	maxTokens int
}

// NewTokenLimitInspector 构造检查器。
// maxTokens <= 0 时使用默认 100000。
func NewTokenLimitInspector(maxTokens int) *TokenLimitInspector {
	if maxTokens <= 0 {
		maxTokens = 100000
	}
	return &TokenLimitInspector{maxTokens: maxTokens}
}

// Name 返回检查器名。
func (i *TokenLimitInspector) Name() string { return "token_limit" }

// Inspect 检查 token 是否超限。
func (i *TokenLimitInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if snap.TokenCount > i.maxTokens {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityWarning,
			Code:          "TOKEN_LIMIT_EXCEEDED",
			Message:       "session token count exceeds limit",
			Suggestion:    "consider starting a new session",
			DetectedAt:    time.Now(),
		}}, nil
	}
	return nil, nil
}

// InactiveInspector 会话闲置检查器。
//
// 当 snap.LastActiveAt 距今超过 maxIdle 时返回 Info 级别 Finding。
type InactiveInspector struct {
	maxIdle time.Duration
}

// NewInactiveInspector 构造检查器。
// maxIdle <= 0 时使用默认 30 分钟。
func NewInactiveInspector(maxIdle time.Duration) *InactiveInspector {
	if maxIdle <= 0 {
		maxIdle = 30 * time.Minute
	}
	return &InactiveInspector{maxIdle: maxIdle}
}

// Name 返回检查器名。
func (i *InactiveInspector) Name() string { return "inactive" }

// Inspect 检查会话是否闲置过久。
func (i *InactiveInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if snap.LastActiveAt.IsZero() {
		// 没有 last active 信息 -> 跳过
		return nil, nil
	}
	idle := time.Since(snap.LastActiveAt)
	if idle > i.maxIdle {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityInfo,
			Code:          "SESSION_IDLE",
			Message:       "session has been idle for too long",
			Suggestion:    "consider reclaiming session resources",
			DetectedAt:    time.Now(),
		}}, nil
	}
	return nil, nil
}

// HighFrequencyInspector 高频请求检查器。
//
// 当 snap.RequestCount > maxPerMinute 时返回 Warning 级别 Finding。
type HighFrequencyInspector struct {
	maxPerMinute int
}

// NewHighFrequencyInspector 构造检查器。
// maxPerMinute <= 0 时使用默认 60。
func NewHighFrequencyInspector(maxPerMinute int) *HighFrequencyInspector {
	if maxPerMinute <= 0 {
		maxPerMinute = 60
	}
	return &HighFrequencyInspector{maxPerMinute: maxPerMinute}
}

// Name 返回检查器名。
func (i *HighFrequencyInspector) Name() string { return "high_frequency" }

// Inspect 检查请求频次。
func (i *HighFrequencyInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if snap.RequestCount > i.maxPerMinute {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityWarning,
			Code:          "HIGH_REQUEST_RATE",
			Message:       "session request rate exceeds threshold",
			Suggestion:    "consider throttling or rate limiting",
			DetectedAt:    time.Now(),
		}}, nil
	}
	return nil, nil
}
