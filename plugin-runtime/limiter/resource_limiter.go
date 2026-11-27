package limiter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ResourceLimiter manages resource limits for plugins.
// On Linux: uses cgroupsv2 for hard limits (memory, CPU, PIDs)
// On macOS: uses process monitoring with warnings (no hard enforcement)
type ResourceLimiter struct {
	pluginName string
	cgroupPath string
	limits     ResourceLimits
	mode       LimiterMode

	// Process monitoring (macOS fallback)
	monitorCancel chan struct{}
	monitorDone   chan struct{}
	monitorActive bool // Track if monitoring goroutine is running
	mu            sync.RWMutex
	lastStats     *ProcessStats

	// onViolation / onOOMKill 是可选观测回调（集成方案 §5.3 Prometheus 埋点），
	// 由 pluginruntime.NewSupervisor 注入 metrics.RecordPluginLimitViolation /
	// RecordPluginOOMKill。本包不直接依赖 metrics——limiter 保持可独立测试，
	// 回调为 nil 时行为与历史版本完全一致（仅 fmt WARN）。
	onViolation func(kind string)
	onOOMKill   func(delta uint64)
	// lastOOMKills 记录上次读到的 memory.events oom_kill 累计值（cgroup 模式
	// 采样读回），差分上报增量；oomSeen 防首次读回把历史 OOM 计为本进程增量。
	lastOOMKills uint64
	oomSeen      bool
	// lastCPUMicros 记录上次 cpu.stat usage_usec 读数（cgroup CPU 差分用）。
	// 仅 monitorCgroup goroutine 读写。
	lastCPUMicros uint64
}

// SetObservabilityHooks 注入 §5.3 埋点回调（须在 Start/AssignProcess 之前调用）。
// nil 回调等价于不埋点。
func (r *ResourceLimiter) SetObservabilityHooks(onViolation func(kind string), onOOMKill func(delta uint64)) {
	r.onViolation = onViolation
	r.onOOMKill = onOOMKill
}

// LimiterMode indicates the enforcement mode
type LimiterMode string

const (
	ModeCgroup   LimiterMode = "cgroup"   // Linux cgroupv2 hard limits
	ModeMonitor  LimiterMode = "monitor"  // macOS process monitoring + warnings
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
func NewResourceLimiter(pluginName string, limits ResourceLimits) (*ResourceLimiter, error) {
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
			if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
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

// Mode returns the limiter's enforcement mode.
func (r *ResourceLimiter) Mode() LimiterMode { return r.mode }

// Limits 返回本 limiter 生效的限制声明（§5.3 导出面用；只读视图）。
func (r *ResourceLimiter) Limits() *ResourceLimits {
	return &r.limits
}

// Apply applies resource limits to a command (before process start).
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

		// Swap: cgroup v2 separates swap accounting (memory.swap.max). Default
		// policy is no swap unless the manifest explicitly opts in.
		swapMax := "0"
		if r.limits.Memory.Swap {
			swapMax = "max"
		}
		if err := r.writeCgroup("memory.swap.max", swapMax); err != nil {
			fmt.Printf("WARN: failed to set memory.swap.max: %v\n", err)
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
	return os.WriteFile(path, []byte(value), 0o644)
}

// AssignProcess assigns a running process to the cgroup (Linux) or starts monitoring (macOS).
func (r *ResourceLimiter) AssignProcess(pid int) error {
	if pid <= 0 {
		// 进程未真正拉起（测试 fake / 启动半途失败）时没有可分配的对象：
		// cgroup 模式写 0 会被内核拒绝，monitor 模式会对 pid 0 空转轮询。
		return fmt.Errorf("invalid pid %d", pid)
	}
	switch r.mode {
	case ModeCgroup:
		if r.cgroupPath == "" {
			return fmt.Errorf("cgroup path not initialized")
		}

		// Assign process to cgroup
		procsPath := filepath.Join(r.cgroupPath, "cgroup.procs")
		if err := os.WriteFile(procsPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
			return fmt.Errorf("failed to assign process to cgroup: %w", err)
		}

		fmt.Printf("INFO: assigned process %d to cgroup %s\n", pid, r.cgroupPath)
		// §5.3：cgroup 模式同样启动采样读回（memory.current / pids.current /
		// memory.events oom_kill），与 monitor 模式共享 lastStats + 埋点回调。
		r.mu.Lock()
		r.monitorActive = true
		r.mu.Unlock()
		go r.monitorCgroup()
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
					if r.onViolation != nil {
						r.onViolation("memory")
					}
				}
			}

			// Check CPU limit (averaged over monitoring period)
			if r.limits.CPU != nil && r.limits.CPU.Limit != "" {
				cpuLimit, err := parseCPU(r.limits.CPU.Limit)
				if err == nil && stats.CPUPercent > float64(cpuLimit) {
					fmt.Printf("WARN: plugin %s exceeded CPU limit: %.1f%% (limit: %d%%)\n",
						r.pluginName, stats.CPUPercent, cpuLimit)
					if r.onViolation != nil {
						r.onViolation("cpu")
					}
				}
			}

			// Check PID limit
			maxProcs := r.limits.MaxProcesses
			if maxProcs > 0 && stats.NumThreads > maxProcs {
				fmt.Printf("WARN: plugin %s exceeded thread limit: %d threads (limit: %d)\n",
					r.pluginName, stats.NumThreads, maxProcs)
				if r.onViolation != nil {
					r.onViolation("threads")
				}
			}

		case <-r.monitorCancel:
			return
		}
	}
}

// monitorCgroup 采样读回 cgroup 状态（§5.3，Linux cgroup 模式）。阶段 1 交付
// 后 cgroup 模式「写完限制就不读」——限制是否生效、内存是否逼近上限、是否
// 发生过 OOM 全部无观察面，灰度期无法判断默认限制是否够用。本循环与
// monitorProcess 同周期（5s）读回：
//   - memory.current → lastStats.MemoryBytes（含 page cache，比 RSS 偏大，
//     判断水位时注意口径；对「逼近 memory.max」的告警目的这是更准确的口径）
//   - pids.current   → lastStats.NumThreads（cgroup 内全部任务数）
//   - memory.events  → oom_kill 计数差分 → onOOMKill 增量回调
//
// 读失败不终止循环：cgroup 文件在进程退出/Stop 清理后消失，属预期路径。
func (r *ResourceLimiter) monitorCgroup() {
	defer func() {
		r.mu.Lock()
		r.monitorActive = false
		r.mu.Unlock()
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if r.cgroupPath == "" {
				return
			}
			stats := &ProcessStats{Timestamp: time.Now()}
			if v, err := readCgroupUint(r.cgroupPath, "memory.current"); err == nil {
				stats.MemoryBytes = v
			}
			if v, err := readCgroupUint(r.cgroupPath, "pids.current"); err == nil {
				stats.NumThreads = int(v)
			}
			if v, err := readCgroupUint(r.cgroupPath, "cpu.stat", "usage_usec"); err == nil && r.lastStats != nil && r.lastStats.Timestamp.Unix() > 0 {
				// CPU 百分比 = 窗口内 usage 差分 / 窗口时长（单核口径 ×100）。
				elapsed := stats.Timestamp.Sub(r.lastStats.Timestamp).Seconds()
				if elapsed > 0 {
					delta := v - r.lastCPUMicros
					if delta > 0 {
						stats.CPUPercent = float64(delta) / elapsed / 10000 // usec→percent
					}
				}
				r.lastCPUMicros = v
			} else if err == nil {
				r.lastCPUMicros = v
			}

			r.mu.Lock()
			r.lastStats = stats
			r.mu.Unlock()

			// memory.events oom_kill 差分上报（首次读回仅记基线）。基线字段
			// 一并持锁——测试与未来导出面都会跨 goroutine 读它们。回调在锁外
			// 调用（metrics 打点不持 limiter 锁，避免锁序耦合）。
			if kills, err := readCgroupUint(r.cgroupPath, "memory.events", "oom_kill"); err == nil {
				var oomDelta uint64
				r.mu.Lock()
				if !r.oomSeen {
					r.oomSeen = true
					r.lastOOMKills = kills
				} else if kills > r.lastOOMKills {
					oomDelta = kills - r.lastOOMKills
					r.lastOOMKills = kills
				} else if kills < r.lastOOMKills {
					// cgroup 被重建（计数器归零）：重置基线，不报负增量。
					r.lastOOMKills = kills
				}
				r.mu.Unlock()
				if oomDelta > 0 && r.onOOMKill != nil {
					r.onOOMKill(oomDelta)
				}
			}

			// 软限检测：memory.current 超过硬限 90%（与 Apply 写入 memory.high
			// 的软限线一致）→ violation 计数。cgroup 硬限下超线先触发回收
			// （memory.high 节流），持续逼近说明限制偏小，正是灰度要看的信号。
			if r.limits.Memory != nil && r.limits.Memory.Limit != "" && stats.MemoryBytes > 0 {
				if memLimit, err := parseMemory(r.limits.Memory.Limit); err == nil {
					if softLimit := uint64(memLimit) * 90 / 100; stats.MemoryBytes > softLimit {
						fmt.Printf("WARN: plugin %s memory usage above soft limit: %d bytes (soft: %d, hard: %d)\n",
							r.pluginName, stats.MemoryBytes, softLimit, memLimit)
						if r.onViolation != nil {
							r.onViolation("memory")
						}
					}
				}
			}

		case <-r.monitorCancel:
			return
		}
	}
}

// readCgroupUint 从 cgroup 目录读一个键值。支持两种文件形态：
//   - 单值文件（memory.current / pids.current）：file 即文件名
//   - 多行键值文件（cpu.stat / memory.events）：key 指定行键
//
// 读失败（文件不存在 / cgroup 已清理）返回错误，调用方按预期路径忽略。
func readCgroupUint(dir, file string, key ...string) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if len(key) > 0 && key[0] != "" {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, key[0]+" ") {
				return strconv.ParseUint(strings.TrimSpace(line[len(key[0])+1:]), 10, 64)
			}
		}
		return 0, fmt.Errorf("key %s not found in %s/%s", key[0], dir, file)
	}
	return strconv.ParseUint(s, 10, 64)
}
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
		stats.MemoryBytes = 0 // TODO: implement via syscall
		stats.CPUPercent = 0  // TODO: implement via syscall
		stats.NumThreads = 1  // TODO: implement via syscall
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
	for _, line := range strings.Split(string(statusData), "\n") {
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

// Stop stops monitoring (monitor mode) and removes the cgroup directory
// (cgroup mode). Safe to call multiple times; a second call is a no-op.
func (r *ResourceLimiter) Stop() error {
	r.mu.Lock()
	active := r.monitorActive
	r.monitorActive = false
	r.mu.Unlock()

	if r.mode == ModeMonitor && active {
		select {
		case <-r.monitorCancel:
			// already closed
		default:
			close(r.monitorCancel)
		}
		<-r.monitorDone
	}

	// Remove cgroup directory
	if r.cgroupPath != "" {
		if err := os.RemoveAll(r.cgroupPath); err != nil {
			return err
		}
		r.cgroupPath = ""
	}

	return nil
}

// parseMemory parses memory formats like "256Mi", "512MB", "1Gi", "1GB",
// "128M", "2G" (decimal suffix as used in manifest resource_limits) or a
// plain byte count.
// ParseMemoryLimit 是 parseMemory 的导出包装（§5.3 导出面把声明限制换算成
// bytes 写 gauge 用）。
func ParseMemoryLimit(mem string) (int64, error) { return parseMemory(mem) }

func parseMemory(mem string) (int64, error) {
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

	// Convert to bytes
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

// parseCPU parses formats like "25" (25%), "50" (50%), or "25%".
func parseCPU(cpu string) (int, error) {
	var percent int
	cpu = strings.TrimSuffix(cpu, "%")
	if _, err := fmt.Sscanf(cpu, "%d", &percent); err != nil {
		return 0, fmt.Errorf("invalid CPU format: %s", cpu)
	}

	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("CPU percent must be 0-100, got %d", percent)
	}

	return percent, nil
}
