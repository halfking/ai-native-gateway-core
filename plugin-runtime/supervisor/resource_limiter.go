package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
)

// ResourceLimiter manages cgroup-based resource limits for plugins.
type ResourceLimiter struct {
	pluginName string
	cgroupPath string
	limits     discovery.ResourceLimits
}

// NewResourceLimiter creates a new resource limiter.
func NewResourceLimiter(pluginName string, limits discovery.ResourceLimits) (*ResourceLimiter, error) {
	// Create cgroup path: /sys/fs/cgroup/llm-gateway-plugins/<plugin-name>
	cgroupPath := filepath.Join("/sys/fs/cgroup", "llm-gateway-plugins", pluginName)
	
	// Create cgroup directory (if not exists)
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		// If cgroup not supported, continue without limits
		fmt.Printf("WARN: failed to create cgroup %s: %v (resource limits disabled)\n", cgroupPath, err)
		return &ResourceLimiter{
			pluginName: pluginName,
			cgroupPath: "",
			limits:     limits,
		}, nil
	}
	
	return &ResourceLimiter{
		pluginName: pluginName,
		cgroupPath: cgroupPath,
		limits:     limits,
	}, nil
}

// Apply applies resource limits to a command.
func (r *ResourceLimiter) Apply(cmd *exec.Cmd) error {
	if r.cgroupPath == "" {
		// Cgroup not supported, skip
		return nil
	}
	
	// Apply memory limit
	if r.limits.Memory != nil && r.limits.Memory.Limit != "" {
		memoryBytes, err := parseMemory(r.limits.Memory.Limit)
		if err != nil {
			return fmt.Errorf("failed to parse memory limit: %w", err)
		}
		
		if err := r.writeCgroup("memory.max", fmt.Sprintf("%d", memoryBytes)); err != nil {
			fmt.Printf("WARN: failed to set memory limit: %v\n", err)
		}
	}
	
	// Apply CPU limit
	if r.limits.CPU != nil && r.limits.CPU.Limit != "" {
		cpuPercent, err := parseCPU(r.limits.CPU.Limit)
		if err != nil {
			return fmt.Errorf("failed to parse CPU limit: %w", err)
		}
		
		// Convert percentage to quota (100ms period)
		// Example: 50% = 50000 quota per 100000 period
		quota := cpuPercent * 1000
		if err := r.writeCgroup("cpu.max", fmt.Sprintf("%d 100000", quota)); err != nil {
			fmt.Printf("WARN: failed to set CPU limit: %v\n", err)
		}
	}
	
	// Apply PID limit (max processes)
	if r.limits.MaxProcesses > 0 {
		if err := r.writeCgroup("pids.max", fmt.Sprintf("%d", r.limits.MaxProcesses)); err != nil {
			fmt.Printf("WARN: failed to set PID limit: %v\n", err)
		}
	}
	
	// Set up SysProcAttr to join cgroup
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	
	// Note: Actual cgroup assignment happens via cgroup.procs after process starts
	// This is handled in a post-start hook (not shown here for simplicity)
	
	return nil
}

// writeCgroup writes a value to a cgroup file.
func (r *ResourceLimiter) writeCgroup(file, value string) error {
	path := filepath.Join(r.cgroupPath, file)
	return os.WriteFile(path, []byte(value), 0644)
}

// AssignProcess assigns a running process to the cgroup.
func (r *ResourceLimiter) AssignProcess(pid int) error {
	if r.cgroupPath == "" {
		return nil // Cgroup not supported
	}
	
	procsPath := filepath.Join(r.cgroupPath, "cgroup.procs")
	return os.WriteFile(procsPath, []byte(strconv.Itoa(pid)), 0644)
}

// Cleanup removes the cgroup directory.
func (r *ResourceLimiter) Cleanup() error {
	if r.cgroupPath == "" {
		return nil
	}
	
	return os.RemoveAll(r.cgroupPath)
}

// Helper functions to parse resource quantities

func parseMemory(mem string) (int64, error) {
	// Parse formats like "256Mi", "512MB", "1Gi", "1GB"
	if len(mem) < 2 {
		return 0, fmt.Errorf("invalid memory format: %s", mem)
	}
	
	// Extract number and unit
	var value int64
	var unit string
	
	if mem[len(mem)-2:] == "Mi" || mem[len(mem)-2:] == "Gi" || mem[len(mem)-2:] == "Ki" {
		fmt.Sscanf(mem, "%d%s", &value, &unit)
	} else if mem[len(mem)-2:] == "MB" || mem[len(mem)-2:] == "GB" || mem[len(mem)-2:] == "KB" {
		fmt.Sscanf(mem, "%d%s", &value, &unit)
	} else {
		// Assume bytes
		fmt.Sscanf(mem, "%d", &value)
		return value, nil
	}
	
	// Convert to bytes
	switch unit {
	case "Ki":
		return value * 1024, nil
	case "Mi":
		return value * 1024 * 1024, nil
	case "Gi":
		return value * 1024 * 1024 * 1024, nil
	case "KB":
		return value * 1000, nil
	case "MB":
		return value * 1000 * 1000, nil
	case "GB":
		return value * 1000 * 1000 * 1000, nil
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}
}

func parseCPU(cpu string) (int, error) {
	// Parse formats like "25" (25%), "50" (50%)
	var percent int
	if _, err := fmt.Sscanf(cpu, "%d", &percent); err != nil {
		return 0, fmt.Errorf("invalid CPU format: %s", cpu)
	}
	
	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("CPU percent must be 0-100, got %d", percent)
	}
	
	return percent, nil
}
