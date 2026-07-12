package settings

// CategoryErrorProbe groups all error-triggered active probe settings.
//
// The active probe workflow fires a direct-to-provider HTTP probe when a
// (credential, model) pair accumulates N consecutive failures, and follows
// a configurable backoff chain for retry attempts. Each probe result is
// written to request_logs (task_type='probe_triggered') so it appears in
// the realtime request stream alongside normal business requests.
const CategoryErrorProbe Category = "error_probe"

// ErrorProbeSpecs returns all platform-scoped error-triggered probe specs.
func ErrorProbeSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "error_probe.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     true,
			Description: "是否启用错误触发的主动探测（请求连续失败时立即直连上游探测，不等待周期扫描）",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "error_probe.consecutive_threshold",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     2,
			Min:         floatPtr(2),
			Max:         floatPtr(10),
			Description: "触发主动探测的连续失败次数阈值。值越小越敏感（误触发风险增加），值越大越保守（响应延迟增加）。",
			Unit:        "次",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "error_probe.max_attempts",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     5,
			Min:         floatPtr(1),
			Max:         floatPtr(20),
			Description: "每个 (凭据, 模型) 组合最多探测轮数。超过后停止探测，依赖被动恢复机制 (passive_probe / credential_recovery)。",
			Unit:        "轮",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "error_probe.timeout_ms",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     10000,
			Min:         floatPtr(1000),
			Max:         floatPtr(60000),
			Description: "单次直连探测的 HTTP 请求超时时间",
			Unit:        "毫秒",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
