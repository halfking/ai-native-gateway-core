package limiter

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestNewResourceLimiter(t *testing.T) {
	limits := ResourceLimits{
		Memory:       &MemoryLimit{Limit: "256Mi"},
		CPU:          &CPULimit{Limit: "25%"},
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
		// On Linux, expect either cgroup or monitor mode (cgroup may be
		// unavailable in containers → graceful fallback)
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
	if err := rl.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestNewResourceLimiterEmpty(t *testing.T) {
	// 未配置任何限制也应成功创建（全零值 = 仅 PID 默认限制 / 监控）
	rl, err := NewResourceLimiter("empty-plugin", ResourceLimits{})
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	if err := rl.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"256Mi", 256 * 1024 * 1024, false},
		{"512MB", 512 * 1024 * 1024, false},
		{"1Gi", 1024 * 1024 * 1024, false},
		{"2GB", 2 * 1024 * 1024 * 1024, false},
		{"128M", 128 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"1024Ki", 1024 * 1024, false},
		{"512K", 512 * 1024, false},
		{"1048576", 1048576, false}, // bytes
		{"invalid", 0, true},
		{"", 0, true},
		{"x", 0, true},
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
		{"50%", 50, false},
		{"100", 100, false},
		{"100%", 100, false},
		{"0", 0, false},
		{"101", 0, true},  // out of range
		{"-5", 0, true},   // negative
		{"abc", 0, true},  // not a number
		{"%50", 0, true},  // malformed
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

func TestApplyMonitorMode(t *testing.T) {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
			t.Skip("cgroup v2 available on this host; monitor-mode Apply test is exercised by TestNewResourceLimiter fallback")
		}
	}
	rl, err := NewResourceLimiter("apply-monitor", ResourceLimits{
		Memory: &MemoryLimit{Limit: "64M"},
	})
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer func() { _ = rl.Stop() }()

	// monitor 模式下 Apply 不做任何事且不报错
	cmd := exec.Command("true")
	if err := rl.Apply(cmd); err != nil {
		t.Errorf("Apply in monitor mode should be a no-op, got %v", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	rl, err := NewResourceLimiter("stop-twice", ResourceLimits{MaxProcesses: 5})
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	// monitor 模式下未 AssignProcess 就 Stop，也应安全
	if err := rl.Stop(); err != nil {
		t.Errorf("first Stop failed: %v", err)
	}
	if err := rl.Stop(); err != nil {
		t.Errorf("second Stop should be a no-op, got %v", err)
	}
}

// TestCgroupLimits verifies process assignment into a cgroup (Linux only).
func TestCgroupLimits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup tests require Linux")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("cgroupv2 not available")
	}

	limits := ResourceLimits{
		Memory: &MemoryLimit{Limit: "50M"},
	}

	rl, err := NewResourceLimiter("test-cgroup", limits)
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	defer func() { _ = rl.Stop() }()

	if rl.mode != ModeCgroup {
		t.Skip("cgroup directory could not be created (permission denied); monitor fallback active")
	}

	cmd := exec.Command("sleep", "60")
	if err := rl.Apply(cmd); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	if err := rl.AssignProcess(cmd.Process.Pid); err != nil {
		t.Fatalf("AssignProcess failed: %v", err)
	}
}
