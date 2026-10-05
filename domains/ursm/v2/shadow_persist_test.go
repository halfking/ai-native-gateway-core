package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// 2026-10-04: URSM_V2_SHADOW_DOUBLE_WRITE was doing two unrelated jobs.
// It (1) makes the shadow sidecar record, feeding
// llm_gateway_ursm_v2_shadow_records_total — the only source of the 7-day
// legacy-vs-v2 drift evidence that cutover is gated on — and (2) it also
// enables the snapshot writer, which produced 46,801,709 INSERT attempts for
// 27,208,769 rows that landed (~42% duplicates rejected by ON CONFLICT) and
// filled a 10 GB table that is 45% of the database.
//
// One switch cannot both preserve the evidence and remove the waste, so
// URSM_V2_SHADOW_PERSIST governs the writer alone. These tests pin three
// things: the decoupled behaviour works, the back-compat default is exact, and
// only a spelled-out negative changes anything.

// TestResolvePersistEnabledDecouplesWriterFromSidecar is the point of the
// change: with the sidecar still double-writing (evidence preserved), the
// snapshot writer can be stopped.
func TestResolvePersistEnabledDecouplesWriterFromSidecar(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "shadow + double-write on + persist explicitly off ⇒ writer off, sidecar still on",
			cfg: Config{
				Mode: api.ModeShadow, ShadowDoubleWrite: true,
				ShadowPersist: false, ShadowPersistSet: true,
			},
			want: false,
		},
		{
			name: "shadow + double-write off + persist explicitly on ⇒ writer on",
			cfg: Config{
				Mode: api.ModeShadow, ShadowDoubleWrite: false,
				ShadowPersist: true, ShadowPersistSet: true,
			},
			want: true,
		},
		{
			name: "back-compat: persist unset follows double-write on",
			cfg:  Config{Mode: api.ModeShadow, ShadowDoubleWrite: true},
			want: true,
		},
		{
			name: "back-compat: persist unset follows double-write off",
			cfg:  Config{Mode: api.ModeShadow, ShadowDoubleWrite: false},
			want: false,
		},
		{
			name: "authoritative ignores the shadow knobs entirely",
			cfg: Config{
				Mode: api.ModeAuthoritative, ShadowDoubleWrite: false,
				ShadowPersist: false, ShadowPersistSet: true,
			},
			want: true,
		},
		{
			name: "canary ignores the shadow knobs entirely",
			cfg: Config{
				Mode: api.ModeCanary, CanaryPercent: 100, ShadowDoubleWrite: false,
				ShadowPersist: false, ShadowPersistSet: true,
			},
			want: true,
		},
		{
			name: "off mode never persists",
			cfg: Config{
				Mode: api.ModeOff, ShadowDoubleWrite: true,
				ShadowPersist: true, ShadowPersistSet: true,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolvePersistEnabled(); got != tc.want {
				t.Fatalf("ResolvePersistEnabled()=%v, want %v (cfg=%+v)", got, tc.want, tc.cfg)
			}
		})
	}
}

// Back-compat is the load-bearing default: an operator who has never heard of
// URSM_V2_SHADOW_PERSIST must get byte-identical behaviour to before. If this
// drifts, every existing shadow deployment silently changes.
func TestShadowPersistUnsetIsByteIdenticalToLegacyBehaviour(t *testing.T) {
	for _, dw := range []bool{true, false} {
		cfg := Config{Mode: api.ModeShadow, ShadowDoubleWrite: dw}
		// The exact expression main.go used before 2026-10-04.
		legacy := cfg.Mode != api.ModeOff &&
			(cfg.Mode != api.ModeShadow || cfg.ShadowDoubleWrite)
		if got := cfg.ResolvePersistEnabled(); got != legacy {
			t.Fatalf("double_write=%v: ResolvePersistEnabled()=%v, legacy expression=%v — back-compat broken",
				dw, got, legacy)
		}
	}
}

// The env parser must require an explicit negative. A typo must never stop
// snapshots, because that is the failure mode that would silently end the
// cutover evidence window.
func TestLoadFromEnvShadowPersist(t *testing.T) {
	cases := []struct {
		env     string
		set     bool
		persist bool
		comment string
	}{
		{"", false, false, "unset → not stated, persist follows double-write"},
		{"0", true, false, "explicit off"},
		{"false", true, false, "explicit off"},
		{"no", true, false, "explicit off"},
		{"1", true, true, "explicit on"},
		{"true", true, true, "explicit on"},
		{"yes", true, true, "explicit on"},
		{"maybe", true, true, "unrecognised ⇒ treated as on; a typo must not stop snapshots"},
		{" 0 ", true, false, "whitespace tolerated"},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env+"/"+tc.comment, func(t *testing.T) {
			t.Setenv("URSM_V2_SHADOW_PERSIST", tc.env)
			cfg := LoadFromEnv()
			if cfg.ShadowPersistSet != tc.set {
				t.Fatalf("ShadowPersistSet=%v, want %v", cfg.ShadowPersistSet, tc.set)
			}
			if cfg.ShadowPersist != tc.persist {
				t.Fatalf("ShadowPersist=%v, want %v", cfg.ShadowPersist, tc.persist)
			}
		})
	}
}

// Turning the snapshot writer off must NOT change Mode, and must NOT change
// the sidecar switch — those carry the routing decision and the cutover
// evidence respectively. This mirrors the existing
// TestLoadFromEnv_ShadowDoubleWrite_DoesNotChangeMode invariant.
func TestShadowPersistChangesNothingButTheWriter(t *testing.T) {
	// Mode must be shadow: outside shadow mode the writer is not gated by
	// these knobs at all (see ResolvePersistEnabled), so the assertion would
	// pass for the wrong reason.
	t.Setenv("URSM_V2_MODE", "shadow")
	t.Setenv("URSM_V2_SHADOW_DOUBLE_WRITE", "1")
	t.Setenv("URSM_V2_SHADOW_PERSIST", "0")
	cfg := LoadFromEnv()
	if !cfg.ShadowDoubleWrite {
		t.Fatal("URSM_V2_SHADOW_PERSIST=0 must not silence the sidecar — that is the cutover evidence")
	}
	if cfg.ResolvePersistEnabled() {
		t.Fatal("writer should be off")
	}
}
