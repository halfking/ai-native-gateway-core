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
	monitorActive bool // Track if monitoring goroutine is running
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
			// 父节点启用控制器委派（cgroup v2 「no internal process」约束）：
			// 子组要有 memory.max/cpu.max/pids.max 控制文件，父目录必须先在
			// cgroup.subtree_control 打开 +memory +cpu +pids。缺这一步 MkdirAll
			// 出来的子组只有 cgroup.* 元文件，后续 writeCgroup 全部 ENOENT
			// （2026-11-28 审计 F1：与 plugin-runtime/limiter 包 v1.4 修复对齐）。
			// 幂等：重复写同值合法；父目录不存在则先建；写失败仅警告。
			parent := "/sys/fs/cgroup/llm-gateway-plugins"
			if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
				fmt.Printf("WARN: failed to create cgroup parent %s: %v (limits may not apply)\n", parent, mkErr)
			} else if err := os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+memory +cpu +pids"), 0o644); err != nil {
				fmt.Printf("WARN: cannot enable subtree controllers for llm-gateway-plugins: %v (limits may not apply)\n", err)
			}
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
		r.mu.Lock()
		r.monitorActive = true
		r.mu.Unlock()
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
	r.mu.Lock()
	active := r.monitorActive
	r.mu.Unlock()

	if r.mode == ModeMonitor && active {
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
	// Parse formats like "256Mi", "512MB", "128M", "1G", or plain bytes.
	// 2026-11-28 审计 F3：与 plugin-runtime/limiter 包的 parseMemory 语义对齐——
	// 旧版把单字母后缀（512M）当 plain number 解析成 512 字节，且 MB/GB 走
	// 1000 进制（limiter 包 MB/GB=1024 进制），两包行为漂移。本模块独立
	// go.mod，不引入跨模块依赖，复制语义而不是 import。
	if len(mem) < 2 {
		return 0, fmt.Errorf("invalid memory format: %s", mem)
	}

	var value int64
	var unit string

	two := mem[len(mem)-2:]
	switch two {
	case "Mi", "Gi", "Ki", "MB", "GB", "KB":
		if _, err := fmt.Sscanf(mem, "%d%s", &value, &unit); err != nil {
			return 0, fmt.Errorf("invalid memory format: %s", mem)
		}
	default:
		// Single-letter suffix (M/G/K) or plain bytes
		single := mem[len(mem)-1:]
		if single == "M" || single == "G" || single == "K" {
			if _, err := fmt.Sscanf(mem, "%d%s", &value, &unit); err != nil {
				return 0, fmt.Errorf("invalid memory format: %s", mem)
			}
			// Normalize to the decimal form handled below.
			unit = unit + "B"
			if unit == "MB" || unit == "GB" || unit == "KB" {
				break
			}
			return 0, fmt.Errorf("unknown unit: %s", unit)
		}
		// Plain number (bytes)
		if _, err := fmt.Sscanf(mem, "%d", &value); err != nil {
			return 0, fmt.Errorf("invalid memory format: %s", mem)
		}
		return value, nil
	}

	// Convert to bytes（Ki/Mi/Gi 与 KB/MB/GB 统一 1024 进制，同 limiter 包）
	switch unit {
	case "Ki", "KB":
		return value * 1024, nil
	case "Mi", "MB":
		return value * 1024 * 1024, nil
	case "Gi", "GB":
		return value * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}
}

func parseCPU(cpu string) (int, error) {
	// Parse formats like "25" (25%), "50" (50%), or "25%".
	var percent int
	cpu = trimSuffix(cpu, "%")
	if _, err := fmt.Sscanf(cpu, "%d", &percent); err != nil {
		return 0, fmt.Errorf("invalid CPU format: %s", cpu)
	}

	// 0% → cpu.max="0 100000"，内核 quota=0=unlimited（审计 F5，与 limiter 包同语义拒绝）
	if percent == 0 {
		return 0, fmt.Errorf("CPU percent must be >= 1 (0%% would write cpu.max quota=0, which the kernel treats as unlimited): %s", cpu)
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
