package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestDefaultConfig_DirectAuthoritativeStart(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Mode != api.ModeAuthoritative {
		t.Fatalf("default mode=%q, want %q", cfg.Mode, api.ModeAuthoritative)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default direct-start config must validate: %v", err)
	}
}

func TestConfigValidateRejectsInvalidModeAndCanaryPercent(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"invalid mode", Config{Mode: "enabled", CanaryPercent: 0}},
		{"negative percent", Config{Mode: api.ModeCanary, CanaryPercent: -1}},
		{"oversized percent", Config{Mode: api.ModeCanary, CanaryPercent: 101}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadFromEnvRejectsInvalidCanaryPercent(t *testing.T) {
	t.Setenv("URSM_V2_CANARY_PERCENT", "101")
	if err := LoadFromEnv().Validate(); err == nil {
		t.Fatal("expected invalid canary percent to reject startup configuration")
	}
}

func TestLoadFromEnv_CanaryAllowLists(t *testing.T) {
	t.Setenv("URSM_V2_MODE", "canary")
	t.Setenv("URSM_V2_CANARY_TENANTS", " tenant-a,tenant-b,tenant-a, ")
	t.Setenv("URSM_V2_CANARY_MODELS", " model-a, model-b ")
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation failed: %v", err)
	}
	if got, want := len(cfg.CanaryTenants), 2; got != want || cfg.CanaryTenants[0] != "tenant-a" || cfg.CanaryTenants[1] != "tenant-b" {
		t.Fatalf("CanaryTenants=%v, want [tenant-a tenant-b]", cfg.CanaryTenants)
	}
	if got, want := len(cfg.CanaryModels), 2; got != want || cfg.CanaryModels[0] != "model-a" || cfg.CanaryModels[1] != "model-b" {
		t.Fatalf("CanaryModels=%v, want [model-a model-b]", cfg.CanaryModels)
	}
}
