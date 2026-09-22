package settings

import (
	"strings"
)

// CategoryErrorProbe groups all error-triggered active probe settings.
//
// The active probe workflow fires a direct-to-provider HTTP probe when a
// (credential, model) pair accumulates N consecutive failures, and follows
// a configurable backoff chain for retry attempts. Each probe result is
// written to request_logs (task_type='probe_triggered') so it appears in
// the realtime request stream alongside normal business requests.
const CategoryErrorProbe Category = "error_probe"

// Error-triggered active-probe keys. Registered in an earlier wave but left
// without consumers (UI-editable no-ops); Wave 3 B5 wired them at the same
// capture point as error_probe.workers (Wave 2): cmd/gateway/main.go reads
// the accessors below once at construction — worker pool, executor HTTP
// client and manager threshold are all fixed at Start, so these specs are
// HotReload=false by design (same rationale as KeyErrorProbeWorkers).
const (
	KeyErrorProbeEnabled              = "error_probe.enabled"
	KeyErrorProbeConsecutiveThreshold = "error_probe.consecutive_threshold"
	KeyErrorProbeMaxAttempts          = "error_probe.max_attempts"
	KeyErrorProbeTimeoutMs            = "error_probe.timeout_ms"
)

// ErrorProbeSpecs returns all platform-scoped error-triggered probe specs.
func ErrorProbeSpecs() []*Spec {
	return []*Spec{
		{
			Key:         KeyErrorProbeEnabled,
			EnvName:     EnvNameAuto(KeyErrorProbeEnabled),
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     true,
			Description: "是否启用错误触发的主动探测（请求连续失败时立即直连上游探测，不等待周期扫描）。启动时读取，改动重启生效。",
			DangerLevel: Safe,
			HotReload:   false, // 2026-09-22 B5 接线：构造期固定，同 error_probe.workers
		},
		{
			Key:         KeyErrorProbeConsecutiveThreshold,
			EnvName:     EnvNameAuto(KeyErrorProbeConsecutiveThreshold),
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     2,
			Min:         floatPtr(2),
			Max:         floatPtr(10),
			Description: "触发主动探测的连续失败次数阈值。值越小越敏感（误触发风险增加），值越大越保守（响应延迟增加）。启动时读取，改动重启生效。",
			Unit:        "次",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			Key:         KeyErrorProbeMaxAttempts,
			EnvName:     EnvNameAuto(KeyErrorProbeMaxAttempts),
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryErrorProbe,
			Default:     5,
			Min:         floatPtr(1),
			Max:         floatPtr(20),
			Description: "每个 (凭据, 模型) 组合最多探测轮数。超过后停止探测，依赖被动恢复机制 (passive_probe / credential_recovery)。启动时读取，改动重启生效。",
			Unit:        "轮",
			DangerLevel: Safe,
			HotReload:   false,
		},
		{
			Key:         KeyErrorProbeTimeoutMs,
			EnvName:     EnvNameAuto(KeyErrorProbeTimeoutMs),
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryRetry, // B5③ 聚合组（原 error_probe）
			Default:     30000,
			Min:         floatPtr(1000),
			Max:         floatPtr(60000),
			Description: "单次直连探测的 HTTP 请求超时时间。启动时读取，改动重启生效。",
			Unit:        "毫秒",
			DangerLevel: Safe,
			HotReload:   false,
		},
	}
}

// ErrorProbeEnabled reports whether the error-triggered active probe runs.
// The env idiom accepts "0" alongside JSON "false" (deployments use
// LLM_GATEWAY_ERROR_PROBE_ENABLED=0), which the registry's bool decode
// would silently drop to the default=true — hence the explicit env-source
// branch. Priority is still DB row > env > default.
func ErrorProbeEnabled() bool {
	if raw, source, err := Global.EffectiveValue(ScopePlatform, KeyErrorProbeEnabled, ""); err == nil && source == "env" {
		s := strings.TrimSpace(string(raw))
		return s != "0" && !strings.EqualFold(s, "false")
	}
	return GetPlatformBool(KeyErrorProbeEnabled, true)
}

// ErrorProbeConsecutiveThreshold is the consecutive-failure count that arms
// the active probe. Startup-only read.
func ErrorProbeConsecutiveThreshold() int {
	return clampThresholdInt(GetPlatformInt(KeyErrorProbeConsecutiveThreshold, 2), 2, 10)
}

// ErrorProbeMaxAttempts caps the per-(credential, model) probe rounds.
// Startup-only read.
func ErrorProbeMaxAttempts() int {
	return clampThresholdInt(GetPlatformInt(KeyErrorProbeMaxAttempts, 5), 1, 20)
}

// ErrorProbeTimeoutMs is the direct-probe HTTP timeout in milliseconds.
// Startup-only read. Default 30000 mirrors ActiveProbeExecutor's legacy
// default — the previously registered 10000 never took effect anywhere
// (key unconsumed) and was aligned to runtime reality in the Wave 3 B5
// adjudication rather than silently changing probe behavior.
func ErrorProbeTimeoutMs() int {
	return clampThresholdInt(GetPlatformInt(KeyErrorProbeTimeoutMs, 30000), 1000, 60000)
}
