package pluginruntime

import "fmt"

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

// AllowExecution is the runtime gate that decides whether a registered binding
// may invoke. It does NOT re-validate binding shape (that happens at
// registration via ValidateBinding). Disabled bindings short-circuit to nil
// since they are never invoked; enabled bindings must have an available sandbox
// when they declare any write capability.
func (e *SandboxEnforcer) AllowExecution(b PluginBinding) error {
	if !b.Enabled {
		return nil
	}
	if e.policy.Available {
		return nil
	}
	if requiresWriteCapability(b.Capabilities) {
		return fmt.Errorf("allow plugin execution failed: write capability requires sandbox (binding_id=%s, plugin_id=%s, capabilities=%v)",
			b.BindingID, b.PluginID, b.Capabilities)
	}
	return nil
}

// AllowDataPlaneWrite is an explicit, separate gate for data-plane mutations.
// Even observability-only bindings pass AllowExecution; this additionally rejects
// any binding that would let a plugin mutate, block or execute before sandbox.
func (e *SandboxEnforcer) AllowDataPlaneWrite(b PluginBinding) error {
	if !e.policy.Available {
		return fmt.Errorf("allow data-plane write failed: sandbox unavailable (binding_id=%s, plugin_id=%s)",
			b.BindingID, b.PluginID)
	}
	if !requiresWriteCapability(b.Capabilities) {
		return fmt.Errorf("allow data-plane write failed: binding has no write capability (binding_id=%s, plugin_id=%s)",
			b.BindingID, b.PluginID)
	}
	return nil
}

// BuildControlledEnv returns the minimal environment exposed to a plugin: only
// variable names listed in EnvAllowlist are copied from base, and any
// credential/DSN-bearing variable is dropped regardless of the allowlist. The
// result never contains the full gateway environment.
func (e *SandboxEnforcer) BuildControlledEnv(base map[string]string) map[string]string {
	out := make(map[string]string, len(e.policy.EnvAllowlist))
	for _, name := range e.policy.EnvAllowlist {
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
// forced: a plugin may at most receive a redacted view (the explicit "none"
// profile means no DTO at all and is honored verbatim).
func (e *SandboxEnforcer) DTOExposure(b PluginBinding) (DTOProfile, error) {
	switch b.DTOProfile {
	case DTOProfileNone, DTOProfileSummary, DTOProfileRedacted:
		// accepted profiles
	default:
		return "", fmt.Errorf("resolve DTO exposure failed: unsupported dto_profile (binding_id=%s, dto_profile=%q)",
			b.BindingID, b.DTOProfile)
	}
	if !e.policy.Available {
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
