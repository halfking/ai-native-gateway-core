// domains/hooks/session-inspector/config.go
//
// 会话健康检查 Hook 的运行时配置层。
// 参照 domains/hooks/sessionaudit/config.go 的设计：
//   - 全部配置项在 settings/spec_modules.go 中以 session_inspector.* 注册；
//   - LoadConfig() 在每次 Execute 时调用，天然支持热更新；
//   - 不在 settings.Global 初始化时缓存，避免读到过期值。
package sessioninspector

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Config 会话健康检查 Hook 的完整运行时配置。
//
// 字段顺序与 settings/spec_modules.go 中的 session_inspector.* 保持一一对应，
// 便于审计。零值采用"安全默认"（保守、只记录不阻断）。
type Config struct {
	Enabled bool // session_inspector.enabled

	// A. Token 使用量监控
	TokenMaxTotal      int    // token.max_total
	TokenSoftWarningPct int   // token.soft_warning_pct
	TokenWarnAction     string // token.warn_action: log|metadata|block
	TokenIncludeOutput  bool   // token.include_output
	TokenResetCycle     string // token.reset_cycle: never|hourly|daily|weekly

	// B. 不活跃会话检测与回收
	IdleTimeout             time.Duration // idle.timeout
	IdleAbsoluteMaxLifetime time.Duration // idle.absolute_max_lifetime
	IdleCleanupInterval     time.Duration // idle.cleanup_interval
	IdleCleanupBatchSize    int           // idle.cleanup_batch_size
	IdleRecycleAction       string        // idle.recycle_action: soft_close|notify_only

	// C. 高频请求检测
	RateRPM           int    // rate.rpm_limit
	RateBurstLimit    int    // rate.burst_limit
	RateBurstWindowS  int    // rate.burst_window_seconds
	RateMaxConcurrent int    // rate.max_concurrent
	RateStrategy      string // rate.strategy: fixed|sliding|token_bucket
	RateObserveOnly   bool   // rate.observe_only

	// D. 会话生命周期管理
	LifecycleAutoExtend      bool // lifecycle.auto_extend_on_activity
	LifecycleMaxPerTenant    int  // lifecycle.max_sessions_per_tenant
	LifecycleEvictionPolicy  string // lifecycle.eviction_policy: lru|fifo|none

	// E. 告警与可观测性
	AlertEnabled           bool     // alert.enabled
	AlertNotifyChannels    []string // alert.notify_channels
	AlertWebhookURLs       []string // alert.webhook_urls
	AlertPrometheusEnabled bool     // alert.prometheus_enabled
}

// DefaultConfig 返回开箱即用的默认值（与 spec 中的 Default 一致）。
// 当 settings.Global 未初始化时使用，行为与 LoadConfig() 等价。
func DefaultConfig() *Config {
	return &Config{
		Enabled: true,

		TokenMaxTotal:       100000,
		TokenSoftWarningPct: 80,
		TokenWarnAction:     "log",
		TokenIncludeOutput:  true,
		TokenResetCycle:     "never",

		IdleTimeout:             30 * time.Minute,
		IdleAbsoluteMaxLifetime: 168 * time.Hour, // 7 天
		IdleCleanupInterval:     5 * time.Minute,
		IdleCleanupBatchSize:    500,
		IdleRecycleAction:       "soft_close",

		RateRPM:           60,
		RateBurstLimit:    100,
		RateBurstWindowS:  5,
		RateMaxConcurrent: 4,
		RateStrategy:      "sliding",
		RateObserveOnly:   false,

		LifecycleAutoExtend:     true,
		LifecycleMaxPerTenant:   1000,
		LifecycleEvictionPolicy: "lru",

		AlertEnabled:           true,
		AlertNotifyChannels:    []string{"feishu", "wechat"},
		AlertWebhookURLs:       []string{},
		AlertPrometheusEnabled: true,
	}
}

// LoadConfig 读取平台级 session_inspector.* 配置。
//
// 设计要点：
//   - 每次调用都从 settings.Global 重新读取，保证热更新；
//   - 任何 spec 缺失时使用 DefaultConfig() 中对应字段；
//   - 非法 enum 字符串保留 spec Default 不覆盖（前端已校验）。
func LoadConfig() *Config {
	cfg := DefaultConfig()

	if v := getBool("session_inspector.enabled", cfg.Enabled); v != cfg.Enabled {
		cfg.Enabled = v
	}

	// A. Token
	if v, ok := getInt("session_inspector.token.max_total", cfg.TokenMaxTotal); ok {
		cfg.TokenMaxTotal = v
	}
	if v, ok := getInt("session_inspector.token.soft_warning_pct", cfg.TokenSoftWarningPct); ok {
		cfg.TokenSoftWarningPct = v
	}
	if v := getString("session_inspector.token.warn_action", cfg.TokenWarnAction); v != "" {
		cfg.TokenWarnAction = v
	}
	if v := getBool("session_inspector.token.include_output", cfg.TokenIncludeOutput); v != cfg.TokenIncludeOutput {
		cfg.TokenIncludeOutput = v
	}
	if v := getString("session_inspector.token.reset_cycle", cfg.TokenResetCycle); v != "" {
		cfg.TokenResetCycle = v
	}

	// B. Idle
	if v, ok := getDuration("session_inspector.idle.timeout", cfg.IdleTimeout); ok {
		cfg.IdleTimeout = v
	}
	if v, ok := getDuration("session_inspector.idle.absolute_max_lifetime", cfg.IdleAbsoluteMaxLifetime); ok {
		cfg.IdleAbsoluteMaxLifetime = v
	}
	if v, ok := getDuration("session_inspector.idle.cleanup_interval", cfg.IdleCleanupInterval); ok {
		cfg.IdleCleanupInterval = v
	}
	if v, ok := getInt("session_inspector.idle.cleanup_batch_size", cfg.IdleCleanupBatchSize); ok {
		cfg.IdleCleanupBatchSize = v
	}
	if v := getString("session_inspector.idle.recycle_action", cfg.IdleRecycleAction); v != "" {
		cfg.IdleRecycleAction = v
	}

	// C. Rate
	if v, ok := getInt("session_inspector.rate.rpm_limit", cfg.RateRPM); ok {
		cfg.RateRPM = v
	}
	if v, ok := getInt("session_inspector.rate.burst_limit", cfg.RateBurstLimit); ok {
		cfg.RateBurstLimit = v
	}
	if v, ok := getInt("session_inspector.rate.burst_window_seconds", cfg.RateBurstWindowS); ok {
		cfg.RateBurstWindowS = v
	}
	if v, ok := getInt("session_inspector.rate.max_concurrent", cfg.RateMaxConcurrent); ok {
		cfg.RateMaxConcurrent = v
	}
	if v := getString("session_inspector.rate.strategy", cfg.RateStrategy); v != "" {
		cfg.RateStrategy = v
	}
	if v := getBool("session_inspector.rate.observe_only", cfg.RateObserveOnly); v != cfg.RateObserveOnly {
		cfg.RateObserveOnly = v
	}

	// D. Lifecycle
	if v := getBool("session_inspector.lifecycle.auto_extend_on_activity", cfg.LifecycleAutoExtend); v != cfg.LifecycleAutoExtend {
		cfg.LifecycleAutoExtend = v
	}
	if v, ok := getInt("session_inspector.lifecycle.max_sessions_per_tenant", cfg.LifecycleMaxPerTenant); ok {
		cfg.LifecycleMaxPerTenant = v
	}
	if v := getString("session_inspector.lifecycle.eviction_policy", cfg.LifecycleEvictionPolicy); v != "" {
		cfg.LifecycleEvictionPolicy = v
	}

	// E. Alert
	if v := getBool("session_inspector.alert.enabled", cfg.AlertEnabled); v != cfg.AlertEnabled {
		cfg.AlertEnabled = v
	}
	if v := getStringArray("session_inspector.alert.notify_channels", cfg.AlertNotifyChannels); v != nil {
		cfg.AlertNotifyChannels = v
	}
	if v := getStringArray("session_inspector.alert.webhook_urls", cfg.AlertWebhookURLs); v != nil {
		cfg.AlertWebhookURLs = v
	}
	if v := getBool("session_inspector.alert.prometheus_enabled", cfg.AlertPrometheusEnabled); v != cfg.AlertPrometheusEnabled {
		cfg.AlertPrometheusEnabled = v
	}

	return cfg
}

// SoftWarningThreshold 返回 token 软警告的绝对阈值（tokens）。
// 计算方式：max_total * soft_warning_pct / 100。
// 当 soft_warning_pct <= 0 时返回 max_total，等价于"关闭软警告"。
func (c *Config) SoftWarningThreshold() int {
	if c.TokenSoftWarningPct <= 0 {
		return c.TokenMaxTotal
	}
	return c.TokenMaxTotal * c.TokenSoftWarningPct / 100
}

// IsBlockAction 返回 token 警告动作是否为阻断。
// 用于 Inspector 决定是否设置 env.StatusCode = 429。
func (c *Config) IsBlockAction() bool {
	return c.TokenWarnAction == "block"
}

// IsObserveOnly 高频请求检测是否只观察不阻断。
func (c *Config) IsObserveOnly() bool {
	return c.RateObserveOnly
}

// ShouldAlertOnSeverity 判定某 severity 是否触发告警。
// info: 不告警；warning/critical: 告警。
func (c *Config) ShouldAlertOnSeverity(sev Severity) bool {
	if !c.AlertEnabled {
		return false
	}
	return sev == SeverityWarning || sev == SeverityCritical || sev == SeverityError
}

// ── settings 读取 helpers（与 sessionaudit/config.go 保持同款签名） ──

func getBool(key string, fallback bool) bool {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getString(key string, fallback string) string {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getInt(key string, fallback int) (int, bool) {
	if settings.Global == nil {
		return fallback, false
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback, false
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	// 兼容 string-as-int (TypeString 类型如 duration 存 "30m")
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n2, err := strconv.Atoi(s); err == nil {
			return n2, true
		}
	}
	return fallback, false
}

func getFloat(key string, fallback float64) float64 {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getStringArray(key string, fallback []string) []string {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	// 先尝试解析为 JSON 数组
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	// 兜底：可能存的是 JSON-encoded 字符串
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
		return []string{s}
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, bool) {
	if settings.Global == nil {
		return fallback, false
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback, false
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fallback, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback, false
	}
	return d, true
}
