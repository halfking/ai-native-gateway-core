package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
)

// MockEventBus implements EventBus for testing.
type MockEventBus struct {
	events []Event
}

func (m *MockEventBus) Publish(ctx context.Context, event Event) error {
	m.events = append(m.events, event)
	return nil
}

func TestSupervisor_Start(t *testing.T) {
	eventBus := &MockEventBus{}
	s := NewSupervisor(eventBus)

	// Create a simple manifest for /bin/sleep
	manifest := &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "test-plugin",
			Version: "1.0.0",
		},
		Spec: discovery.Spec{
			Type:       discovery.TypeExternal,
			Executable: "/bin/sleep",
			Args:       []string{"10"},
			ExtensionPoints: []discovery.ExtensionPointDecl{
				{Name: "test", Phase: "routing", Interface: "Test"},
			},
			Resources: discovery.ResourceLimits{},
		},
	}

	ctx := context.Background()

	// Start plugin
	if err := s.Start(ctx, manifest); err != nil {
		t.Fatalf("failed to start plugin: %v", err)
	}

	// Verify it's running
	state, err := s.StateOf("test-plugin")
	if err != nil {
		t.Fatalf("failed to get state: %v", err)
	}

	if state.Status != StatusRunning {
		t.Errorf("expected status Running, got %s", state.Status)
	}

	if state.PID <= 0 {
		t.Errorf("expected valid PID, got %d", state.PID)
	}

	// Stop plugin
	if err := s.Stop(ctx, "test-plugin"); err != nil {
		t.Fatalf("failed to stop plugin: %v", err)
	}

	// Verify event was published
	if len(eventBus.events) == 0 {
		t.Error("expected at least one event")
	}
}

func TestSupervisor_Restart(t *testing.T) {
	eventBus := &MockEventBus{}
	s := NewSupervisor(eventBus)

	manifest := &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "restart-test",
			Version: "1.0.0",
		},
		Spec: discovery.Spec{
			Type:       discovery.TypeExternal,
			Executable: "/bin/sleep",
			Args:       []string{"60"},
			ExtensionPoints: []discovery.ExtensionPointDecl{
				{Name: "test", Phase: "routing", Interface: "Test"},
			},
			Resources: discovery.ResourceLimits{},
		},
	}

	ctx := context.Background()

	// Start
	if err := s.Start(ctx, manifest); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	originalPID := func() int {
		state, _ := s.StateOf("restart-test")
		return state.PID
	}()

	// Restart
	if err := s.Restart(ctx, "restart-test"); err != nil {
		t.Fatalf("failed to restart: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Verify new PID
	state, _ := s.StateOf("restart-test")
	if state.PID == originalPID {
		t.Error("expected different PID after restart")
	}

	// Cleanup
	s.Stop(ctx, "restart-test")
}

func TestResourceLimiter_ParseMemory(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"256Mi", 256 * 1024 * 1024},
		{"512MB", 512 * 1000 * 1000},
		{"1Gi", 1 * 1024 * 1024 * 1024},
		{"2GB", 2 * 1000 * 1000 * 1000},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseMemory(tt.input)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestResourceLimiter_ParseCPU(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"25", 25},
		{"50", 50},
		{"100", 100},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseCPU(tt.input)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestResourceLimiter_Apply(t *testing.T) {
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "256Mi",
		},
		CPU: &discovery.ResourceQuantity{
			Limit: "50",
		},
		MaxProcesses: 10,
	}

	limiter, err := NewResourceLimiter("test-limiter", limits)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	// Create a dummy command
	cmd := &exec.Cmd{
		Path: "/bin/echo",
		Args: []string{"test"},
	}

	// Apply limits (may fail on systems without cgroup v2)
	if err := limiter.Apply(cmd); err != nil {
		t.Logf("WARN: apply failed (cgroup might not be available): %v", err)
	}

	// Cleanup
	if limiter.cgroupPath != "" {
		os.RemoveAll(filepath.Dir(limiter.cgroupPath))
	}
}

func TestSupervisor_HealthCheck(t *testing.T) {
	eventBus := &MockEventBus{}
	s := NewSupervisor(eventBus)

	manifest := &discovery.Manifest{
		Metadata: discovery.Metadata{
			Name:    "health-test",
			Version: "1.0.0",
		},
		Spec: discovery.Spec{
			Type:       discovery.TypeExternal,
			Executable: "/bin/sleep",
			Args:       []string{"30"},
			ExtensionPoints: []discovery.ExtensionPointDecl{
				{Name: "test", Phase: "routing", Interface: "Test"},
			},
			Resources: discovery.ResourceLimits{},
		},
	}

	ctx := context.Background()

	// Start
	if err := s.Start(ctx, manifest); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer s.Stop(ctx, "health-test")

	// Health check
	health, err := s.HealthCheck(ctx, "health-test")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	if !health.Healthy {
		t.Error("expected healthy status")
	}
}
