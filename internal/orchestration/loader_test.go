package orchestration

import (
	"context"
	"errors"
	"testing"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

type fakeProbe struct {
	handshakeErr error
	healthErr    error
	handshakes   int
	healthChecks int
}

func (p *fakeProbe) Handshake(context.Context, *pluginruntime.Manifest) error {
	p.handshakes++
	return p.handshakeErr
}

func (p *fakeProbe) Health(context.Context, *pluginruntime.Manifest) error {
	p.healthChecks++
	return p.healthErr
}

func testManifest() *pluginruntime.Manifest {
	return &pluginruntime.Manifest{
		PluginID:      "planner",
		PluginVersion: "1.0.0",
		GatewayCompatibility: pluginruntime.GatewayCompatibility{
			APIContract: pluginruntime.SupportedAPIContract,
		},
		Runtime: pluginruntime.Runtime{Entrypoint: "bin/planner"},
		Bindings: []pluginruntime.PluginBinding{{
			BindingID:        "planner.request",
			Phase:            pluginruntime.PhaseRequest,
			ExecutionMode:    pluginruntime.ExecutionSequential,
			Capabilities:     []string{pluginruntime.CapabilityRequestObserve},
			TimeoutMillis:    1000,
			ConcurrencyLimit: 1,
			FailurePolicy:    pluginruntime.FailureClosed,
			DTOProfile:       pluginruntime.DTOProfileSummary,
			Enabled:          true,
		}},
	}
}

func TestPluginLoaderRequiresHandshakeAndHealthBeforeReady(t *testing.T) {
	probe := &fakeProbe{}
	loader := NewPluginLoader(PluginLoaderConfig{Probe: probe})

	if err := loader.Load(context.Background(), testManifest()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loader.Ready("planner") {
		t.Fatal("plugin should be ready after handshake and health check")
	}
	if probe.handshakes != 1 || probe.healthChecks != 1 {
		t.Fatalf("probe calls = %d/%d, want 1/1", probe.handshakes, probe.healthChecks)
	}
}

func TestPluginLoaderFailsClosedOnHandshakeFailure(t *testing.T) {
	probe := &fakeProbe{handshakeErr: errors.New("handshake unavailable")}
	loader := NewPluginLoader(PluginLoaderConfig{Probe: probe})

	if err := loader.Load(context.Background(), testManifest()); err == nil {
		t.Fatal("Load() should fail when handshake fails")
	}
	if loader.Ready("planner") {
		t.Fatal("failed plugin must not be ready")
	}
}

func TestPluginLoaderFailsClosedOnHealthFailure(t *testing.T) {
	probe := &fakeProbe{healthErr: errors.New("health unavailable")}
	loader := NewPluginLoader(PluginLoaderConfig{Probe: probe})

	if err := loader.Load(context.Background(), testManifest()); err == nil {
		t.Fatal("Load() should fail when health check fails")
	}
	if loader.Ready("planner") {
		t.Fatal("unhealthy plugin must not be ready")
	}
	if probe.handshakes != 1 || probe.healthChecks != 1 {
		t.Fatalf("probe calls = %d/%d, want 1/1", probe.handshakes, probe.healthChecks)
	}
}

func TestPluginLoaderRejectsWriteCapabilityWithoutSandbox(t *testing.T) {
	manifest := testManifest()
	manifest.Bindings[0].Capabilities = []string{pluginruntime.CapabilityRequestMutate}
	loader := NewPluginLoader(PluginLoaderConfig{Probe: &fakeProbe{}})

	if err := loader.Load(context.Background(), manifest); err == nil {
		t.Fatal("Load() should reject write capability without sandbox")
	}
}

func TestPluginLoaderAuthorizeChecksReadyCapabilityAndScope(t *testing.T) {
	manifest := testManifest()
	manifest.Bindings[0].TenantScope = []string{"tenant-a"}
	loader := NewPluginLoader(PluginLoaderConfig{Probe: &fakeProbe{}})
	if err := loader.Load(context.Background(), manifest); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if err := loader.Authorize("planner.request", pluginruntime.CapabilityRequestObserve, "tenant-a", ""); err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if err := loader.Authorize("planner.request", pluginruntime.CapabilityRequestBlock, "tenant-a", ""); err == nil {
		t.Fatal("Authorize() should reject undeclared capability")
	}
	if err := loader.Authorize("planner.request", pluginruntime.CapabilityRequestObserve, "tenant-b", ""); err == nil {
		t.Fatal("Authorize() should reject tenant outside scope")
	}
}
