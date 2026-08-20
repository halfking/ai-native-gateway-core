package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// PluginProbe is the minimum readiness contract for an orchestration plugin.
// The plugin is not ready until both methods succeed.
type PluginProbe interface {
	Handshake(context.Context, *pluginruntime.Manifest) error
	Health(context.Context, *pluginruntime.Manifest) error
}

type PluginLoaderConfig struct {
	Probe            PluginProbe
	SandboxAvailable bool
}

type PluginLoader struct {
	mu               sync.RWMutex
	probe            PluginProbe
	sandboxAvailable bool
	bindings         *pluginruntime.BindingRegistry
	manifests        map[string]*pluginruntime.Manifest
	ready            map[string]bool
}

func NewPluginLoader(cfg PluginLoaderConfig) *PluginLoader {
	return &PluginLoader{
		probe:            cfg.Probe,
		sandboxAvailable: cfg.SandboxAvailable,
		bindings:         pluginruntime.NewBindingRegistry(),
		manifests:        make(map[string]*pluginruntime.Manifest),
		ready:            make(map[string]bool),
	}
}

// Load validates, registers, handshakes and health-checks one plugin atomically.
// A failed gate leaves the plugin unavailable and never marks it ready.
func (l *PluginLoader) Load(ctx context.Context, manifest *pluginruntime.Manifest) error {
	if l == nil || l.bindings == nil {
		return fmt.Errorf("load plugin failed: loader unavailable (plugin_id=%s)", pluginID(manifest))
	}
	if manifest == nil {
		return fmt.Errorf("load plugin failed: manifest is nil (plugin_id=unknown)")
	}
	if err := validateManifest(manifest); err != nil {
		return fmt.Errorf("load plugin failed: %w (plugin_id=%s)", err, manifest.PluginID)
	}
	l.mu.Lock()
	l.ready[manifest.PluginID] = false
	l.mu.Unlock()
	if err := l.bindings.Register(manifest.PluginID, manifest.Bindings, pluginruntime.BindingValidationOptions{SandboxAvailable: l.sandboxAvailable}); err != nil {
		return fmt.Errorf("load plugin failed: register bindings: %w (plugin_id=%s)", err, manifest.PluginID)
	}
	if l.probe == nil {
		return fmt.Errorf("load plugin failed: readiness probe unavailable (plugin_id=%s)", manifest.PluginID)
	}
	if err := l.probe.Handshake(ctx, manifest); err != nil {
		return fmt.Errorf("load plugin failed: handshake: %w (plugin_id=%s)", err, manifest.PluginID)
	}
	if err := l.probe.Health(ctx, manifest); err != nil {
		return fmt.Errorf("load plugin failed: health: %w (plugin_id=%s)", err, manifest.PluginID)
	}
	clone := cloneManifest(manifest)
	l.mu.Lock()
	l.manifests[manifest.PluginID] = clone
	l.ready[manifest.PluginID] = true
	l.mu.Unlock()
	return nil
}

func (l *PluginLoader) Ready(pluginID string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ready[pluginID]
}

func (l *PluginLoader) Authorize(bindingID, capability, tenantID, model string) error {
	if l == nil {
		return fmt.Errorf("authorize plugin failed: loader unavailable (binding_id=%s)", bindingID)
	}
	for _, manifest := range l.snapshotManifests() {
		for _, binding := range manifest.Bindings {
			if binding.BindingID != bindingID {
				continue
			}
			if !l.Ready(manifest.PluginID) {
				return fmt.Errorf("authorize plugin failed: plugin not ready (binding_id=%s)", bindingID)
			}
			if !contains(binding.Capabilities, capability) {
				return fmt.Errorf("authorize plugin failed: capability denied (binding_id=%s, capability=%s)", bindingID, capability)
			}
			if !pluginruntime.ScopeAllows(binding.TenantScope, tenantID) || !pluginruntime.ScopeAllows(binding.ModelScope, model) {
				return fmt.Errorf("authorize plugin failed: scope denied (binding_id=%s, tenant_id=%s, model=%s)", bindingID, tenantID, model)
			}
			return nil
		}
	}
	return fmt.Errorf("authorize plugin failed: binding not found (binding_id=%s)", bindingID)
}

func validateManifest(m *pluginruntime.Manifest) error {
	if strings.TrimSpace(m.PluginID) == "" || strings.TrimSpace(m.PluginVersion) == "" {
		return fmt.Errorf("plugin_id and plugin_version are required")
	}
	if m.GatewayCompatibility.APIContract != pluginruntime.SupportedAPIContract {
		return fmt.Errorf("unsupported api contract %q", m.GatewayCompatibility.APIContract)
	}
	if strings.TrimSpace(m.Runtime.Entrypoint) == "" {
		return fmt.Errorf("runtime entrypoint is required")
	}
	return nil
}

func (l *PluginLoader) snapshotManifests() []*pluginruntime.Manifest {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*pluginruntime.Manifest, 0, len(l.manifests))
	for _, manifest := range l.manifests {
		out = append(out, manifest)
	}
	return out
}

func cloneManifest(m *pluginruntime.Manifest) *pluginruntime.Manifest {
	clone := *m
	clone.Bindings = append([]pluginruntime.PluginBinding(nil), m.Bindings...)
	for i := range clone.Bindings {
		clone.Bindings[i].Capabilities = append([]string(nil), m.Bindings[i].Capabilities...)
		clone.Bindings[i].TenantScope = append([]string(nil), m.Bindings[i].TenantScope...)
		clone.Bindings[i].ModelScope = append([]string(nil), m.Bindings[i].ModelScope...)
	}
	return &clone
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func pluginID(m *pluginruntime.Manifest) string {
	if m == nil {
		return "unknown"
	}
	return m.PluginID
}
