package pluginruntime

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var knownBindingCapabilities = map[string]struct{}{
	CapabilityRequestObserve: {}, CapabilityRequestMutate: {}, CapabilityRequestBlock: {},
	CapabilitySessionObserve: {}, CapabilitySessionClose: {},
	CapabilityResponseObserve: {}, CapabilityResponseMutate: {}, CapabilityResponseBlock: {}, CapabilityResponseStream: {},
	CapabilityToolObserve: {}, CapabilityToolMutate: {}, CapabilityToolBlock: {}, CapabilityToolExecute: {},
	CapabilityAuditEmit: {}, CapabilityDurableConsume: {},
}

var knownBindingPhases = map[BindingPhase]struct{}{
	PhaseRequest: {}, PhaseGovernance: {}, PhaseTransform: {}, PhaseTool: {},
	PhaseResponse: {}, PhaseStream: {}, PhaseAnalysis: {}, PhaseSessionClose: {},
}

var knownExecutionModes = map[ExecutionMode]struct{}{
	ExecutionSequential: {}, ExecutionParallel: {},
}

var knownFailurePolicies = map[FailurePolicy]struct{}{
	FailureOpen: {}, FailureClosed: {}, FailureSuspend: {}, FailureRetryDLQ: {},
}

var knownDTOProfiles = map[DTOProfile]struct{}{
	DTOProfileNone: {}, DTOProfileSummary: {}, DTOProfileRedacted: {},
}

// BindingValidationOptions controls security-sensitive binding validation.
type BindingValidationOptions struct {
	SandboxAvailable bool
}

// ValidateBinding validates a binding before it can participate in a lifecycle phase.
// The manifest capability list is intentionally not treated as authorization: a
// binding must explicitly declare each capability and pass this gate.
func ValidateBinding(pluginID string, b PluginBinding, opts BindingValidationOptions) error {
	if pluginID == "" || b.PluginID != "" && b.PluginID != pluginID {
		return fmt.Errorf("binding %q: plugin_id mismatch", b.BindingID)
	}
	if strings.TrimSpace(b.BindingID) == "" {
		return fmt.Errorf("binding: binding_id required")
	}
	if _, ok := knownBindingPhases[b.Phase]; !ok {
		return fmt.Errorf("binding %q: unsupported phase %q", b.BindingID, b.Phase)
	}
	if _, ok := knownExecutionModes[b.ExecutionMode]; !ok {
		return fmt.Errorf("binding %q: unsupported execution_mode %q", b.BindingID, b.ExecutionMode)
	}
	if _, ok := knownFailurePolicies[b.FailurePolicy]; !ok {
		return fmt.Errorf("binding %q: unsupported failure_policy %q", b.BindingID, b.FailurePolicy)
	}
	if _, ok := knownDTOProfiles[b.DTOProfile]; !ok {
		return fmt.Errorf("binding %q: unsupported dto_profile %q", b.BindingID, b.DTOProfile)
	}
	if b.TimeoutMillis <= 0 || b.TimeoutMillis > 120000 {
		return fmt.Errorf("binding %q: timeout_ms must be between 1 and 120000", b.BindingID)
	}
	if b.ConcurrencyLimit <= 0 || b.ConcurrencyLimit > 1024 {
		return fmt.Errorf("binding %q: concurrency_limit must be between 1 and 1024", b.BindingID)
	}
	if !b.Enabled {
		return nil
	}
	if len(b.Capabilities) == 0 {
		return fmt.Errorf("binding %q: at least one capability is required", b.BindingID)
	}
	seen := make(map[string]struct{}, len(b.Capabilities))
	for _, capability := range b.Capabilities {
		capability = strings.TrimSpace(capability)
		if _, ok := knownBindingCapabilities[capability]; !ok {
			return fmt.Errorf("binding %q: unsupported capability %q", b.BindingID, capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("binding %q: duplicate capability %q", b.BindingID, capability)
		}
		seen[capability] = struct{}{}
		if !opts.SandboxAvailable && isWriteCapability(capability) {
			return fmt.Errorf("binding %q: capability %q requires sandbox", b.BindingID, capability)
		}
	}
	return nil
}

func isWriteCapability(capability string) bool {
	return strings.HasSuffix(capability, ".mutate") ||
		strings.HasSuffix(capability, ".block") ||
		capability == CapabilityToolExecute
}

// BindingRegistry is an in-memory, deterministic registry for lifecycle bindings.
// It deliberately does not own plugin menu/status state or business persistence.
type BindingRegistry struct {
	mu       sync.RWMutex
	bindings map[string][]PluginBinding
}

func NewBindingRegistry() *BindingRegistry {
	return &BindingRegistry{bindings: make(map[string][]PluginBinding)}
}

// Register validates and atomically replaces all bindings for one plugin.
// Re-registering the same validated set is idempotent; duplicate binding IDs fail.
func (r *BindingRegistry) Register(pluginID string, bindings []PluginBinding, opts BindingValidationOptions) error {
	if r == nil {
		return fmt.Errorf("binding registry is nil")
	}
	seen := make(map[string]struct{}, len(bindings))
	validated := make([]PluginBinding, 0, len(bindings))
	for _, binding := range bindings {
		if err := ValidateBinding(pluginID, binding, opts); err != nil {
			return err
		}
		if _, exists := seen[binding.BindingID]; exists {
			return fmt.Errorf("plugin %q: duplicate binding_id %q", pluginID, binding.BindingID)
		}
		seen[binding.BindingID] = struct{}{}
		binding.PluginID = pluginID
		binding.Capabilities = append([]string(nil), binding.Capabilities...)
		binding.TenantScope = append([]string(nil), binding.TenantScope...)
		binding.ModelScope = append([]string(nil), binding.ModelScope...)
		validated = append(validated, binding)
	}
	sort.SliceStable(validated, func(i, j int) bool {
		if validated[i].Phase != validated[j].Phase {
			return validated[i].Phase < validated[j].Phase
		}
		if validated[i].Priority != validated[j].Priority {
			return validated[i].Priority < validated[j].Priority
		}
		return validated[i].BindingID < validated[j].BindingID
	})
	r.mu.Lock()
	r.bindings[pluginID] = validated
	r.mu.Unlock()
	return nil
}

func (r *BindingRegistry) Bindings(pluginID string) []PluginBinding {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	bindings := r.bindings[pluginID]
	out := make([]PluginBinding, len(bindings))
	copy(out, bindings)
	for i := range out {
		out[i].Capabilities = append([]string(nil), out[i].Capabilities...)
		out[i].TenantScope = append([]string(nil), out[i].TenantScope...)
		out[i].ModelScope = append([]string(nil), out[i].ModelScope...)
	}
	return out
}

func (r *BindingRegistry) Ready(pluginID string) bool {
	for _, binding := range r.Bindings(pluginID) {
		if binding.Enabled {
			return true
		}
	}
	return false
}

// ScopeAllows applies an allowlist scope. An empty scope means all values in the
// already-authenticated request context; non-empty scope requires exact match.
func ScopeAllows(scope []string, value string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, candidate := range scope {
		if candidate == value {
			return true
		}
	}
	return false
}

// BindingTimeout returns a bounded duration for adapter calls.
func BindingTimeout(binding PluginBinding) time.Duration {
	if binding.TimeoutMillis <= 0 {
		return time.Millisecond
	}
	return time.Duration(binding.TimeoutMillis) * time.Millisecond
}
