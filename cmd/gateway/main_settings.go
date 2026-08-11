// Settings sync helpers used by main() at startup.
//
// Extracted from main.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// All declarations here are package-private; behaviour is unchanged
// from the original implementation in main.go.
package main

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// syncRateLimitGateFromSettings reads the current rate_limit.enabled setting
// from settings.Global and applies it to ratelimit's atomic.Bool cache. The
// cache is checked on every request hot-path (Limiter / FpSlot / RPM /
// Executor sticky override) so we never hit the settings backend during a
// request.
//
// Called once at startup. Runtime changes go through the admin /api/settings
// endpoint which invalidates the registry; the registry's onChange hook
// should also call ratelimit.SetRateLimitEnabled(v) (see
// admin/modules.go:236 / settings/spec_modules.go:793).
//
// Errors are intentionally non-fatal: if the registry / spec is missing,
// we keep the default (enabled=true) which matches settings/spec_modules.go.
func syncRateLimitGateFromSettings() {
	if settings.Global == nil {
		return
	}
	sp := settings.Global.Spec(ratelimit.RateLimitGateKey)
	if sp == nil {
		// Spec not yet registered — keep the package default (enabled).
		return
	}
	v, _, err := settings.Global.EffectiveValue(sp.Scope, ratelimit.RateLimitGateKey, "")
	if err != nil || len(v) == 0 {
		return
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		slog.Debug("rate_limit.enabled: failed to unmarshal", "raw", string(v))
		return
	}
	ratelimit.SetRateLimitEnabled(b)
	slog.Info("rate_limit.enabled initialised", "enabled", b)
}

// syncDispatchGateFromSettings reads dispatch_v2.enabled from settings.Global
// and applies it to dispatch's atomic.Bool cache (domains/dispatch/gate.go).
// The cache is read on every Execute hot-path call so we never hit the
// settings backend during a request. Mirrors syncRateLimitGateFromSettings.
// Runtime changes go through the admin settings PUT handler which calls
// dispatch.SetDispatchEnabled directly. Errors are non-fatal (keep default ON).
func syncDispatchGateFromSettings() {
	if settings.Global == nil {
		return
	}
	sp := settings.Global.Spec(dispatch.DispatchGateKey)
	if sp == nil {
		return // Spec not registered — keep package default (enabled).
	}
	v, _, err := settings.Global.EffectiveValue(sp.Scope, dispatch.DispatchGateKey, "")
	if err != nil || len(v) == 0 {
		return
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		slog.Debug("dispatch_v2.enabled: failed to unmarshal", "raw", string(v))
		return
	}
	dispatch.SetDispatchEnabled(b)
	slog.Info("dispatch_v2.enabled initialised", "enabled", b)
}

// applyLogSettingsToLogging 从 settings_kv 读取 log.* 配置并应用到已初始化的
// lumberjack writer（热加载）。在 settings registry 初始化后调用一次，使 DB 中
// 持久化的日志轮转参数在启动时即生效。文件路径（log.file）不在热加载范围。
//
// 静默失败：任何读取/解析错误只记 warning，保留 env/YAML 默认值，不阻塞启动。
func applyLogSettingsToLogging() {
	cur := logging.ActiveConfig()
	if cur.File == "" {
		return // 文件日志未启用，无需同步
	}
	updated := false

	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_size_mb", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n > 0 {
			cur.MaxSizeMB = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_backups", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n >= 0 {
			cur.MaxBackups = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.max_age_days", ""); err == nil && src == "db" {
		if n := parseIntSetting(v); n >= 0 {
			cur.MaxAgeDays = n
			updated = true
		}
	}
	if v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, "log.compress", ""); err == nil && src == "db" {
		if b, ok := parseBoolSetting(v); ok {
			cur.Compress = b
			updated = true
		}
	}

	if updated {
		if err := logging.Reconfigure(cur); err != nil {
			slog.Warn("settings: apply log.* to logging failed", "error", err)
		} else {
			slog.Info("settings: log.* applied from DB (hot reload)",
				"max_size_mb", cur.MaxSizeMB,
				"max_backups", cur.MaxBackups,
				"max_age_days", cur.MaxAgeDays,
				"compress", cur.Compress)
		}
	}
}

// readIntSettingPublic 从 settings.Global 读 int 值（worker 配置用）。
func readIntSettingPublic(key string) (int, string) {
	if settings.Global == nil {
		return 0, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return 0, ""
	}
	s := strings.Trim(string(v), `"`)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ""
	}
	return n, src
}

// readBoolSettingPublic 从 settings.Global 读 bool 值（worker 配置用）。
func readBoolSettingPublic(key string) (bool, string) {
	if settings.Global == nil {
		return false, ""
	}
	v, src, err := settings.Global.EffectiveValue(settings.ScopePlatform, key, "")
	if err != nil || len(v) == 0 {
		return false, ""
	}
	s := strings.Trim(string(v), `"`)
	return s == "true" || s == "1", src
}

// parseIntSetting 解析 settings_kv 返回的 JSON 值（可能是 "100" 或 100）。
func parseIntSetting(raw json.RawMessage) int {
	s := strings.Trim(string(raw), `"`)
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return -1
}

// parseBoolSetting 解析 settings_kv 返回的 JSON bool 值。
func parseBoolSetting(raw json.RawMessage) (bool, bool) {
	s := strings.Trim(string(raw), `"`)
	switch strings.ToLower(s) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// readBoolSettingValue 读取平台级 bool 设置项（忽略错误，缺省 false）。
func readBoolSettingValue(key string) bool {
	sp := settings.Global.Spec(key)
	if sp == nil {
		return false
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return false
	}
	s := strings.Trim(string(raw), `"`)
	return s == "true" || s == "1"
}

// newStorageRetentionConfigProvider returns a closure that reads storage.*
// settings from settings.Global and constructs a bg.StorageRetentionConfig on
// every call (so live-config changes in the admin UI take effect on the next
// retention tick instead of requiring a restart).
//
// Defaults (matching the original inline closure in main()):
//
//	auto_cleanup_enabled   → false (only enabled when explicitly set)
//	auto_cleanup_threshold → 85%
//	disk_quota_percent     → 80%
//
// Behaviour is identical to the inline closure that previously lived in main();
// only the dependency on closure-captured locals was eliminated by parameterising
// the read helpers (already top-level functions).
func newStorageRetentionConfigProvider() func() bg.StorageRetentionConfig {
	return func() bg.StorageRetentionConfig {
		var c bg.StorageRetentionConfig
		if b, _ := readBoolSettingPublic("storage.auto_cleanup_enabled"); b {
			c.AutoCleanupEnabled = true
		}
		if v, _ := readIntSettingPublic("storage.auto_cleanup_threshold"); v > 0 {
			c.AutoCleanupThreshold = float64(v)
		} else {
			c.AutoCleanupThreshold = 85
		}
		if v, _ := readIntSettingPublic("storage.disk_quota_percent"); v > 0 {
			c.DiskQuotaPercent = float64(v)
		} else {
			c.DiskQuotaPercent = 80
		}
		return c
	}
}
