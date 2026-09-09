package config

import (
	"os"
	"testing"
)

func TestNormalizeRuntimeRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{name: "empty defaults active", want: RuntimeRoleActive},
		{name: "active", in: RuntimeRoleActive, want: RuntimeRoleActive},
		{name: "traffic only", in: RuntimeRoleTrafficOnly, want: RuntimeRoleTrafficOnly},
		{name: "unknown fails closed", in: "candidate", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRuntimeRole(tc.in)
			if tc.err {
				if err == nil {
					t.Fatal("NormalizeRuntimeRole() error = nil")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeRuntimeRole(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestValidateRuntimeRole(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ValidateRuntimeRole(); err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeRole != RuntimeRoleActive {
		t.Fatalf("default role = %q, want active", cfg.RuntimeRole)
	}
	cfg.RuntimeRole = RuntimeRoleTrafficOnly
	if err := cfg.ValidateRuntimeRole(); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsTrafficOnly() {
		t.Fatal("traffic-only role not detected")
	}
}

// TestCredRecoveryEnvContract documents the 2026-09-09 P0 fix's runtime
// contract: credential_recovery must start on traffic-only instances
// (so a stuck / no-active-primary cluster can still self-heal) UNLESS
// operators explicitly opt out via LLM_GATEWAY_CRED_RECOVERY_DISABLED.
//
// The exact precedence — env var → opt-out → gate — is wired into
// cmd/gateway/main.go's bg-services block; this test guards the two
// boolean checks an operator actually cares about, decoupled from the
// gateway bootstrap so it stays deterministic under `go test ./config`.
func TestCredRecoveryEnvContract(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		set     bool
		wantOpt bool
	}{
		{name: "unset is no opt-out", env: "", set: false, wantOpt: false},
		{name: "true opts out", env: "true", set: true, wantOpt: true},
		{name: "TRUE (case insensitive) opts out", env: "TRUE", set: true, wantOpt: true},
		{name: "Trailing whitespace tolerated", env: "  true  ", set: true, wantOpt: true},
		{name: "false does NOT opt out", env: "false", set: true, wantOpt: false},
		{name: "1 does NOT opt out (only literal true)", env: "1", set: true, wantOpt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envName := "LLM_GATEWAY_CRED_RECOVERY_DISABLED"
			orig, hadOrig := os.LookupEnv(envName)
			defer func() {
				if hadOrig {
					os.Setenv(envName, orig)
				} else {
					os.Unsetenv(envName)
				}
			}()
			if tc.set {
				os.Setenv(envName, tc.env)
			} else {
				os.Unsetenv(envName)
			}
			got := isCredRecoveryDisabledForTest()
			if got != tc.wantOpt {
				t.Fatalf("isCredRecoveryDisabled() = %v, want %v (env=%q set=%v)",
					got, tc.wantOpt, tc.env, tc.set)
			}
		})
	}
}

// TestQuotaProbeEnvContract mirrors TestCredRecoveryEnvContract for the
// 2026-09-10 quota-probe family opt-out (LLM_GATEWAY_QUOTA_PROBE_DISABLED):
// credProbeV2 / PeriodicQuotaProbe / BalanceQuotaProbe now run on data-plane
// and traffic-only nodes by default so hard-quota credentials
// (permanently_exhausted / balance_exhausted) regain an automatic recovery
// path; only the literal "true" opts a split-topology operator out.
func TestQuotaProbeEnvContract(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		set     bool
		wantOpt bool
	}{
		{name: "unset is no opt-out", env: "", set: false, wantOpt: false},
		{name: "true opts out", env: "true", set: true, wantOpt: true},
		{name: "TRUE (case insensitive) opts out", env: "TRUE", set: true, wantOpt: true},
		{name: "Trailing whitespace tolerated", env: "  true  ", set: true, wantOpt: true},
		{name: "false does NOT opt out", env: "false", set: true, wantOpt: false},
		{name: "1 does NOT opt out (only literal true)", env: "1", set: true, wantOpt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envName := "LLM_GATEWAY_QUOTA_PROBE_DISABLED"
			orig, hadOrig := os.LookupEnv(envName)
			defer func() {
				if hadOrig {
					os.Setenv(envName, orig)
				} else {
					os.Unsetenv(envName)
				}
			}()
			if tc.set {
				os.Setenv(envName, tc.env)
			} else {
				os.Unsetenv(envName)
			}
			if got := IsQuotaProbeDisabled(); got != tc.wantOpt {
				t.Fatalf("IsQuotaProbeDisabled() = %v, want %v (env=%q set=%v)",
					got, tc.wantOpt, tc.env, tc.set)
			}
		})
	}
}
