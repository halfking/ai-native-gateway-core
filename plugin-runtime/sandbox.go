package pluginruntime

import (
	"fmt"
	"sort"
)

// SandboxEnforcer applies Workflow B (execution sandbox and isolation) to plugin
// bindings and execution requests. It is the runtime counterpart to the
// manifest/binding gate: the binding layer rejects unknown capabilities and
// write-capabilities-before-sandbox, while the enforcer refuses to actually
// hand a plugin a privileged environment or full request DTO when no sandbox is
// established. Both layers must fail-closed; the enforcer is defense in depth.
//
// Key invariants from the orchestration design:
//   - no full gateway environment, DSN or credentials are ever passed to a plugin;
//   - data-plane write capability (mutate/block/tool.execute) is refused until a
//     real sandbox is available;
//   - the DTO exposed to a plugin is gated by its declared DTOProfile and the
//     sandbox state (redacted before a sandbox exists).
type SandboxEnforcer struct {
	policy SandboxPolicy
}

// NewSandboxEnforcer builds an enforcer from a validated policy. A nil policy is
// treated as the zero-trust default (no sandbox available).
func NewSandboxEnforcer(policy *SandboxPolicy) *SandboxEnforcer {
	if policy == nil {
		return &SandboxEnforcer{policy: DefaultSandboxPolicy()}
	}
	return &SandboxEnforcer{policy: *policy}
}

// Policy returns the effective policy.
func (e *SandboxEnforcer) Policy() SandboxPolicy { return e.policy }

// AllowExecution validates that a ready, handshake-passing binding may execute
// under the current sandbox. It returns an error if the binding requires any
// write capability but no real sandbox is available.
func (e *SandboxEnforcer) AllowExecution(b PluginBinding) error {
	if err := ValidateBinding(b.PluginID, b, BindingValidationOptions{SandboxAvailable: e.policy.Available}); err != nil {
		return err
	}
	if !e.policy.Available && requiresWriteCapability(b.Capabilities) {
		return fmt.Errorf("sandbox: binding %q requires write capability %v but no sandbox is available", b.BindingID, b.Capabilities)
	}
	return nil
}

// AllowDataPlaneWrite is an explicit, separate gate for data-plane mutations.
// Even observability-only bindings pass AllowExecution; this additionally rejects
// any binding that would let a plugin mutate, block or execute before sandbox.
func (e *SandboxEnforcer) AllowDataPlaneWrite(b PluginBinding) error {
	if !e.policy.Available {
		return fmt.Errorf("sandbox: data-plane write capability requires an available sandbox (binding %q)", b.BindingID)
	}
	if !requiresWriteCapability(b.Capabilities) {
		return fmt.Errorf("sandbox: binding %q has no data-plane write capability", b.BindingID)
	}
	return nil
}

// BuildControlledEnv returns the minimal environment exposed to a plugin: only
// variable names listed in EnvAllowlist are copied from base, and any
// credential/DSN-bearing variable is dropped regardless of the allowlist. The
// result never contains the full gateway environment.
func (e *SandboxEnforcer) BuildControlledEnv(base map[string]string) map[string]string {
	out := make(map[string]string, len(e.policy.EnvAllowlist))
	allow := append([]string(nil), e.policy.EnvAllowlist...)
	sort.Strings(allow)
	for _, name := range allow {
		if sensitiveEnvKey(name) {
			continue
		}
		if v, ok := base[name]; ok {
			out[name] = v
		}
	}
	return out
}

// DTOExposure decides the maximum request data a plugin may receive, honoring
// the binding's declared DTOProfile. Before a sandbox exists, redaction is
// forced: a plugin may at most receive a redacted/summary view, never the raw
// gateway request or environment.
func (e *SandboxEnforcer) DTOExposure(b PluginBinding) (DTOProfile, error) {
	switch b.DTOProfile {
	case DTOProfileNone, DTOProfileSummary, DTOProfileRedacted:
		// accepted profiles
	default:
		return "", fmt.Errorf("sandbox: binding %q has unsupported dto_profile %q", b.BindingID, b.DTOProfile)
	}
	if !e.policy.Available {
		// Fail-closed: without a sandbox a plugin only ever receives a redacted
		// view. The explicit "none" profile (no DTO at all) stays none; every
		// other profile is clamped down to redacted so no full/summary request
		// data reaches an unsandboxed process.
		if b.DTOProfile == DTOProfileNone {
			return DTOProfileNone, nil
		}
		return DTOProfileRedacted, nil
	}
	return b.DTOProfile, nil
}

// requiresWriteCapability reports whether any capability implies a data-plane
// write (mutate/block/execute). It reuses the binding layer's classification so
// the two gates never drift.
func requiresWriteCapability(caps []string) bool {
	for _, c := range caps {
		if isWriteCapability(c) {
			return true
		}
	}
	return false
}
