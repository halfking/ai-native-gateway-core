package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

func TestAuditRunningVersionIsObservable(t *testing.T) {
	sup := newMockSupervisor()
	sup.processes["plugin1"] = &supervisor.ProcessState{PluginID: "plugin1", Status: supervisor.StatusRunning, Version: "1.0.0"}
	reg := newMockRegistry()
	reg.plugins["plugin1"] = &RegistryPlugin{ID: "plugin1", Version: "2.0.0", Enabled: true}
	r := NewReconciler(&Config{}, sup, reg).(*reconciler)
	actual, err := r.getActualStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if actual["plugin1"].Version == "" {
		t.Fatal("running process has no observable version; upgrade comparison is unreachable")
	}
}

type waitingRegistry struct {
	*mockRegistry
	entered chan struct{}
}

func (r *waitingRegistry) List(ctx context.Context, _ RegistryFilter) ([]*RegistryPlugin, error) {
	close(r.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestStopCancelsInitialReconcile(t *testing.T) {
	reg := &waitingRegistry{newMockRegistry(), make(chan struct{})}
	r := NewReconciler(&Config{}, newMockSupervisor(), reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan error, 1)
	stopped := make(chan error, 1)
	go func() { started <- r.Start(ctx) }()
	<-reg.entered
	go func() { stopped <- r.Stop() }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		cancel()
		<-stopped
		<-started
		t.Fatal("Stop cannot cancel initial reconciliation")
	}
	if err := <-started; !errors.Is(err, context.Canceled) {
		t.Fatalf("start error=%v", err)
	}
}

func TestDisableReportsStopFailure(t *testing.T) {
	sup := newMockSupervisor()
	stopErr := errors.New("stop failed")
	sup.stopErr = stopErr
	sup.processes["plugin1"] = &supervisor.ProcessState{PluginID: "plugin1", Status: supervisor.StatusCrashed}
	reg := newMockRegistry()
	reg.plugins["plugin1"] = &RegistryPlugin{ID: "plugin1", Enabled: true}
	r := NewReconciler(&Config{MaxCrashCount: 1}, sup, reg)
	_ = r.Reconcile(context.Background())
	if err := r.Reconcile(context.Background()); !errors.Is(err, stopErr) {
		t.Fatalf("disable lost stop error: %v", err)
	}
	if reg.plugins["plugin1"].Enabled {
		t.Fatal("disable was not applied after stop error")
	}
}

func TestReconcileUpgradesThroughPublicCycle(t *testing.T) {
	sup := newMockSupervisor()
	sup.processes["plugin1"] = &supervisor.ProcessState{PluginID: "plugin1", Status: supervisor.StatusRunning, Version: "1.0.0"}
	reg := newMockRegistry()
	reg.plugins["plugin1"] = &RegistryPlugin{ID: "plugin1", Version: "2.0.0", Enabled: true}
	reg.manifests["plugin1"] = &discovery.Manifest{Metadata: discovery.Metadata{Name: "plugin1", Version: "2.0.0"}}
	old := sup.processes["plugin1"]
	r := NewReconciler(&Config{}, sup, reg)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sup.processes["plugin1"] == old {
		t.Fatal("version mismatch did not replace running process")
	}
	if sup.processes["plugin1"].Version != "2.0.0" {
		t.Fatal("wrong version started")
	}
	upgraded := sup.processes["plugin1"]
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sup.processes["plugin1"] != upgraded {
		t.Fatal("stable version upgraded again")
	}
}

func TestAuditReconcileReportsFailedStart(t *testing.T) {
	sup := newMockSupervisor()
	sup.startErr = errors.New("process failed to start")
	reg := newMockRegistry()
	reg.plugins["plugin1"] = &RegistryPlugin{ID: "plugin1", Version: "1.0.0", Enabled: true}
	reg.manifests["plugin1"] = &discovery.Manifest{Metadata: discovery.Metadata{Name: "plugin1", Version: "1.0.0"}}
	r := NewReconciler(&Config{}, sup, reg)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile returned nil even though the desired process could not start")
	}
}
