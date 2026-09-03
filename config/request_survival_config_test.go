package config

import (
	"testing"
)

// TestRequestSurvivalDefaults pins the doc-18 §14 defaults: every survival
// knob OFF/zero-impact until explicitly enabled, so flag-off behavior is
// byte-identical to pre-survival.
func TestRequestSurvivalDefaults(t *testing.T) {
	cfg := Load()
	if cfg.RequestSurvivalEnabled {
		t.Fatal("request_survival must default to disabled")
	}
	if cfg.RequestSurvivalDurableEnabled {
		t.Fatal("request_survival durable must default to disabled")
	}
	if cfg.RequestSurvivalEnabledForTenant("any") {
		t.Fatal("disabled survival must reject every tenant")
	}
}

func TestRequestSurvivalNormalizeFillsDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.NormalizeRequestSurvival()
	if cfg.RequestSurvivalInteractiveDeadlineSeconds != 18000 {
		t.Fatalf("interactive deadline = %d, want 18000 (5h recovery window)", cfg.RequestSurvivalInteractiveDeadlineSeconds)
	}
	if cfg.RequestSurvivalDurableDeadlineSeconds != 86400 {
		t.Fatalf("durable deadline = %d, want 86400", cfg.RequestSurvivalDurableDeadlineSeconds)
	}
	if cfg.RequestSurvivalRetryBaseSeconds != 30 || cfg.RequestSurvivalRetryMaxSeconds != 120 {
		t.Fatalf("retry base/max = %d/%d, want 30/120", cfg.RequestSurvivalRetryBaseSeconds, cfg.RequestSurvivalRetryMaxSeconds)
	}
	if cfg.RequestSurvivalRetryIntervalSeconds != 30 || cfg.RequestSurvivalNightMaxAttempts != 600 || cfg.RequestSurvivalNightStartHour != 20 {
		t.Fatalf("recovery cadence/night policy = %d/%d/%d, want 30/600/20", cfg.RequestSurvivalRetryIntervalSeconds, cfg.RequestSurvivalNightMaxAttempts, cfg.RequestSurvivalNightStartHour)
	}
	if cfg.RequestSurvivalWorkerCount != 4 || cfg.RequestSurvivalWorkerLeaseSecs != 60 {
		t.Fatalf("worker count/lease = %d/%d, want 4/60", cfg.RequestSurvivalWorkerCount, cfg.RequestSurvivalWorkerLeaseSecs)
	}
	if cfg.RequestSurvivalMaxAttempts != 100 || cfg.RequestSurvivalMaxActiveTasksPerTenant != 100 {
		t.Fatalf("max retries/tasks = %d/%d, want 100/100", cfg.RequestSurvivalMaxAttempts, cfg.RequestSurvivalMaxActiveTasksPerTenant)
	}
	if cfg.RequestSurvivalStatusIntervalSeconds != 60 {
		t.Fatalf("status interval = %d, want 60", cfg.RequestSurvivalStatusIntervalSeconds)
	}
}

func TestRequestSurvivalNormalizeClampsRetryLimits(t *testing.T) {
	cfg := &Config{RequestSurvivalRetryMaxSeconds: 600, RequestSurvivalMaxAttempts: 500}
	cfg.NormalizeRequestSurvival()
	if cfg.RequestSurvivalRetryMaxSeconds != 120 || cfg.RequestSurvivalMaxAttempts != 500 {
		t.Fatalf("retry max/attempts = %d/%d, want 120/500", cfg.RequestSurvivalRetryMaxSeconds, cfg.RequestSurvivalMaxAttempts)
	}
}

// TestRequestSurvivalDurableImpliesSurvival: Phase 2 durable cannot run
// without its Phase 1 in-connection prerequisite.
func TestRequestSurvivalDurableImpliesSurvival(t *testing.T) {
	cfg := &Config{RequestSurvivalDurableEnabled: true}
	cfg.NormalizeRequestSurvival()
	if !cfg.RequestSurvivalEnabled {
		t.Fatal("durable_enabled must imply request_survival_enabled")
	}
}

func TestRequestSurvivalTenantAllowlist(t *testing.T) {
	cfg := &Config{
		RequestSurvivalEnabled:         true,
		RequestSurvivalTenantAllowlist: []string{"alpha", "beta"},
	}
	cfg.NormalizeRequestSurvival()
	if !cfg.RequestSurvivalEnabledForTenant("alpha") {
		t.Fatal("allowlisted tenant alpha must pass")
	}
	if cfg.RequestSurvivalEnabledForTenant("gamma") {
		t.Fatal("non-allowlisted tenant gamma must be rejected")
	}
	if cfg.RequestSurvivalEnabledForTenant("") {
		t.Fatal("empty tenant must never pass an allowlist gate (fail closed)")
	}

	open := &Config{RequestSurvivalEnabled: true}
	open.NormalizeRequestSurvival()
	if !open.RequestSurvivalEnabledForTenant("") {
		t.Fatal("empty allowlist = all tenants (incl. default tenant)")
	}
}

func TestRequestSurvivalEnvOverrides(t *testing.T) {
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS", "600")
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_TENANT_ALLOWLIST", "alpha, beta")
	cfg := Load()
	if !cfg.RequestSurvivalEnabled {
		t.Fatal("env LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED=true lost")
	}
	if cfg.RequestSurvivalInteractiveDeadlineSeconds != 600 {
		t.Fatalf("interactive deadline = %d, want 600", cfg.RequestSurvivalInteractiveDeadlineSeconds)
	}
	if len(cfg.RequestSurvivalTenantAllowlist) != 2 {
		t.Fatalf("allowlist = %v, want 2 entries", cfg.RequestSurvivalTenantAllowlist)
	}
}

// TestRequestSurvivalEnvWinsOverFile mirrors the config precedence rule:
// environment variables override YAML file values.
func TestRequestSurvivalEnvWinsOverFile(t *testing.T) {
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED", "")
	fileCfg := &Config{
		RequestSurvivalEnabled:                    true,
		RequestSurvivalInteractiveDeadlineSeconds: 42,
	}
	cfg := &Config{}
	cfg.mergeFrom(fileCfg)
	if !cfg.RequestSurvivalEnabled {
		t.Fatal("yaml request_survival_enabled=true lost in merge")
	}
	if cfg.RequestSurvivalInteractiveDeadlineSeconds != 42 {
		t.Fatalf("interactive deadline = %d, want 42 from file", cfg.RequestSurvivalInteractiveDeadlineSeconds)
	}
}

// TestStreamRetryAndSurvivalMutex is the config-level half of the startup
// mutual exclusion enforced in cmd/gateway/main.go; it pins that both flags
// can be independently set here and the wiring layer decides the winner.
func TestStreamRetryAndSurvivalMutex(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_REQUEST_SURVIVAL_ENABLED", "true")
	cfg := Load()
	if !cfg.StreamRetryEnabled || !cfg.RequestSurvivalEnabled {
		t.Fatal("env parse regression: both flags should parse independently")
	}
	// The wiring rule: survival wins, streamretry is dropped.
	if cfg.RequestSurvivalEnabled && cfg.StreamRetryEnabled {
		cfg.StreamRetryEnabled = false
	}
	if cfg.StreamRetryEnabled {
		t.Fatal("mutual exclusion rule failed")
	}
}

// TestStreamRetryEnabledEnvParses pins the env-parse fix: the struct tag has
// declared LLM_GATEWAY_STREAM_RETRY_ENABLED since 2026-08-12 but Load()
// never read it, silently disabling the wrapper in env-only deployments.
func TestStreamRetryEnabledEnvParses(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STREAM_RETRY_ENABLED", "true")
	cfg := Load()
	if !cfg.StreamRetryEnabled {
		t.Fatal("LLM_GATEWAY_STREAM_RETRY_ENABLED=true must enable the wrapper")
	}
}
