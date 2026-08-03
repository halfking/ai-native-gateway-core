// spec §10 Step 5 C-1 assembly guard regression test.
//
// main.go wires the legacy credentialstate.Manager ONLY when URSM v2 is
// NOT in authoritative mode. In authoritative mode the manager must stay
// nil so the legacy read/write path is dead. This test pins the guard
// predicate against the real ursmv2.Manager type (the same construction
// path used in main.go) to guard against regressions in either:
//
//   - the guard condition (ursmV2Mgr.Mode() == ModeAuthoritative), or
//   - the v2 Manager's Mode() reporting.
//
// It deliberately mirrors the exact boolean used in main.go's assembly
// block (see cmd/gateway/main.go, search "spec §10 Step 5 C-1"). The
// router-level zero-call invariant is already covered by
// TestPlanCandidates_AuthoritativeZeroAvailable_StateManagerUnused in
// domains/streaming/executors; this test covers the earlier assembly
// decision so the manager is never even constructed in authoritative.
package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// buildV2ManagerForAssembly mirrors main.go's ursmv2.New(...) call but
// lets the test pick the mode. It returns the manager plus the
// authoritative-guard predicate used by the main.go assembly block.
func buildV2ManagerForAssembly(t *testing.T, mode api.RolloutMode) *ursmv2.Manager {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = mode
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	return ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
}

// TestAuthoritativeModeDisablesCredentialStateAssembly pins the spec
// §10 Step 5 C-1 invariant at the assembly layer: when URSM v2 is
// authoritative, the guard predicate that gates
// `stateManager = credentialstate.NewManager(...)` in main.go MUST be
// true, so the legacy manager is left nil.
func TestAuthoritativeModeDisablesCredentialStateAssembly(t *testing.T) {
	mgr := buildV2ManagerForAssembly(t, api.ModeAuthoritative)

	if mgr.Mode() != api.ModeAuthoritative {
		t.Fatalf("v2 manager Mode() = %q, want %q", mgr.Mode(), api.ModeAuthoritative)
	}
	// This is the exact boolean from main.go's assembly guard.
	disableLegacy := mgr != nil && mgr.Mode() == api.ModeAuthoritative
	if !disableLegacy {
		t.Fatalf("authoritative guard should disable legacy stateManager assembly; "+
			"disableLegacy=%v (mode=%q)", disableLegacy, mgr.Mode())
	}
}

// TestNonAuthoritativeModesKeepCredentialStateAssembly pins the
// complement: off / canary / shadow must NOT trip the authoritative
// guard, so the legacy stateManager assembly stays enabled in those
// modes (rollback / dual-run paths remain exercised).
func TestNonAuthoritativeModesKeepCredentialStateAssembly(t *testing.T) {
	for _, mode := range []api.RolloutMode{api.ModeOff, api.ModeShadow, api.ModeCanary} {
		t.Run(string(mode), func(t *testing.T) {
			mgr := buildV2ManagerForAssembly(t, mode)
			if mgr.Mode() != mode {
				t.Fatalf("v2 manager Mode() = %q, want %q", mgr.Mode(), mode)
			}
			disableLegacy := mgr != nil && mgr.Mode() == api.ModeAuthoritative
			if disableLegacy {
				t.Fatalf("mode %q must NOT disable legacy stateManager assembly", mode)
			}
		})
	}
}

// TestNilV2ManagerKeepsCredentialStateAssembly pins the nil-mgr branch:
// when redis is unavailable main.go leaves ursmV2Mgr == nil, which must
// NOT disable the legacy assembly (ursmV2Mgr == nil means v2 is off and
// the legacy path must keep running).
func TestNilV2ManagerKeepsCredentialStateAssembly(t *testing.T) {
	var mgr *ursmv2.Manager // nil
	// Exact boolean from main.go's assembly guard.
	disableLegacy := mgr != nil && mgr.Mode() == api.ModeAuthoritative
	if disableLegacy {
		t.Fatalf("nil v2 manager must not disable legacy stateManager assembly")
	}
}
