package admin

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestDetectCircularDependencies_NoCycle(t *testing.T) {
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{}},
		{Key: "b", Dependencies: []ModuleDependency{{Key: "a"}}},
		{Key: "c", Dependencies: []ModuleDependency{{Key: "b"}}},
	}

	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("expected no cycle, got: %v", err)
	}
}

func TestDetectCircularDependencies_SimpleCycle(t *testing.T) {
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "b"}}},
		{Key: "b", Dependencies: []ModuleDependency{{Key: "a"}}},
	}

	err := detectCircularDependencies(modules)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "a") || !strings.Contains(errMsg, "b") {
		t.Errorf("expected cycle path to mention both 'a' and 'b', got: %s", errMsg)
	}
}

func TestDetectCircularDependencies_ThreeNodeCycle(t *testing.T) {
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "b"}}},
		{Key: "b", Dependencies: []ModuleDependency{{Key: "c"}}},
		{Key: "c", Dependencies: []ModuleDependency{{Key: "a"}}},
	}

	err := detectCircularDependencies(modules)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %s", errMsg)
	}
}

func TestDetectCircularDependencies_SelfCycle(t *testing.T) {
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "a"}}},
	}

	err := detectCircularDependencies(modules)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "a") {
		t.Errorf("expected cycle path to mention 'a', got: %s", errMsg)
	}
}

func TestDetectCircularDependencies_ComplexGraph(t *testing.T) {
	// Diamond dependency (not a cycle)
	//     a
	//    / \
	//   b   c
	//    \ /
	//     d
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "b"}, {Key: "c"}}},
		{Key: "b", Dependencies: []ModuleDependency{{Key: "d"}}},
		{Key: "c", Dependencies: []ModuleDependency{{Key: "d"}}},
		{Key: "d", Dependencies: []ModuleDependency{}},
	}

	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("expected no cycle in diamond graph, got: %v", err)
	}
}

func TestDetectCircularDependencies_WithCycleInSubgraph(t *testing.T) {
	// a -> b (ok)
	// c -> d -> e -> c (cycle)
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "b"}}},
		{Key: "b", Dependencies: []ModuleDependency{}},
		{Key: "c", Dependencies: []ModuleDependency{{Key: "d"}}},
		{Key: "d", Dependencies: []ModuleDependency{{Key: "e"}}},
		{Key: "e", Dependencies: []ModuleDependency{{Key: "c"}}},
	}

	err := detectCircularDependencies(modules)
	if err == nil {
		t.Fatal("expected cycle error in disconnected subgraph, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %s", errMsg)
	}
}

func TestDetectCircularDependencies_NonexistentDependency(t *testing.T) {
	// Module depends on a module that doesn't exist - should not crash
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{{Key: "nonexistent"}}},
		{Key: "b", Dependencies: []ModuleDependency{{Key: "a"}}},
	}

	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("expected no cycle (nonexistent deps ignored), got: %v", err)
	}
}

func TestAllModuleDefinitions_NoCycles(t *testing.T) {
	// This test verifies that the actual module definitions don't have cycles
	// If it panics, there's a real cycle in the production module graph

	settings.Global = settings.NewRegistry()

	// Should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("allModuleDefinitions panicked with cycle: %v", r)
		}
	}()

	modules := allModuleDefinitions()

	if len(modules) == 0 {
		t.Fatal("expected non-empty module list")
	}

	// Verify detectCircularDependencies was called (indirect check)
	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("production module graph contains cycle: %v", err)
	}
}

func TestDetectCircularDependencies_EmptyGraph(t *testing.T) {
	modules := []ModuleDefinition{}

	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("expected no error for empty graph, got: %v", err)
	}
}

func TestDetectCircularDependencies_SingleNode(t *testing.T) {
	modules := []ModuleDefinition{
		{Key: "a", Dependencies: []ModuleDependency{}},
	}

	err := detectCircularDependencies(modules)
	if err != nil {
		t.Errorf("expected no error for single node, got: %v", err)
	}
}
