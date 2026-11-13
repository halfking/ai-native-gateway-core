package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// Mock implementations

type mockSupervisor struct {
	processes map[string]*supervisor.ProcessState
	startErr  error
	stopErr   error
}

func newMockSupervisor() *mockSupervisor {
	return &mockSupervisor{
		processes: make(map[string]*supervisor.ProcessState),
	}
}

func (m *mockSupervisor) Start(ctx context.Context, manifest *discovery.Manifest) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.processes[manifest.Metadata.Name] = &supervisor.ProcessState{
		PluginID: manifest.Metadata.Name,
		Status:   supervisor.StatusRunning,
	}
	return nil
}

func (m *mockSupervisor) Stop(ctx context.Context, pluginID string) error {
	if m.stopErr != nil {
		return m.stopErr
	}
	if proc, exists := m.processes[pluginID]; exists {
		proc.Status = supervisor.StatusStopped
	}
	return nil
}

func (m *mockSupervisor) Restart(ctx context.Context, pluginID string) error {
	if err := m.Stop(ctx, pluginID); err != nil {
		return err
	}
	return nil
}

func (m *mockSupervisor) StateOf(pluginID string) (*supervisor.ProcessState, error) {
	if proc, exists := m.processes[pluginID]; exists {
		return proc, nil
	}
	return nil, errors.New("plugin not found")
}

func (m *mockSupervisor) ListAll() []*supervisor.ProcessState {
	var result []*supervisor.ProcessState
	for _, proc := range m.processes {
		result = append(result, proc)
	}
	return result
}

type mockRegistry struct {
	plugins   map[string]*RegistryPlugin
	manifests map[string]*discovery.Manifest
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{
		plugins:   make(map[string]*RegistryPlugin),
		manifests: make(map[string]*discovery.Manifest),
	}
}

func (m *mockRegistry) List(ctx context.Context, filter RegistryFilter) ([]*RegistryPlugin, error) {
	var result []*RegistryPlugin
	for _, p := range m.plugins {
		if filter.EnabledOnly && !p.Enabled {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (m *mockRegistry) UpdateVersion(ctx context.Context, pluginID string, version string) error {
	if p, exists := m.plugins[pluginID]; exists {
		p.Version = version
		if manifest, ok := m.manifests[pluginID]; ok {
			manifest.Metadata.Version = version
		}
		return nil
	}
	return errors.New("plugin not found")
}

func (m *mockRegistry) UpdateStatus(ctx context.Context, pluginID string, status PluginStatus) error {
	if p, exists := m.plugins[pluginID]; exists {
		p.Status = status
		p.Enabled = (status == StatusEnabled)
		return nil
	}
	return errors.New("plugin not found")
}

func (m *mockRegistry) GetManifest(ctx context.Context, pluginID string) (*discovery.Manifest, error) {
	if manifest, exists := m.manifests[pluginID]; exists {
		return manifest, nil
	}
	return nil, errors.New("manifest not found")
}

// Test cases

func TestReconcile_PluginNotRunning(t *testing.T) {
	// Setup
	sup := newMockSupervisor()
	reg := newMockRegistry()

	// Add a desired plugin to registry
	reg.plugins["plugin1"] = &RegistryPlugin{
		ID:      "plugin1",
		Name:    "plugin1",
		Version: "1.0.0",
		Enabled: true,
	}
	reg.manifests["plugin1"] = &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "plugin1",
			Version: "1.0.0",
		},
	}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	// Execute
	ctx := context.Background()
	err := r.Reconcile(ctx)

	// Verify
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Plugin should have been started
	if _, exists := sup.processes["plugin1"]; !exists {
		t.Error("Expected plugin1 to be started")
	}
}

func TestReconcile_PluginCrashed(t *testing.T) {
	// Setup
	sup := newMockSupervisor()
	reg := newMockRegistry()

	// Add plugin to registry
	reg.plugins["plugin1"] = &RegistryPlugin{
		ID:      "plugin1",
		Name:    "plugin1",
		Version: "1.0.0",
		Enabled: true,
	}
	reg.manifests["plugin1"] = &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "plugin1",
			Version: "1.0.0",
		},
	}

	// Plugin is crashed in supervisor
	sup.processes["plugin1"] = &supervisor.ProcessState{
		PluginID: "plugin1",
		Status:   supervisor.StatusCrashed,
	}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	// Execute
	ctx := context.Background()
	err := r.Reconcile(ctx)

	// Verify
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Crash should have been recorded
	rec := r.(*reconciler)
	crashCount := rec.crashTracker.GetCount("plugin1")
	if crashCount != 1 {
		t.Errorf("Expected crash count 1, got %d", crashCount)
	}
}

func TestReconcile_PluginVersionMismatch(t *testing.T) {
	// Setup
	sup := newMockSupervisor()
	reg := newMockRegistry()

	// Registry has v2.0.0
	reg.plugins["plugin1"] = &RegistryPlugin{
		ID:      "plugin1",
		Name:    "plugin1",
		Version: "2.0.0",
		Enabled: true,
	}
	reg.manifests["plugin1"] = &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "plugin1",
			Version: "2.0.0",
		},
	}

	// Supervisor has v1.0.0 running
	sup.processes["plugin1"] = &supervisor.ProcessState{
		PluginID: "plugin1",
		Status:   supervisor.StatusRunning,
	}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	// Get desired and actual states
	ctx := context.Background()
	rec := r.(*reconciler)

	desired, err := rec.getDesiredStates(ctx)
	if err != nil {
		t.Fatalf("getDesiredStates failed: %v", err)
	}

	actual, err := rec.getActualStates(ctx)
	if err != nil {
		t.Fatalf("getActualStates failed: %v", err)
	}

	// Add version to actual state for testing
	actual["plugin1"].Version = "1.0.0"

	// Compute actions
	actions := rec.computeActions(desired, actual)

	// Verify upgrade action is generated
	foundUpgrade := false
	for _, action := range actions {
		if action.Action == ActionUpgrade && action.PluginID == "plugin1" {
			foundUpgrade = true
			if action.OldVersion != "1.0.0" || action.NewVersion != "2.0.0" {
				t.Errorf("Expected upgrade from 1.0.0 to 2.0.0, got %s to %s",
					action.OldVersion, action.NewVersion)
			}
		}
	}

	if !foundUpgrade {
		t.Error("Expected upgrade action for version mismatch")
	}
}

func TestReconcile_ContinuousCrash(t *testing.T) {
	// Setup
	sup := newMockSupervisor()
	reg := newMockRegistry()

	reg.plugins["plugin1"] = &RegistryPlugin{
		ID:      "plugin1",
		Name:    "plugin1",
		Version: "1.0.0",
		Enabled: true,
	}
	reg.manifests["plugin1"] = &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "plugin1",
			Version: "1.0.0",
		},
	}

	sup.processes["plugin1"] = &supervisor.ProcessState{
		PluginID: "plugin1",
		Status:   supervisor.StatusCrashed,
	}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    3, // Low threshold for testing
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)
	rec := r.(*reconciler)

	ctx := context.Background()

	// Simulate 3 crashes
	for i := 0; i < 3; i++ {
		rec.crashTracker.RecordCrash("plugin1")
	}

	// Now reconcile should disable the plugin
	err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify plugin is disabled
	if reg.plugins["plugin1"].Status != StatusDisabled {
		t.Error("Expected plugin1 to be disabled after continuous crashes")
	}
}

func TestCrashTracker_ResetWindow(t *testing.T) {
	resetWindow := 100 * time.Millisecond
	ct := NewCrashTracker(resetWindow)

	// Record first crash
	ct.RecordCrash("plugin1")
	if ct.GetCount("plugin1") != 1 {
		t.Errorf("Expected count 1, got %d", ct.GetCount("plugin1"))
	}

	// Record second crash immediately
	ct.RecordCrash("plugin1")
	if ct.GetCount("plugin1") != 2 {
		t.Errorf("Expected count 2, got %d", ct.GetCount("plugin1"))
	}

	// Wait for reset window to pass
	time.Sleep(150 * time.Millisecond)

	// Count should be reset
	if ct.GetCount("plugin1") != 0 {
		t.Errorf("Expected count 0 after reset window, got %d", ct.GetCount("plugin1"))
	}

	// New crash should start new window
	ct.RecordCrash("plugin1")
	if ct.GetCount("plugin1") != 1 {
		t.Errorf("Expected count 1 in new window, got %d", ct.GetCount("plugin1"))
	}
}

func TestCrashTracker_Reset(t *testing.T) {
	ct := NewCrashTracker(1 * time.Hour)

	// Record crashes
	ct.RecordCrash("plugin1")
	ct.RecordCrash("plugin1")
	ct.RecordCrash("plugin2")

	// Reset plugin1
	ct.Reset("plugin1")

	// Verify
	if ct.GetCount("plugin1") != 0 {
		t.Errorf("Expected count 0 after reset, got %d", ct.GetCount("plugin1"))
	}
	if ct.GetCount("plugin2") != 1 {
		t.Errorf("Expected plugin2 count 1, got %d", ct.GetCount("plugin2"))
	}
}

func TestStateTransition(t *testing.T) {
	tests := []struct {
		from  State
		to    State
		valid bool
	}{
		{StatePending, StateStarting, true},
		{StateStarting, StateRunning, true},
		{StateRunning, StateFailed, true},
		{StateFailed, StateStarting, true},
		{StateFailed, StateDisabled, true},
		{StateRunning, StateDisabled, false}, // Invalid transition
		{StatePending, StateRunning, false},  // Invalid transition
	}

	for _, tt := range tests {
		result := IsValidTransition(tt.from, tt.to)
		if result != tt.valid {
			t.Errorf("IsValidTransition(%s, %s) = %v, want %v",
				tt.from, tt.to, result, tt.valid)
		}
	}
}

func TestReconcile_PluginNotDesired(t *testing.T) {
	// Setup
	sup := newMockSupervisor()
	reg := newMockRegistry()

	// Plugin is running but not in registry (not desired)
	sup.processes["plugin1"] = &supervisor.ProcessState{
		PluginID: "plugin1",
		Status:   supervisor.StatusRunning,
	}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	// Execute
	ctx := context.Background()
	err := r.Reconcile(ctx)

	// Verify
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Plugin should have been stopped
	proc := sup.processes["plugin1"]
	if proc.Status != supervisor.StatusStopped {
		t.Errorf("Expected plugin1 to be stopped, got status %s", proc.Status)
	}
}

func TestMapProcessStatus(t *testing.T) {
	tests := []struct {
		input    supervisor.ProcessStatus
		expected State
	}{
		{supervisor.StatusStarting, StateStarting},
		{supervisor.StatusRunning, StateRunning},
		{supervisor.StatusStopping, StateStopping},
		{supervisor.StatusStopped, StateStopped},
		{supervisor.StatusCrashed, StateFailed},
	}

	for _, tt := range tests {
		result := mapProcessStatus(tt.input)
		if result != tt.expected {
			t.Errorf("mapProcessStatus(%s) = %s, want %s",
				tt.input, result, tt.expected)
		}
	}
}

func TestReconciler_StartStop(t *testing.T) {
	sup := newMockSupervisor()
	reg := newMockRegistry()

	config := &Config{
		Interval:         100 * time.Millisecond,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	ctx := context.Background()

	// Start reconciler
	err := r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Let it run for a bit
	time.Sleep(250 * time.Millisecond)

	// Stop reconciler
	err = r.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestCrashTracker_Cleanup(t *testing.T) {
	resetWindow := 50 * time.Millisecond
	ct := NewCrashTracker(resetWindow)

	// Record crashes
	ct.RecordCrash("plugin1")
	ct.RecordCrash("plugin2")

	// Wait for window to expire
	time.Sleep(100 * time.Millisecond)

	// Cleanup
	ct.Cleanup()

	// Verify records are removed
	if len(ct.crashes) != 0 {
		t.Errorf("Expected no crash records after cleanup, got %d", len(ct.crashes))
	}
}

func TestGetTransitionAction(t *testing.T) {
	tests := []struct {
		from     State
		to       State
		expected Action
	}{
		{StatePending, StateStarting, ActionStart},
		{StateFailed, StateStarting, ActionRestart},
		{StateFailed, StateDisabled, ActionDisable},
		{StateRunning, StateStopping, ActionStop},
		{StateRunning, StateStarting, ActionUpgrade},
		{StateRunning, StateDisabled, ActionNone}, // Invalid
	}

	for _, tt := range tests {
		result := GetTransitionAction(tt.from, tt.to)
		if result != tt.expected {
			t.Errorf("GetTransitionAction(%s, %s) = %s, want %s",
				tt.from, tt.to, result, tt.expected)
		}
	}
}

func TestCanTransition(t *testing.T) {
	if !CanTransition(StatePending, StateStarting) {
		t.Error("Expected valid transition from pending to starting")
	}

	if CanTransition(StateRunning, StateDisabled) {
		t.Error("Expected invalid transition from running to disabled")
	}
}

func TestExecuteAction_Errors(t *testing.T) {
	sup := newMockSupervisor()
	reg := newMockRegistry()

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)
	rec := r.(*reconciler)

	ctx := context.Background()

	// Test start without manifest
	action := &ReconcileAction{
		PluginID: "nonexistent",
		Action:   ActionStart,
	}
	err := rec.executeAction(ctx, action)
	if err == nil {
		t.Error("Expected error when starting plugin without manifest")
	}

	// Test upgrade without manifest
	reg.plugins["plugin1"] = &RegistryPlugin{
		ID:      "plugin1",
		Name:    "plugin1",
		Version: "1.0.0",
		Enabled: true,
	}
	sup.processes["plugin1"] = &supervisor.ProcessState{
		PluginID: "plugin1",
		Status:   supervisor.StatusRunning,
	}

	action = &ReconcileAction{
		PluginID:   "plugin1",
		Action:     ActionUpgrade,
		OldVersion: "1.0.0",
		NewVersion: "2.0.0",
	}
	err = rec.executeAction(ctx, action)
	if err == nil {
		t.Error("Expected error when upgrading plugin without manifest")
	}

	// Test unknown action
	action = &ReconcileAction{
		PluginID: "plugin1",
		Action:   "unknown",
	}
	err = rec.executeAction(ctx, action)
	if err == nil {
		t.Error("Expected error for unknown action")
	}
}

func TestGetDesiredStates_Error(t *testing.T) {
	sup := newMockSupervisor()
	reg := &mockRegistryWithError{}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)
	rec := r.(*reconciler)

	ctx := context.Background()
	_, err := rec.getDesiredStates(ctx)
	if err == nil {
		t.Error("Expected error from registry")
	}
}

func TestReconcile_Errors(t *testing.T) {
	sup := newMockSupervisor()
	reg := &mockRegistryWithError{}

	config := &Config{
		Interval:         30 * time.Second,
		MaxCrashCount:    10,
		CrashResetWindow: 1 * time.Hour,
	}

	r := NewReconciler(config, sup, reg)

	ctx := context.Background()
	err := r.Reconcile(ctx)
	if err == nil {
		t.Error("Expected error during reconciliation")
	}
}

func TestConfigDefaults(t *testing.T) {
	sup := newMockSupervisor()
	reg := newMockRegistry()

	// Empty config
	config := &Config{}

	r := NewReconciler(config, sup, reg)
	rec := r.(*reconciler)

	// Check defaults
	if rec.config.Interval != 30*time.Second {
		t.Errorf("Expected default interval 30s, got %v", rec.config.Interval)
	}
	if rec.config.MaxCrashCount != 10 {
		t.Errorf("Expected default max crash count 10, got %d", rec.config.MaxCrashCount)
	}
	if rec.config.CrashResetWindow != 1*time.Hour {
		t.Errorf("Expected default reset window 1h, got %v", rec.config.CrashResetWindow)
	}
}

func TestFilterEnabled(t *testing.T) {
	filter := FilterEnabled()
	if !filter.EnabledOnly {
		t.Error("Expected EnabledOnly to be true")
	}
}

// Mock registry with error for testing error paths
type mockRegistryWithError struct{}

func (m *mockRegistryWithError) List(ctx context.Context, filter RegistryFilter) ([]*RegistryPlugin, error) {
	return nil, errors.New("registry error")
}

func (m *mockRegistryWithError) UpdateVersion(ctx context.Context, pluginID string, version string) error {
	return errors.New("registry error")
}

func (m *mockRegistryWithError) UpdateStatus(ctx context.Context, pluginID string, status PluginStatus) error {
	return errors.New("registry error")
}

func (m *mockRegistryWithError) GetManifest(ctx context.Context, pluginID string) (*discovery.Manifest, error) {
	return nil, errors.New("registry error")
}
