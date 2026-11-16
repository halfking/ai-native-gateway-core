package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
)

// ResourceLimiter manages resource limits for plugins.
// On Linux: uses cgroupsv2 for hard limits (memory, CPU, PIDs)
// On macOS: uses process monitoring with warnings (no hard enforcement)
type ResourceLimiter struct {
	pluginName string
	cgroupPath string
	limits     discovery.ResourceLimits
	mode       LimiterMode

	// Process monitoring (macOS fallback)
	monitorCancel chan struct{}
	monitorDone   chan struct{}
	mu            sync.RWMutex
	lastStats     *ProcessStats
}

// LimiterMode indicates the enforcement mode
type LimiterMode string

const (
	ModeCgroup  LimiterMode = "cgroup"  // Linux cgroupv2 hard limits
	ModeMonitor LimiterMode = "monitor" // macOS process monitoring + warnings
	ModeDisabled LimiterMode = "disabled" // No resource limits
)

// ProcessStats tracks resource usage for monitoring
type ProcessStats struct {
	MemoryBytes uint64
	CPUPercent  float64
	NumThreads  int
	Timestamp   time.Time
}

// NewResourceLimiter creates a new resource limiter.
func NewResourceLimiter(pluginName string, limits discovery.ResourceLimits) (*ResourceLimiter, error) {
	rl := &ResourceLimiter{
		pluginName:    pluginName,
		limits:        limits,
		monitorCancel: make(chan struct{}),
		monitorDone:   make(chan struct{}),
	}

	// Determine mode based on OS and cgroup availability
	if runtime.GOOS == "linux" {
		cgroupPath := filepath.Join("/sys/fs/cgroup", "llm-gateway-plugins", pluginName)
		
		// Check if cgroup v2 is mounted
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
			// Try to create cgroup directory
			if err := os.MkdirAll(cgroupPath, 0755); err != nil {
				fmt.Printf("WARN: failed to create cgroup %s: %v (falling back to monitor mode)\n", cgroupPath, err)
				rl.mode = ModeMonitor
			} else {
				rl.cgroupPath = cgroupPath
				rl.mode = ModeCgroup
				fmt.Printf("INFO: cgroup resource limits enabled for plugin %s\n", pluginName)
			}
		} else {
			fmt.Printf("WARN: cgroupv2 not available (falling back to monitor mode)\n")
			rl.mode = ModeMonitor
		}
	} else {
		// macOS or other OS: use monitor mode
		fmt.Printf("INFO: resource monitoring enabled for plugin %s (OS: %s, no hard limits)\n", pluginName, runtime.GOOS)
		rl.mode = ModeMonitor
	}

	return rl, nil
}

// Apply applies resource limits to a command.
func (r *ResourceLimiter) Apply(cmd *exec.Cmd) error {
	switch r.mode {
	case ModeCgroup:
		return r.applyCgroupLimits(cmd)
	case ModeMonitor:
		// For monitor mode, limits are checked after process starts
		return nil
	case ModeDisabled:
		return nil
	default:
		return fmt.Errorf("unknown limiter mode: %s", r.mode)
	}
}

// applyCgroupLimits applies cgroupv2 resource limits (Linux only)
func (r *ResourceLimiter) applyCgroupLimits(cmd *exec.Cmd) error {
	if r.cgroupPath == "" {
		return fmt.Errorf("cgroup path not initialized")
	}

	// Apply memory limit
	if r.limits.Memory != nil && r.limits.Memory.Limit != "" {
		memoryBytes, err := parseMemory(r.limits.Memory.Limit)
		if err != nil {
			return fmt.Errorf("failed to parse memory limit: %w", err)
		}

		if err := r.writeCgroup("memory.max", fmt.Sprintf("%d", memoryBytes)); err != nil {
			fmt.Printf("WARN: failed to set memory limit: %v\n", err)
		} else {
			fmt.Printf("INFO: set memory limit for %s: %d bytes\n", r.pluginName, memoryBytes)
		}

		// Set memory.high for soft limit (triggers reclaim before OOM)
		softLimit := memoryBytes * 90 / 100 // 90% of hard limit
		if err := r.writeCgroup("memory.high", fmt.Sprintf("%d", softLimit)); err != nil {
			fmt.Printf("WARN: failed to set memory.high: %v\n", err)
		}
	}

	// Apply CPU limit
	if r.limits.CPU != nil && r.limits.CPU.Limit != "" {
		cpuPercent, err := parseCPU(r.limits.CPU.Limit)
		if err != nil {
			return fmt.Errorf("failed to parse CPU limit: %w", err)
		}

		// Convert percentage to quota (100ms period)
		// Example: 25% = 25000 quota per 100000 period
		quota := cpuPercent * 1000
		if err := r.writeCgroup("cpu.max", fmt.Sprintf("%d 100000", quota)); err != nil {
			fmt.Printf("WARN: failed to set CPU limit: %v\n", err)
		} else {
			fmt.Printf("INFO: set CPU limit for %s: %d%%\n", r.pluginName, cpuPercent)
		}
	}

	// Apply PID limit (max processes/threads)
	maxProcs := r.limits.MaxProcesses
	if maxProcs == 0 {
		maxProcs = 10 // Default: 10 processes
	}
	if err := r.writeCgroup("pids.max", fmt.Sprintf("%d", maxProcs)); err != nil {
		fmt.Printf("WARN: failed to set PID limit: %v\n", err)
	} else {
		fmt.Printf("INFO: set PID limit for %s: %d\n", r.pluginName, maxProcs)
	}

	// Set up SysProcAttr for Linux
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	return nil
}

// writeCgroup writes a value to a cgroup file.
func (r *ResourceLimiter) writeCgroup(file, value string) error {
	path := filepath.Join(r.cgroupPath, file)
	return os.WriteFile(path, []byte(value), 0644)
}

// AssignProcess assigns a running process to the cgroup (Linux) or starts monitoring (macOS).
func (r *ResourceLimiter) AssignProcess(pid int) error {
	switch r.mode {
	case ModeCgroup:
		if r.cgroupPath == "" {
			return fmt.Errorf("cgroup path not initialized")
		}

		// Assign process to cgroup
		procsPath := filepath.Join(r.cgroupPath, "cgroup.procs")
		if err := os.WriteFile(procsPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
			return fmt.Errorf("failed to assign process to cgroup: %w", err)
		}
		
		fmt.Printf("INFO: assigned process %d to cgroup %s\n", pid, r.cgroupPath)
		return nil

	case ModeMonitor:
		// Start process monitoring
		go r.monitorProcess(pid)
		return nil

	case ModeDisabled:
		return nil

	default:
		return fmt.Errorf("unknown limiter mode: %s", r.mode)
	}
}

// monitorProcess monitors process resource usage (macOS fallback)
func (r *ResourceLimiter) monitorProcess(pid int) {
	defer close(r.monitorDone)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stats, err := r.getProcessStats(pid)
			if err != nil {
				// Process might have exited
				return
			}

			r.mu.Lock()
			r.lastStats = stats
			r.mu.Unlock()

			// Check memory limit
			if r.limits.Memory != nil && r.limits.Memory.Limit != "" {
				memLimit, err := parseMemory(r.limits.Memory.Limit)
				if err == nil && stats.MemoryBytes > uint64(memLimit) {
					fmt.Printf("WARN: plugin %s exceeded memory limit: %d bytes (limit: %d bytes)\n",
						r.pluginName, stats.MemoryBytes, memLimit)
				}
			}

			// Check CPU limit (averaged over monitoring period)
			if r.limits.CPU != nil && r.limits.CPU.Limit != "" {
				cpuLimit, err := parseCPU(r.limits.CPU.Limit)
				if err == nil && stats.CPUPercent > float64(cpuLimit) {
					fmt.Printf("WARN: plugin %s exceeded CPU limit: %.1f%% (limit: %d%%)\n",
						r.pluginName, stats.CPUPercent, cpuLimit)
				}
			}

			// Check PID limit
			maxProcs := r.limits.MaxProcesses
			if maxProcs > 0 && stats.NumThreads > maxProcs {
				fmt.Printf("WARN: plugin %s exceeded thread limit: %d threads (limit: %d)\n",
					r.pluginName, stats.NumThreads, maxProcs)
			}

		case <-r.monitorCancel:
			return
		}
	}
}

// getProcessStats retrieves current process resource usage
func (r *ResourceLimiter) getProcessStats(pid int) (*ProcessStats, error) {
	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("process not found: %w", err)
	}

	// On Unix, FindProcess always succeeds. Send signal 0 to check if alive.
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return nil, fmt.Errorf("process not alive: %w", err)
	}

	// Platform-specific stats collection
	stats := &ProcessStats{
		Timestamp: time.Now(),
	}

	if runtime.GOOS == "darwin" {
		// macOS: read from ps command
		// Note: This is a simplified implementation. Production code should use
		// syscall or cgo for more accurate metrics.
		stats.MemoryBytes = 0  // TODO: implement via syscall
		stats.CPUPercent = 0   // TODO: implement via syscall
		stats.NumThreads = 1   // TODO: implement via syscall
	} else if runtime.GOOS == "linux" {
		// Linux: read from /proc
		if err := r.readProcStats(pid, stats); err != nil {
			return nil, err
		}
	}

	return stats, nil
}

// readProcStats reads process stats from /proc filesystem (Linux only)
func (r *ResourceLimiter) readProcStats(pid int, stats *ProcessStats) error {
	// Read /proc/[pid]/status for memory
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	statusData, err := os.ReadFile(statusPath)
	if err != nil {
		return fmt.Errorf("failed to read /proc status: %w", err)
	}

	// Parse VmRSS (resident set size)
	for _, line := range splitLines(string(statusData)) {
		if len(line) > 6 && line[:6] == "VmRSS:" {
			var vmRSS uint64
			fmt.Sscanf(line[6:], "%d", &vmRSS)
			stats.MemoryBytes = vmRSS * 1024 // Convert kB to bytes
		}
		if len(line) > 8 && line[:8] == "Threads:" {
			fmt.Sscanf(line[8:], "%d", &stats.NumThreads)
		}
	}

	// CPU percentage would require tracking over time
	// Simplified: set to 0 for now
	stats.CPUPercent = 0

	return nil
}

// GetStats returns the last collected process stats (monitor mode only)
func (r *ResourceLimiter) GetStats() *ProcessStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastStats
}

// Cleanup removes the cgroup directory and stops monitoring.
func (r *ResourceLimiter) Cleanup() error {
	// Stop monitoring if running
	if r.mode == ModeMonitor {
		close(r.monitorCancel)
		<-r.monitorDone
	}

	// Remove cgroup directory
	if r.cgroupPath != "" {
		return os.RemoveAll(r.cgroupPath)
	}

	return nil
}

// Helper functions

func splitLines(s string) []string {
	var lines []string
	var current string
	for _, c := range s {
		if c == '\n' {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func parseMemory(mem string) (int64, error) {
	// Parse formats like "256Mi", "512MB", "1Gi", "1GB"
	if len(mem) < 2 {
		return 0, fmt.Errorf("invalid memory format: %s", mem)
	}

	// Extract number and unit
	var value int64
	var unit string
	var scanned int

	if len(mem) >= 2 && (mem[len(mem)-2:] == "Mi" || mem[len(mem)-2:] == "Gi" || mem[len(mem)-2:] == "Ki") {
		scanned, _ = fmt.Sscanf(mem, "%d%s", &value, &unit)
		if scanned != 2 {
			return 0, fmt.Errorf("invalid memory format: %s", mem)
		}
	} else if len(mem) >= 2 && (mem[len(mem)-2:] == "MB" || mem[len(mem)-2:] == "GB" || mem[len(mem)-2:] == "KB") {
		scanned, _ = fmt.Sscanf(mem, "%d%s", &value, &unit)
		if scanned != 2 {
			return 0, fmt.Errorf("invalid memory format: %s", mem)
		}
	} else {
		// Try to parse as plain number (bytes)
		scanned, _ = fmt.Sscanf(mem, "%d", &value)
		if scanned != 1 {
			return 0, fmt.Errorf("invalid memory format: %s", mem)
		}
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
	// Parse formats like "25" (25%), "50" (50%), or "25%"
	var percent int
	cpu = trimSuffix(cpu, "%")
	if _, err := fmt.Sscanf(cpu, "%d", &percent); err != nil {
		return 0, fmt.Errorf("invalid CPU format: %s", cpu)
	}

	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("CPU percent must be 0-100, got %d", percent)
	}

	return percent, nil
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
