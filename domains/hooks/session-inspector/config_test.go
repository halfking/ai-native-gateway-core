package sessioninspector

import (
	"testing"
	"time"
)

// TestDefaultConfig 验证 DefaultConfig 返回的零值符合 spec 默认值。
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("default Enabled should be true")
	}
	if cfg.TokenMaxTotal != 100000 {
		t.Errorf("default TokenMaxTotal = %d, want 100000", cfg.TokenMaxTotal)
	}
	if cfg.IdleTimeout != 30*time.Minute {
		t.Errorf("default IdleTimeout = %s, want 30m", cfg.IdleTimeout)
	}
	if cfg.IdleRecycleAction != "soft_close" {
		t.Errorf("default IdleRecycleAction = %s, want soft_close", cfg.IdleRecycleAction)
	}
	if cfg.RateRPM != 60 {
		t.Errorf("default RateRPM = %d, want 60", cfg.RateRPM)
	}
	if cfg.RateStrategy != "sliding" {
		t.Errorf("default RateStrategy = %s, want sliding", cfg.RateStrategy)
	}
	if cfg.LifecycleEvictionPolicy != "lru" {
		t.Errorf("default LifecycleEvictionPolicy = %s, want lru", cfg.LifecycleEvictionPolicy)
	}
	if len(cfg.AlertNotifyChannels) != 2 {
		t.Errorf("default AlertNotifyChannels len = %d, want 2", len(cfg.AlertNotifyChannels))
	}
}

// TestSoftWarningThreshold 测试软警告阈值的计算逻辑。
func TestSoftWarningThreshold(t *testing.T) {
	cfg := &Config{TokenMaxTotal: 1000, TokenSoftWarningPct: 80}
	if got := cfg.SoftWarningThreshold(); got != 800 {
		t.Errorf("SoftWarningThreshold = %d, want 800", got)
	}

	// 边界：pct=0 → 等于 max（关闭软警告）
	cfg.TokenSoftWarningPct = 0
	if got := cfg.SoftWarningThreshold(); got != 1000 {
		t.Errorf("SoftWarningThreshold at pct=0 = %d, want 1000", got)
	}

	// 边界：pct=100 → 等于 max
	cfg.TokenSoftWarningPct = 100
	if got := cfg.SoftWarningThreshold(); got != 1000 {
		t.Errorf("SoftWarningThreshold at pct=100 = %d, want 1000", got)
	}
}

// TestIsBlockAction 测试 token 警告动作判定。
func TestIsBlockAction(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{"log", false},
		{"metadata", false},
		{"block", true},
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		cfg := &Config{TokenWarnAction: tc.action}
		if got := cfg.IsBlockAction(); got != tc.want {
			t.Errorf("IsBlockAction(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
}

// TestIsObserveOnly 测试高频请求 observe_only 判定。
func TestIsObserveOnly(t *testing.T) {
	cfg := &Config{RateObserveOnly: true}
	if !cfg.IsObserveOnly() {
		t.Error("expected observe_only=true")
	}
	cfg.RateObserveOnly = false
	if cfg.IsObserveOnly() {
		t.Error("expected observe_only=false")
	}
}

// TestShouldAlertOnSeverity 测试告警判定。
func TestShouldAlertOnSeverity(t *testing.T) {
	cfg := &Config{AlertEnabled: true}
	if cfg.ShouldAlertOnSeverity(SeverityInfo) {
		t.Error("info should not trigger alert")
	}
	if !cfg.ShouldAlertOnSeverity(SeverityWarning) {
		t.Error("warning should trigger alert")
	}
	if !cfg.ShouldAlertOnSeverity(SeverityError) {
		t.Error("error should trigger alert")
	}
	if !cfg.ShouldAlertOnSeverity(SeverityCritical) {
		t.Error("critical should trigger alert")
	}

	// 禁用告警时全部 false
	cfg.AlertEnabled = false
	if cfg.ShouldAlertOnSeverity(SeverityCritical) {
		t.Error("alert disabled should not trigger")
	}
}

// TestLoadConfigFallback 测试 settings.Global 不可用时 LoadConfig 返回默认配置。
func TestLoadConfigFallback(t *testing.T) {
	cfg := LoadConfig()
	if cfg == nil {
		t.Fatal("LoadConfig returned nil")
	}
	// 在没有 settings.Global 初始化时，应当等同于 DefaultConfig
	def := DefaultConfig()
	if cfg.TokenMaxTotal != def.TokenMaxTotal {
		t.Errorf("TokenMaxTotal = %d, want default %d", cfg.TokenMaxTotal, def.TokenMaxTotal)
	}
	if cfg.IdleTimeout != def.IdleTimeout {
		t.Errorf("IdleTimeout = %s, want default %s", cfg.IdleTimeout, def.IdleTimeout)
	}
	if cfg.RateStrategy != def.RateStrategy {
		t.Errorf("RateStrategy = %s, want default %s", cfg.RateStrategy, def.RateStrategy)
	}
}
