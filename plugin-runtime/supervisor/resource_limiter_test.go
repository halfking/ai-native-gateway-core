package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
)

func TestNewResourceLimiter(t *testing.T) {
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "256Mi",
		},
		CPU: &discovery.ResourceQuantity{
			Limit: "25",
		},
		MaxProcesses: 10,
	}

	rl, err := NewResourceLimiter("test-plugin", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}

	if rl.pluginName != "test-plugin" {
		t.Errorf("Expected plugin name 'test-plugin', got %s", rl.pluginName)
	}

	// Check mode based on OS
	if runtime.GOOS == "linux" {
		// On Linux, expect either cgroup or monitor mode
		if rl.mode != ModeCgroup && rl.mode != ModeMonitor {
			t.Errorf("Expected cgroup or monitor mode on Linux, got %s", rl.mode)
		}
	} else {
		// On non-Linux (macOS, etc), expect monitor mode
		if rl.mode != ModeMonitor {
			t.Errorf("Expected monitor mode on %s, got %s", runtime.GOOS, rl.mode)
		}
	}

	// Cleanup
	if err := rl.Cleanup(); err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"256Mi", 256 * 1024 * 1024, false},
		// F3（2026-11-28 审计）：与 limiter 包语义对齐，MB/GB 统一 1024 进制
		{"512MB", 512 * 1024 * 1024, false},
		{"1Gi", 1024 * 1024 * 1024, false},
		{"2GB", 2 * 1024 * 1024 * 1024, false},
		{"1024Ki", 1024 * 1024, false},
		{"1048576", 1048576, false}, // bytes
		{"512M", 512 * 1024 * 1024, false}, // 单字母后缀（旧版误解析成 512 字节）
		{"1G", 1024 * 1024 * 1024, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseMemory(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMemory(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		wantErr  bool
	}{
		{"25", 25, false},
		{"50", 50, false},
		{"100", 100, false},
		// F5（2026-11-28 审计）：0% → cpu.max quota=0 = 内核 unlimited，拒绝
		{"0", 0, true},
		{"0%", 0, true},
		{"25%", 25, false},
		{"50%", 50, false},
		{"101", 0, true},  // > 100
		{"-1", 0, true},   // < 0
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseCPU(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCPU(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("parseCPU(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestApply(t *testing.T) {
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "256Mi",
		},
		CPU: &discovery.ResourceQuantity{
			Limit: "25",
		},
		MaxProcesses: 10,
	}

	rl, err := NewResourceLimiter("test-plugin", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer rl.Cleanup()

	// Create a dummy command (don't actually start it)
	cmd := exec.Command("sleep", "1")

	// Apply should not fail
	if err := rl.Apply(cmd); err != nil {
		t.Errorf("Apply failed: %v", err)
	}
}

func TestAssignProcessMonitorMode(t *testing.T) {
	// This test works on all platforms (monitor mode)
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "256Mi",
		},
		CPU: &discovery.ResourceQuantity{
			Limit: "25",
		},
		MaxProcesses: 10,
	}

	rl, err := NewResourceLimiter("test-plugin", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer rl.Cleanup()

	// Start a real process for testing
	cmd := exec.Command("sleep", "2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	defer cmd.Process.Kill()

	pid := cmd.Process.Pid

	// Assign process
	if err := rl.AssignProcess(pid); err != nil {
		t.Errorf("AssignProcess failed: %v", err)
	}

	// For monitor mode, give it time to collect stats
	if rl.mode == ModeMonitor {
		time.Sleep(100 * time.Millisecond)

		// Check if monitoring is working (stats may be nil initially)
		stats := rl.GetStats()
		// Stats might be nil if not collected yet, which is okay
		if stats != nil {
			t.Logf("Collected stats: Memory=%d bytes, CPU=%.1f%%, Threads=%d",
				stats.MemoryBytes, stats.CPUPercent, stats.NumThreads)
		}
	}

	// Wait for process to finish or kill it
	cmd.Process.Kill()
	cmd.Wait()
}

func TestResourceLimiterCleanup(t *testing.T) {
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "256Mi",
		},
	}

	rl, err := NewResourceLimiter("test-plugin-cleanup", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}

	// Cleanup should succeed
	if err := rl.Cleanup(); err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}

	// Second cleanup should also succeed (idempotent)
	if err := rl.Cleanup(); err != nil {
		t.Errorf("Second cleanup failed: %v", err)
	}
}

// TestCgroupCreation tests cgroup directory creation on Linux
func TestCgroupCreation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping cgroup test on non-Linux platform")
	}

	// Check if we have permission to create cgroups
	testPath := "/sys/fs/cgroup/llm-gateway-plugins-test"
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Skipf("Cannot create test cgroup (permission denied or cgroup not available): %v", err)
	}
	defer os.RemoveAll(testPath)

	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "128Mi",
		},
	}

	rl, err := NewResourceLimiter("test-cgroup-plugin", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer rl.Cleanup()

	if rl.mode == ModeCgroup {
		// Verify cgroup directory was created
		if _, err := os.Stat(rl.cgroupPath); os.IsNotExist(err) {
			t.Errorf("Cgroup directory was not created: %s", rl.cgroupPath)
		}
	}
}

// TestMemoryLimitEnforcement is an integration test that verifies memory limit enforcement
// This test is Linux-only and requires root or CAP_SYS_ADMIN
func TestMemoryLimitEnforcement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping memory limit enforcement test on non-Linux platform")
	}

	if os.Getuid() != 0 {
		t.Skip("Skipping memory limit test (requires root)")
	}

	// Create limiter with tight memory limit
	limits := discovery.ResourceLimits{
		Memory: &discovery.ResourceQuantity{
			Limit: "10Mi", // Very low limit to trigger OOM quickly
		},
	}

	rl, err := NewResourceLimiter("test-oom-plugin", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer rl.Cleanup()

	if rl.mode != ModeCgroup {
		t.Skip("Cgroup mode not available, skipping OOM test")
	}

	// Start a process that allocates memory
	// This Go program allocates 20MB (exceeds 10Mi limit)
	cmd := exec.Command("sh", "-c", `
		dd if=/dev/zero of=/dev/null bs=1M count=20
	`)

	if err := rl.Apply(cmd); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}

	if err := rl.AssignProcess(cmd.Process.Pid); err != nil {
		t.Fatalf("AssignProcess failed: %v", err)
	}

	// Wait for process (should be OOM killed)
	err = cmd.Wait()
	
	// We expect the process to be killed (non-zero exit)
	if err == nil {
		t.Error("Expected process to be killed by OOM, but it succeeded")
	} else {
		t.Logf("Process was terminated as expected: %v", err)
	}
}
