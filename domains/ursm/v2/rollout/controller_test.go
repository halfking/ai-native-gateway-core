package rollout

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestDeterministic(t *testing.T) {
	c := New(Config{Mode: api.ModeCanary, CanaryPercent: 50})
	a := c.ShouldUseV2("tenantA", "gpt-4", "r1")
	b := c.ShouldUseV2("tenantA", "gpt-4", "r1")
	if a != b {
		t.Fatalf("decision must be stable across repeated calls with same inputs")
	}
}

func TestOffAlwaysFalse(t *testing.T) {
	c := New(Config{Mode: api.ModeOff})
	if c.ShouldUseV2("t", "m", "r") {
		t.Fatalf("off mode must never use v2")
	}
}

func TestAuthAlwaysTrue(t *testing.T) {
	c := New(Config{Mode: api.ModeAuthoritative})
	if !c.ShouldUseV2("t", "m", "r") {
		t.Fatalf("authoritative must always use v2")
	}
}

func TestCanaryRespectWhitelist(t *testing.T) {
	c := New(Config{Mode: api.ModeCanary, CanaryPercent: 0, CanaryTenants: []string{"vip"}})
	if !c.ShouldUseV2("vip", "m", "r") {
		t.Fatalf("whitelist must bypass percent")
	}
	if c.ShouldUseV2("other", "m", "r") {
		t.Fatalf("non-whitelist must respect percent")
	}
}
