package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	RuntimeRoleActive      = "active"
	RuntimeRoleTrafficOnly = "traffic-only"
)

// NormalizeRuntimeRole validates the process role used by blue-green deploys.
// Empty values retain the active default; unknown values fail closed so a
// candidate can never accidentally start with ambiguous worker ownership.
func NormalizeRuntimeRole(role string) (string, error) {
	if role == "" {
		return RuntimeRoleActive, nil
	}
	switch role {
	case RuntimeRoleActive, RuntimeRoleTrafficOnly:
		return role, nil
	default:
		return "", fmt.Errorf("invalid runtime role %q (want %s or %s)", role, RuntimeRoleActive, RuntimeRoleTrafficOnly)
	}
}

func (c *Config) IsTrafficOnly() bool {
	return c != nil && c.RuntimeRole == RuntimeRoleTrafficOnly
}

// ValidateRuntimeRole applies the fail-closed role contract after YAML/env
// precedence has been resolved.
func (c *Config) ValidateRuntimeRole() error {
	role, err := NormalizeRuntimeRole(c.RuntimeRole)
	if err != nil {
		return err
	}
	c.RuntimeRole = role
	return nil
}

// IsCredRecoveryDisabled reports whether an operator has opted the
// credential_recovery worker out via the LLM_GATEWAY_CRED_RECOVERY_DISABLED
// environment variable. The check is intentionally narrow — only the literal
// "true" (case-insensitive, trimmed) counts — so an unset, empty, "false",
// or "1" value keeps the recovery loop running.
//
// 2026-09-09 P0 fix: the recovery worker is allowed (and required) to run on
// traffic-only instances so a cluster with no active-role primary can still
// self-heal credentials whose quota / availability recover_at has elapsed.
// Operators who want the original strict opt-in (only the active-role
// instance runs the loop) can revert to that behaviour by exporting this
// variable on every node.
func IsCredRecoveryDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("LLM_GATEWAY_CRED_RECOVERY_DISABLED")), "true")
}

// isCredRecoveryDisabledForTest is the test-only alias used by
// runtime_role_test.go. Kept as a separate name so production callers
// always reach IsCredRecoveryDisabled and never accidentally depend on a
// renamed internal helper.
func isCredRecoveryDisabledForTest() bool { return IsCredRecoveryDisabled() }

// IsQuotaProbeDisabled reports whether an operator has opted the quota-probe
// family (credProbeV2 / PeriodicQuotaProbe / BalanceQuotaProbe and their
// probe-picker helpers) out of running on data-plane / traffic-only nodes
// via the LLM_GATEWAY_QUOTA_PROBE_DISABLED environment variable. Same narrow
// contract as IsCredRecoveryDisabled: only the literal "true"
// (case-insensitive, trimmed) counts.
//
// 2026-09-10 P0 audit fix (follow-up to the 2026-09-09 credential_recovery
// fix): the credential_recovery SQL deliberately defers hard-quota
// credentials (quota_state='permanently_exhausted' / 'balance_exhausted')
// to BalanceQuotaProbe → writeHealth, and credProbeV2 is the ProbeNow
// executor both quota ticks rely on. Those workers used to live behind the
// bare `!bgDataPlaneOnly` gate, so on clusters whose only instances are
// traffic-only canaries (154/245 since the 2026-08-31 blue-green pinning)
// a recharged credential such as MiniMax cred 41 had NO automatic recovery
// path at all. The family now runs by default everywhere; operators with a
// true split topology (separate control-plane primary) can restore the old
// behaviour by exporting this variable on the data-plane candidates.
func IsQuotaProbeDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("LLM_GATEWAY_QUOTA_PROBE_DISABLED")), "true")
}
