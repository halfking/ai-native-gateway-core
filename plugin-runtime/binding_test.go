package pluginruntime

import (
	"strings"
	"testing"
)

func validBinding(id string, priority int) PluginBinding {
	return PluginBinding{
		BindingID:        id,
		Phase:            PhaseRequest,
		Priority:         priority,
		ExecutionMode:    ExecutionSequential,
		Capabilities:     []string{CapabilityRequestObserve},
		TimeoutMillis:    1000,
		ConcurrencyLimit: 1,
		FailurePolicy:    FailureOpen,
		DTOProfile:       DTOProfileSummary,
		Enabled:          true,
	}
}

func TestBindingRegistryOrdersBindingsDeterministically(t *testing.T) {
	r := NewBindingRegistry()
	late := validBinding("late", 20)
	early := validBinding("early", 10)
	if err := r.Register("plugin-a", []PluginBinding{late, early}, BindingValidationOptions{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	bindings := r.Bindings("plugin-a")
	if len(bindings) != 2 || bindings[0].BindingID != "early" || bindings[1].BindingID != "late" {
		t.Fatalf("bindings = %+v, want priority order", bindings)
	}
	bindings[0].Capabilities[0] = CapabilityToolExecute
	if got := r.Bindings("plugin-a")[0].Capabilities[0]; got != CapabilityRequestObserve {
		t.Fatalf("registry leaked mutable capabilities: %q", got)
	}
}

func TestBindingRegistryRejectsUnsafeAndUnknownCapabilities(t *testing.T) {
	r := NewBindingRegistry()
	unknown := validBinding("unknown", 1)
	unknown.Capabilities = []string{"unknown.capability"}
	if err := r.Register("plugin-a", []PluginBinding{unknown}, BindingValidationOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported capability") {
		t.Fatalf("unknown capability error = %v", err)
	}
	unsafe := validBinding("unsafe", 1)
	unsafe.Capabilities = []string{CapabilityToolExecute}
	if err := r.Register("plugin-a", []PluginBinding{unsafe}, BindingValidationOptions{}); err == nil || !strings.Contains(err.Error(), "requires sandbox") {
		t.Fatalf("unsafe capability error = %v", err)
	}
}

func TestBindingRegistryRejectsDuplicateIDAndInvalidScopeContract(t *testing.T) {
	r := NewBindingRegistry()
	binding := validBinding("duplicate", 1)
	if err := r.Register("plugin-a", []PluginBinding{binding, binding}, BindingValidationOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate binding_id") {
		t.Fatalf("duplicate binding error = %v", err)
	}
	if !ScopeAllows(nil, "tenant-a") || !ScopeAllows([]string{"tenant-a"}, "tenant-a") || ScopeAllows([]string{"tenant-a"}, "tenant-b") {
		t.Fatal("unexpected scope evaluation")
	}
}

func TestManifestRejectsUnsafePluginAndBinding(t *testing.T) {
	m := &Manifest{
		PluginID:             "../escape",
		PluginVersion:        "1",
		GatewayCompatibility: GatewayCompatibility{APIContract: SupportedAPIContract},
		Runtime:              Runtime{Entrypoint: "bin/plugin", Protocol: "http-unix-socket", HandshakePath: "/handshake", HealthPath: "/health"},
	}
	if err := m.validate(); err == nil {
		t.Fatal("unsafe plugin id accepted")
	}
	m.PluginID = "plugin-a"
	m.Bindings = []PluginBinding{validBinding("mutate", 1)}
	m.Bindings[0].Capabilities = []string{CapabilityRequestMutate}
	if err := m.validate(); err == nil || !strings.Contains(err.Error(), "requires sandbox") {
		t.Fatalf("unsafe binding error = %v", err)
	}
}
