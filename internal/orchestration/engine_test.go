package orchestration

import (
	"context"
	"testing"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

func TestCoreOrchestratorLoadsPluginAndSchedulesAuthorizedAction(t *testing.T) {
	loader := NewPluginLoader(PluginLoaderConfig{Probe: &fakeProbe{}})
	scheduler := NewScheduler(SchedulerConfig{LeaseTTL: time.Minute})
	engine := NewCoreOrchestrator(loader, scheduler)
	manifest := testManifest()

	if err := engine.LoadPlugin(context.Background(), manifest); err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if err := engine.Schedule(context.Background(), Action{
		ID:            "a1",
		RunID:         "run-1",
		Sequence:      1,
		BindingID:     "planner.request",
		Capability:    pluginruntime.CapabilityRequestObserve,
		TenantID:      "tenant-a",
		FailurePolicy: pluginruntime.FailureClosed,
	}); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	claim, err := engine.Claim("run-1", "worker-1")
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim.Action.ID != "a1" {
		t.Fatalf("claimed action = %+v", claim.Action)
	}
}

func TestCoreOrchestratorRejectsUnauthorizedAction(t *testing.T) {
	loader := NewPluginLoader(PluginLoaderConfig{Probe: &fakeProbe{}})
	engine := NewCoreOrchestrator(loader, NewScheduler(SchedulerConfig{LeaseTTL: time.Minute}))
	if err := engine.LoadPlugin(context.Background(), testManifest()); err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if err := engine.Schedule(context.Background(), Action{
		ID:         "a1",
		RunID:      "run-1",
		Sequence:   1,
		BindingID:  "planner.request",
		Capability: pluginruntime.CapabilityRequestBlock,
		TenantID:   "tenant-a",
	}); err == nil {
		t.Fatal("Schedule() should reject undeclared capability")
	}
}
