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

// TestShadowDoubleWrite (P0-3) pins the opt-in contract for shadow
// double-write. Default off (shadow is silent) → ShouldUseV2 false.
// Explicit on → true. Mode itself is unchanged — routing in shadow
// stays on the legacy credentialstate manager (see
// selectStateBackendWithReady, ModeAuthoritative-only promotion).
func TestShadowDoubleWrite(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"shadow default off", Config{Mode: api.ModeShadow}, false},
		{"shadow explicit on", Config{Mode: api.ModeShadow, ShadowDoubleWrite: true}, true},
		{"off mode double-write flag ignored", Config{Mode: api.ModeOff, ShadowDoubleWrite: true}, false},
		{"authoritative always on regardless of flag", Config{Mode: api.ModeAuthoritative, ShadowDoubleWrite: false}, true},
		{"canary unaffected by shadow flag", Config{Mode: api.ModeCanary, CanaryPercent: 100, ShadowDoubleWrite: false}, true},
		{"canary 0% with shadow flag unaffected", Config{Mode: api.ModeCanary, CanaryPercent: 0, ShadowDoubleWrite: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.cfg)
			got := c.ShouldUseV2("t", "m", "r")
			if got != tc.want {
				t.Fatalf("ShouldUseV2() = %v, want %v (cfg=%+v)", got, tc.want, tc.cfg)
			}
		})
	}
}

// TestShadowDoubleWriteAccessor pins the ShadowDoubleWrite() reader
// so callers (executor / sidecar instrumentation) can distinguish
// "shadow mode is on but quiet" from "shadow mode is on and
// double-writing for the cutover comparison".
func TestShadowDoubleWriteAccessor(t *testing.T) {
	// Default config (no explicit ShadowDoubleWrite) reports false.
	if New(Config{Mode: api.ModeShadow}).ShadowDoubleWrite() {
		t.Fatal("default Config{Mode: ModeShadow} must report ShadowDoubleWrite()==false")
	}
	if !New(Config{Mode: api.ModeShadow, ShadowDoubleWrite: true}).ShadowDoubleWrite() {
		t.Fatal("ShadowDoubleWrite=true must be readable")
	}
	// nil controller must not panic.
	var nilCtl *Controller
	if nilCtl.ShadowDoubleWrite() {
		t.Fatal("nil controller must report ShadowDoubleWrite()==false")
	}
}
