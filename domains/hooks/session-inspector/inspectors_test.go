package sessioninspector

import (
	"testing"
	"time"
)

// ---------- TokenLimitInspector (config-driven) ----------

func TestTokenLimitInspector_ConfigSoftWarning(t *testing.T) {
	cfg := &Config{
		TokenMaxTotal:      1000,
		TokenSoftWarningPct: 80,
	}
	i := NewTokenLimitInspectorWithConfig(cfg)
	snap := &SessionSnapshot{TokenCount: 850} // 850 > 800 (80%)
	findings, err := i.Inspect(snap)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1", len(findings))
	}
	if findings[0].Code != "TOKEN_SOFT_WARNING" {
		t.Errorf("code=%q, want TOKEN_SOFT_WARNING", findings[0].Code)
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("severity=%q, want warning", findings[0].Severity)
	}
}

func TestTokenLimitInspector_ConfigHardLimit(t *testing.T) {
	cfg := &Config{
		TokenMaxTotal:       1000,
		TokenSoftWarningPct: 80,
		TokenWarnAction:     "block",
	}
	i := NewTokenLimitInspectorWithConfig(cfg)
	if !i.IsBlockAction() {
		t.Error("IsBlockAction should be true when warn_action=block")
	}
	snap := &SessionSnapshot{TokenCount: 1500}
	findings, _ := i.Inspect(snap)
	if findings[0].Code != "TOKEN_LIMIT_EXCEEDED" {
		t.Errorf("code=%q", findings[0].Code)
	}
	if findings[0].Severity != SeverityCritical {
		t.Errorf("severity=%q, want critical", findings[0].Severity)
	}
}

func TestTokenLimitInspector_ConfigDisabled(t *testing.T) {
	cfg := &Config{TokenMaxTotal: 0} // 禁用
	i := NewTokenLimitInspectorWithConfig(cfg)
	snap := &SessionSnapshot{TokenCount: 999999}
	findings, _ := i.Inspect(snap)
	if findings != nil {
		t.Error("max_total=0 should disable inspector")
	}
}

// ---------- InactiveInspector (config-driven) ----------

func TestInactiveInspector_ConfigAbsoluteMaxLifetime(t *testing.T) {
	cfg := &Config{
		IdleTimeout:             30 * time.Minute,
		IdleAbsoluteMaxLifetime: 1 * time.Hour, // 1h 上限
	}
	i := NewInactiveInspectorWithConfig(cfg)
	snap := &SessionSnapshot{
		StartedAt:     time.Now().Add(-2 * time.Hour), // 2h 前
		LastActiveAt:  time.Now(),                       // 刚活跃（不会触发 idle）
	}
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1 (absolute expiry)", len(findings))
	}
	if findings[0].Code != "SESSION_EXPIRED" {
		t.Errorf("code=%q, want SESSION_EXPIRED", findings[0].Code)
	}
	if findings[0].Severity != SeverityError {
		t.Errorf("severity=%q, want error", findings[0].Severity)
	}
}

// ---------- HighFrequencyInspector (config-driven) ----------

func TestHighFrequencyInspector_ConfigBurstExceeded(t *testing.T) {
	cfg := &Config{
		RateRPM:          60,
		RateBurstLimit:   100,
		RateBurstWindowS: 5,
		RateObserveOnly:  false,
	}
	i := NewHighFrequencyInspectorWithConfig(cfg)
	snap := &SessionSnapshot{
		RequestCount: 50,  // 未超 RPM
		BurstCount:   150, // 超过 burst
	}
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1 (burst)", len(findings))
	}
	if findings[0].Code != "BURST_EXCEEDED" {
		t.Errorf("code=%q, want BURST_EXCEEDED", findings[0].Code)
	}
	if findings[0].Severity != SeverityCritical {
		t.Errorf("severity=%q, want critical", findings[0].Severity)
	}
}

func TestHighFrequencyInspector_ConfigConcurrent(t *testing.T) {
	cfg := &Config{
		RateRPM:          60,
		RateMaxConcurrent: 4,
	}
	i := NewHighFrequencyInspectorWithConfig(cfg)
	snap := &SessionSnapshot{
		RequestCount:    10, // 正常
		ConcurrentCount: 8,  // 超 4
	}
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1 (concurrent)", len(findings))
	}
	if findings[0].Code != "CONCURRENT_EXCEEDED" {
		t.Errorf("code=%q", findings[0].Code)
	}
}

func TestHighFrequencyInspector_ConfigObserveOnly(t *testing.T) {
	cfg := &Config{
		RateRPM:         10,
		RateObserveOnly: true,
	}
	i := NewHighFrequencyInspectorWithConfig(cfg)
	snap := &SessionSnapshot{RequestCount: 100} // 远超标
	findings, _ := i.Inspect(snap)
	if findings[0].Severity != SeverityWarning {
		t.Errorf("observe_only should demote to warning, got %q", findings[0].Severity)
	}
}

// ---------- SessionLifecycleInspector (NEW) ----------

func TestSessionLifecycleInspector_Exceeds(t *testing.T) {
	cfg := &Config{
		LifecycleMaxPerTenant:   100,
		LifecycleEvictionPolicy: "lru",
	}
	i := NewSessionLifecycleInspectorWithConfig(cfg)
	snap := &SessionSnapshot{TenantActiveCount: 150}
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1", len(findings))
	}
	if findings[0].Code != "TENANT_SESSION_LIMIT" {
		t.Errorf("code=%q, want TENANT_SESSION_LIMIT", findings[0].Code)
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("severity=%q", findings[0].Severity)
	}
	// metadata 应包含 eviction policy
	if findings[0].Metadata["eviction"] != "lru" {
		t.Errorf("metadata.eviction = %v, want lru", findings[0].Metadata["eviction"])
	}
}

func TestSessionLifecycleInspector_WithinLimit(t *testing.T) {
	cfg := &Config{LifecycleMaxPerTenant: 1000}
	i := NewSessionLifecycleInspectorWithConfig(cfg)
	snap := &SessionSnapshot{TenantActiveCount: 50}
	findings, _ := i.Inspect(snap)
	if findings != nil {
		t.Errorf("expected nil, got %+v", findings)
	}
}

func TestSessionLifecycleInspector_Disabled(t *testing.T) {
	cfg := &Config{LifecycleMaxPerTenant: 0} // 禁用
	i := NewSessionLifecycleInspectorWithConfig(cfg)
	snap := &SessionSnapshot{TenantActiveCount: 9999}
	findings, _ := i.Inspect(snap)
	if findings != nil {
		t.Error("max=0 should disable inspector")
	}
}

// ---------- ErrorRateInspector (NEW) ----------

func TestErrorRateInspector_Critical(t *testing.T) {
	i := NewErrorRateInspectorWithConfig(nil)
	snap := &SessionSnapshot{RequestCount: 100, ErrorRate: 0.6} // 60%
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d", len(findings))
	}
	if findings[0].Code != "HIGH_ERROR_RATE" {
		t.Errorf("code=%q", findings[0].Code)
	}
	if findings[0].Severity != SeverityError {
		t.Errorf("severity=%q", findings[0].Severity)
	}
}

func TestErrorRateInspector_Warning(t *testing.T) {
	i := NewErrorRateInspectorWithConfig(nil)
	snap := &SessionSnapshot{RequestCount: 100, ErrorRate: 0.3} // 30%
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d", len(findings))
	}
	if findings[0].Code != "ELEVATED_ERROR_RATE" {
		t.Errorf("code=%q, want ELEVATED_ERROR_RATE", findings[0].Code)
	}
}

func TestErrorRateInspector_Healthy(t *testing.T) {
	i := NewErrorRateInspectorWithConfig(nil)
	snap := &SessionSnapshot{RequestCount: 100, ErrorRate: 0.05}
	findings, _ := i.Inspect(snap)
	if findings != nil {
		t.Errorf("expected nil for healthy session, got %+v", findings)
	}
}

// ---------- ModelSwitchInspector (NEW) ----------

func TestModelSwitchInspector_Exceeds(t *testing.T) {
	i := NewModelSwitchInspectorWithConfig(nil)
	snap := &SessionSnapshot{ModelSwitchCount: 10}
	findings, _ := i.Inspect(snap)
	if len(findings) != 1 {
		t.Fatalf("findings=%d", len(findings))
	}
	if findings[0].Code != "FREQUENT_MODEL_SWITCH" {
		t.Errorf("code=%q", findings[0].Code)
	}
}

func TestModelSwitchInspector_Normal(t *testing.T) {
	i := NewModelSwitchInspectorWithConfig(nil)
	snap := &SessionSnapshot{ModelSwitchCount: 3}
	findings, _ := i.Inspect(snap)
	if findings != nil {
		t.Errorf("expected nil, got %+v", findings)
	}
}

// ---------- BuildInspectorsFromConfig ----------

func TestBuildInspectorsFromConfig(t *testing.T) {
	cfg := DefaultConfig()
	inspectors := BuildInspectorsFromConfig(cfg)
	if len(inspectors) != 6 {
		t.Fatalf("expected 6 inspectors, got %d", len(inspectors))
	}
	expectedNames := map[string]bool{
		"token_limit":      false,
		"inactive":         false,
		"high_frequency":   false,
		"session_lifecycle": false,
		"error_rate":       false,
		"model_switch":     false,
	}
	for _, ins := range inspectors {
		if _, ok := expectedNames[ins.Name()]; !ok {
			t.Errorf("unexpected inspector: %s", ins.Name())
		}
		expectedNames[ins.Name()] = true
	}
	for name, seen := range expectedNames {
		if !seen {
			t.Errorf("missing inspector: %s", name)
		}
	}
}

func TestBuildInspectorsFromConfig_NilConfig(t *testing.T) {
	inspectors := BuildInspectorsFromConfig(nil)
	if len(inspectors) != 6 {
		t.Errorf("expected 6 inspectors for nil config, got %d", len(inspectors))
	}
}
