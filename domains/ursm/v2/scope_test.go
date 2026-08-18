package v2

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func strictCanaryConfig() Config {
	cfg := DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.StrictCanary = true
	cfg.CanaryTenants = []string{"canary-tenant"}
	cfg.CanaryCredentials = []int{101}
	cfg.CanaryModels = []string{"glm-5.2"}
	cfg.RedisKeyPrefix = "ursm:v2:canary:"
	return cfg
}

func TestStrictCanaryConfigRejectsUnsafeScope(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"wrong mode", func(c *Config) { c.Mode = api.ModeShadow }},
		{"percent rollout", func(c *Config) { c.CanaryPercent = 1 }},
		{"missing tenant", func(c *Config) { c.CanaryTenants = nil }},
		{"missing credential", func(c *Config) { c.CanaryCredentials = nil }},
		{"missing model", func(c *Config) { c.CanaryModels = nil }},
		{"default prefix", func(c *Config) { c.RedisKeyPrefix = DefaultConfig().RedisKeyPrefix }},
		{"prefix without separator", func(c *Config) { c.RedisKeyPrefix = "ursm:v2:canary" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := strictCanaryConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() succeeded for unsafe strict canary configuration")
			}
		})
	}
}

func TestStrictCanaryScopeRequiresExactTriple(t *testing.T) {
	scope := newScope(true, []string{"canary-tenant"}, []int{101}, []string{"glm-5.2"})
	if !scope.Allows("canary-tenant", 101, "glm-5.2") {
		t.Fatal("allowlisted triple was rejected")
	}
	for _, tc := range []struct {
		tenant string
		cred   int
		model  string
	}{
		{"other", 101, "glm-5.2"},
		{"canary-tenant", 102, "glm-5.2"},
		{"canary-tenant", 101, "glm-5.2-alias"},
		{"", 101, "glm-5.2"},
	} {
		if scope.Allows(tc.tenant, tc.cred, tc.model) {
			t.Fatalf("out-of-scope triple admitted: tenant=%q cred=%d model=%q", tc.tenant, tc.cred, tc.model)
		}
	}
}

func TestStrictCanaryManagerRejectsOutOfScopeWrites(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mgr := New(Dependencies{Redis: rdb, Config: strictCanaryConfig()})
	ctx := context.Background()

	if err := mgr.ApplyProbeForTenant(ctx, "canary-tenant", api.ProbeOutcome{CredentialID: 101, RawModel: "glm-5.2", Success: true}); err != nil {
		t.Fatalf("allowlisted probe failed: %v", err)
	}
	if !mr.Exists("ursm:v2:canary:node:canary-tenant:101:glm-5.2") {
		t.Fatal("allowlisted probe did not write isolated prefix")
	}

	err := mgr.ApplyProbeForTenant(ctx, "canary-tenant", api.ProbeOutcome{CredentialID: 102, RawModel: "glm-5.2", Success: true})
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("out-of-scope probe error=%v, want ErrOutOfScope", err)
	}
	if mr.Exists("ursm:v2:canary:node:canary-tenant:102:glm-5.2") {
		t.Fatal("out-of-scope probe wrote Redis state")
	}

	err = mgr.RecordRequest(ctx, api.RequestOutcome{TenantID: "canary-tenant", CredentialID: 102, RawModel: "glm-5.2", RequestID: "out-of-scope"})
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("out-of-scope request error=%v, want ErrOutOfScope", err)
	}
}
