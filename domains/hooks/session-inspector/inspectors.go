package sessioninspector

import (
	"fmt"
	"time"
)

// TokenLimitInspector token 超限检查器。
//
// 判定规则（结合 Config）：
//   - snap.TokenCount > max_total      → SeverityCritical, code=TOKEN_LIMIT_EXCEEDED
//   - snap.TokenCount > soft_threshold → SeverityWarning,  code=TOKEN_SOFT_WARNING
//
// 软警告触发后是否阻断取决于 cfg.IsBlockAction()。
// 当 max_total <= 0 时禁用此 inspector。
type TokenLimitInspector struct {
	maxTotal       int
	softThreshold  int
	warnAction     string
	includeOutput  bool
	resetCycle     string
}

// NewTokenLimitInspector 构造检查器（兼容旧 API：仅传硬上限）。
// 推荐使用 NewTokenLimitInspectorWithConfig() 读取完整配置。
func NewTokenLimitInspector(maxTokens int) *TokenLimitInspector {
	if maxTokens <= 0 {
		maxTokens = 100000
	}
	return &TokenLimitInspector{
		maxTotal:      maxTokens,
		softThreshold: maxTokens * 80 / 100,
		warnAction:    "log",
		includeOutput: true,
		resetCycle:    "never",
	}
}

// NewTokenLimitInspectorWithConfig 从 Config 构造检查器。
func NewTokenLimitInspectorWithConfig(cfg *Config) *TokenLimitInspector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &TokenLimitInspector{
		maxTotal:      cfg.TokenMaxTotal,
		softThreshold: cfg.SoftWarningThreshold(),
		warnAction:    cfg.TokenWarnAction,
		includeOutput: cfg.TokenIncludeOutput,
		resetCycle:    cfg.TokenResetCycle,
	}
}

// Name 返回检查器名。
func (i *TokenLimitInspector) Name() string { return "token_limit" }

// Inspect 检查 token 是否超限或软警告。
func (i *TokenLimitInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if i.maxTotal <= 0 {
		return nil, nil // 禁用
	}

	// 硬上限
	if snap.TokenCount > i.maxTotal {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityCritical,
			Code:          "TOKEN_LIMIT_EXCEEDED",
			Message:       fmt.Sprintf("session token count %d exceeds hard limit %d", snap.TokenCount, i.maxTotal),
			Suggestion:    "consider starting a new session or enabling compression",
			Metadata: map[string]any{
				"current":     snap.TokenCount,
				"max":         i.maxTotal,
				"over_pct":    (snap.TokenCount - i.maxTotal) * 100 / i.maxTotal,
				"include_output": i.includeOutput,
			},
			DetectedAt: time.Now(),
		}}, nil
	}

	// 软警告（仅在硬上限未触发时检查）
	if i.softThreshold > 0 && snap.TokenCount > i.softThreshold {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityWarning,
			Code:          "TOKEN_SOFT_WARNING",
			Message:       fmt.Sprintf("session token count %d reached soft threshold %d (%d%%)",
				snap.TokenCount, i.softThreshold, i.softThreshold*100/i.maxTotal),
			Suggestion:    "monitor usage; consider proactive compression",
			Metadata: map[string]any{
				"current":    snap.TokenCount,
				"threshold":  i.softThreshold,
				"pct_of_max": snap.TokenCount * 100 / i.maxTotal,
			},
			DetectedAt: time.Now(),
		}}, nil
	}

	return nil, nil
}

// IsBlockAction 暴露给 Hook 用于决定是否阻断（warn_action=block）。
func (i *TokenLimitInspector) IsBlockAction() bool {
	return i.warnAction == "block"
}

// InactiveInspector 会话闲置检查器。
//
// 判定规则：
//   - snap.LastActiveAt 距今 > idle.timeout          → SeverityWarning, code=SESSION_IDLE
//   - snap.StartedAt   距今 > absolute_max_lifetime   → SeverityError,   code=SESSION_EXPIRED
type InactiveInspector struct {
	maxIdle       time.Duration
	absoluteLimit time.Duration
	autoExtend    bool
	recycleAction string
}

// NewInactiveInspector 构造检查器（兼容旧 API）。
func NewInactiveInspector(maxIdle time.Duration) *InactiveInspector {
	if maxIdle <= 0 {
		maxIdle = 30 * time.Minute
	}
	return &InactiveInspector{
		maxIdle:       maxIdle,
		absoluteLimit: 168 * time.Hour,
		autoExtend:    true,
		recycleAction: "soft_close",
	}
}

// NewInactiveInspectorWithConfig 从 Config 构造检查器。
func NewInactiveInspectorWithConfig(cfg *Config) *InactiveInspector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &InactiveInspector{
		maxIdle:       cfg.IdleTimeout,
		absoluteLimit: cfg.IdleAbsoluteMaxLifetime,
		autoExtend:    cfg.LifecycleAutoExtend,
		recycleAction: cfg.IdleRecycleAction,
	}
}

// Name 返回检查器名。
func (i *InactiveInspector) Name() string { return "inactive" }

// Inspect 检查会话是否闲置过久或超期。
func (i *InactiveInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}

	// 1) 绝对生命周期
	if !snap.StartedAt.IsZero() && i.absoluteLimit > 0 {
		if age := time.Since(snap.StartedAt); age > i.absoluteLimit {
			return []*Finding{{
				InspectorName: i.Name(),
				Severity:      SeverityError,
				Code:          "SESSION_EXPIRED",
				Message:       fmt.Sprintf("session age %s exceeds absolute max lifetime %s", age, i.absoluteLimit),
				Suggestion:    "session exceeded max lifetime; must be closed",
				Metadata: map[string]any{
					"age_seconds":  int64(age.Seconds()),
					"max_seconds":  int64(i.absoluteLimit.Seconds()),
					"recycle_action": i.recycleAction,
				},
				DetectedAt: time.Now(),
			}}, nil
		}
	}

	// 2) 闲置超时
	if snap.LastActiveAt.IsZero() {
		return nil, nil // 没有 last active 信息 -> 跳过
	}
	idle := time.Since(snap.LastActiveAt)
	if i.maxIdle <= 0 {
		return nil, nil // 禁用闲置检查
	}
	if idle > i.maxIdle {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityWarning,
			Code:          "SESSION_IDLE",
			Message:       fmt.Sprintf("session has been idle for %s (max %s)", idle, i.maxIdle),
			Suggestion:    "consider reclaiming session resources",
			Metadata: map[string]any{
				"idle_seconds":    int64(idle.Seconds()),
				"max_idle_seconds": int64(i.maxIdle.Seconds()),
				"auto_extend":     i.autoExtend,
				"recycle_action":  i.recycleAction,
			},
			DetectedAt: time.Now(),
		}}, nil
	}

	return nil, nil
}

// HighFrequencyInspector 高频请求检查器。
//
// 判定规则：
//   - snap.RequestCountPerMinute > rpm_limit            → SeverityWarning, code=HIGH_REQUEST_RATE
//   - snap.RequestCount > burst_limit（短期窗口）       → SeverityCritical, code=BURST_EXCEEDED
//   - snap.ConcurrentCount > max_concurrent             → SeverityError, code=CONCURRENT_EXCEEDED
//
// observe_only 模式下仅返回 Warning 不返回 Error。
type HighFrequencyInspector struct {
	rpmLimit      int
	burstLimit    int
	burstWindowS  int
	maxConcurrent int
	strategy      string
	observeOnly   bool
}

// NewHighFrequencyInspector 构造检查器（兼容旧 API）。
func NewHighFrequencyInspector(maxPerMinute int) *HighFrequencyInspector {
	if maxPerMinute <= 0 {
		maxPerMinute = 60
	}
	return &HighFrequencyInspector{
		rpmLimit:      maxPerMinute,
		burstLimit:    maxPerMinute * 2,
		burstWindowS:  5,
		maxConcurrent: 4,
		strategy:      "sliding",
		observeOnly:   false,
	}
}

// NewHighFrequencyInspectorWithConfig 从 Config 构造检查器。
func NewHighFrequencyInspectorWithConfig(cfg *Config) *HighFrequencyInspector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &HighFrequencyInspector{
		rpmLimit:      cfg.RateRPM,
		burstLimit:    cfg.RateBurstLimit,
		burstWindowS:  cfg.RateBurstWindowS,
		maxConcurrent: cfg.RateMaxConcurrent,
		strategy:      cfg.RateStrategy,
		observeOnly:   cfg.RateObserveOnly,
	}
}

// Name 返回检查器名。
func (i *HighFrequencyInspector) Name() string { return "high_frequency" }

// Inspect 检查请求频次与并发。
func (i *HighFrequencyInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	var findings []*Finding

	// 1) 突发窗口超限（最严重）
	if i.burstLimit > 0 && snap.BurstCount > i.burstLimit {
		findings = append(findings, &Finding{
			InspectorName: i.Name(),
			Severity:      SeverityCritical,
			Code:          "BURST_EXCEEDED",
			Message:       fmt.Sprintf("burst request count %d in %ds window exceeds limit %d",
				snap.BurstCount, i.burstWindowS, i.burstLimit),
			Suggestion: "apply rate limiting or cooldown",
			Metadata: map[string]any{
				"burst_count":   snap.BurstCount,
				"burst_window":  i.burstWindowS,
				"burst_limit":   i.burstLimit,
				"strategy":      i.strategy,
				"observe_only":  i.observeOnly,
			},
			DetectedAt: time.Now(),
		})
	}

	// 2) RPM 超限
	if i.rpmLimit > 0 && snap.RequestCount > i.rpmLimit {
		sev := SeverityWarning
		if !i.observeOnly {
			sev = SeverityError
		}
		findings = append(findings, &Finding{
			InspectorName: i.Name(),
			Severity:      sev,
			Code:          "HIGH_REQUEST_RATE",
			Message:       fmt.Sprintf("session request count %d exceeds RPM limit %d", snap.RequestCount, i.rpmLimit),
			Suggestion:    "consider throttling or rate limiting",
			Metadata: map[string]any{
				"request_count": snap.RequestCount,
				"rpm_limit":     i.rpmLimit,
				"strategy":      i.strategy,
				"observe_only":  i.observeOnly,
			},
			DetectedAt: time.Now(),
		})
	}

	// 3) 单会话并发超限
	if i.maxConcurrent > 0 && snap.ConcurrentCount > i.maxConcurrent {
		findings = append(findings, &Finding{
			InspectorName: i.Name(),
			Severity:      SeverityError,
			Code:          "CONCURRENT_EXCEEDED",
			Message:       fmt.Sprintf("concurrent request count %d exceeds max %d",
				snap.ConcurrentCount, i.maxConcurrent),
			Suggestion: "queue or reject new requests for this session",
			Metadata: map[string]any{
				"concurrent_count": snap.ConcurrentCount,
				"max_concurrent":   i.maxConcurrent,
			},
			DetectedAt: time.Now(),
		})
	}

	return findings, nil
}

// SessionLifecycleInspector 会话生命周期检查器（NEW）。
//
// 判定规则：
//   - snap.TenantActiveCount > max_sessions_per_tenant → SeverityWarning, code=TENANT_SESSION_LIMIT
//   - 根据 eviction_policy 给出不同 Suggestion
type SessionLifecycleInspector struct {
	maxPerTenant   int
	evictionPolicy string
}

// NewSessionLifecycleInspectorWithConfig 构造检查器。
func NewSessionLifecycleInspectorWithConfig(cfg *Config) *SessionLifecycleInspector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &SessionLifecycleInspector{
		maxPerTenant:   cfg.LifecycleMaxPerTenant,
		evictionPolicy: cfg.LifecycleEvictionPolicy,
	}
}

// Name 返回检查器名。
func (i *SessionLifecycleInspector) Name() string { return "session_lifecycle" }

// Inspect 检查租户级会话数量。
func (i *SessionLifecycleInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if i.maxPerTenant <= 0 {
		return nil, nil // 禁用
	}
	if snap.TenantActiveCount <= i.maxPerTenant {
		return nil, nil
	}

	suggestion := "monitor and wait for natural expiry"
	switch i.evictionPolicy {
	case "lru":
		suggestion = "oldest inactive session will be evicted (LRU)"
	case "fifo":
		suggestion = "oldest session will be evicted (FIFO)"
	case "none":
		suggestion = "no eviction; manually close excess sessions"
	}

	return []*Finding{{
		InspectorName: i.Name(),
		Severity:      SeverityWarning,
		Code:          "TENANT_SESSION_LIMIT",
		Message:       fmt.Sprintf("tenant has %d active sessions, exceeds limit %d",
			snap.TenantActiveCount, i.maxPerTenant),
		Suggestion:    suggestion,
		Metadata: map[string]any{
			"active_count":   snap.TenantActiveCount,
			"max_per_tenant": i.maxPerTenant,
			"eviction":       i.evictionPolicy,
		},
		DetectedAt: time.Now(),
	}}, nil
}

// ErrorRateInspector 会话错误率检查器（NEW）。
//
// 判定规则：
//   - snap.ErrorRate >= 0.5 (50%)  → SeverityError,   code=HIGH_ERROR_RATE
//   - snap.ErrorRate >= 0.2 (20%)  → SeverityWarning, code=ELEVATED_ERROR_RATE
//
// 与 session_health_api.go 的 ComputeHealth 共享 error_rate 字段。
type ErrorRateInspector struct {
	warnThreshold float64 // 0.2 默认
	blockThresh   float64 // 0.5 默认
}

// NewErrorRateInspectorWithConfig 构造检查器（阈值固定，参考 session_health.go 的 outcome 分类）。
func NewErrorRateInspectorWithConfig(_ *Config) *ErrorRateInspector {
	return &ErrorRateInspector{
		warnThreshold: 0.2,
		blockThresh:   0.5,
	}
}

// Name 返回检查器名。
func (i *ErrorRateInspector) Name() string { return "error_rate" }

// Inspect 检查会话错误率。
func (i *ErrorRateInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if snap.ErrorRate >= i.blockThresh {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityError,
			Code:          "HIGH_ERROR_RATE",
			Message:       fmt.Sprintf("session error rate %.1f%% exceeds critical threshold %.1f%%",
				snap.ErrorRate*100, i.blockThresh*100),
			Suggestion: "investigate upstream errors; consider session replacement",
			Metadata: map[string]any{
				"error_rate":     snap.ErrorRate,
				"block_threshold": i.blockThresh,
				"request_count":  snap.RequestCount,
			},
			DetectedAt: time.Now(),
		}}, nil
	}
	if snap.ErrorRate >= i.warnThreshold {
		return []*Finding{{
			InspectorName: i.Name(),
			Severity:      SeverityWarning,
			Code:          "ELEVATED_ERROR_RATE",
			Message:       fmt.Sprintf("session error rate %.1f%% exceeds warn threshold %.1f%%",
				snap.ErrorRate*100, i.warnThreshold*100),
			Suggestion: "monitor error patterns",
			Metadata: map[string]any{
				"error_rate":      snap.ErrorRate,
				"warn_threshold":  i.warnThreshold,
				"request_count":   snap.RequestCount,
			},
			DetectedAt: time.Now(),
		}}, nil
	}
	return nil, nil
}

// ModelSwitchInspector 模型频繁切换检查器（NEW）。
//
// 判定规则：
//   - snap.ModelSwitchCount > 5  → SeverityWarning, code=FREQUENT_MODEL_SWITCH
//
// 与 session_health.go 的 ModelSwitchPenalty 联动。
type ModelSwitchInspector struct {
	threshold int
}

// NewModelSwitchInspectorWithConfig 构造检查器。
func NewModelSwitchInspectorWithConfig(_ *Config) *ModelSwitchInspector {
	return &ModelSwitchInspector{threshold: 5}
}

// Name 返回检查器名。
func (i *ModelSwitchInspector) Name() string { return "model_switch" }

// Inspect 检查模型切换频次。
func (i *ModelSwitchInspector) Inspect(snap *SessionSnapshot) ([]*Finding, error) {
	if snap == nil {
		return nil, nil
	}
	if snap.ModelSwitchCount <= i.threshold {
		return nil, nil
	}
	return []*Finding{{
		InspectorName: i.Name(),
		Severity:      SeverityWarning,
		Code:          "FREQUENT_MODEL_SWITCH",
		Message:       fmt.Sprintf("session switched models %d times (threshold %d)", snap.ModelSwitchCount, i.threshold),
		Suggestion:    "consider fixing model selection to reduce context loss",
		Metadata: map[string]any{
			"switch_count": snap.ModelSwitchCount,
			"threshold":    i.threshold,
		},
		DetectedAt: time.Now(),
	}}, nil
}
