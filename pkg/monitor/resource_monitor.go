package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
	"gopkg.in/natefinch/lumberjack.v2"
)

// ResourceSnapshot 资源使用快照
type ResourceSnapshot struct {
	Timestamp         time.Time           `json:"timestamp"`
	InstanceID        string              `json:"instance_id"`
	PID               int32               `json:"pid"`
	Goroutines        int                 `json:"goroutines"`
	Threads           int                 `json:"threads"`
	MemoryStats       MemoryStats         `json:"memory_stats"`
	SystemMemoryStats SystemMemoryStats   `json:"system_memory_stats"`
	CPUStats          CPUStats            `json:"cpu_stats"`
	FileDescriptors   FileDescriptorStats `json:"file_descriptors"`
	GCStats           GCStats             `json:"gc_stats"`
}

// MemoryStats 进程内存统计
type MemoryStats struct {
	AllocMB        uint64  `json:"alloc_mb"`         // 当前分配的内存(MB)
	TotalAllocMB   uint64  `json:"total_alloc_mb"`   // 累计分配的内存(MB)
	SysMB          uint64  `json:"sys_mb"`           // 从系统获得的内存(MB)
	HeapAllocMB    uint64  `json:"heap_alloc_mb"`    // 堆上分配的内存(MB)
	HeapSysMB      uint64  `json:"heap_sys_mb"`      // 堆从系统获得的内存(MB)
	HeapIdleMB     uint64  `json:"heap_idle_mb"`     // 堆空闲内存(MB)
	HeapInUseMB    uint64  `json:"heap_in_use_mb"`   // 堆使用中的内存(MB)
	HeapReleasedMB uint64  `json:"heap_released_mb"` // 释放给系统的内存(MB)
	StackInUseMB   uint64  `json:"stack_in_use_mb"`  // 栈使用的内存(MB)
	StackSysMB     uint64  `json:"stack_sys_mb"`     // 栈从系统获得的内存(MB)
	RSSMB          uint64  `json:"rss_mb"`           // RSS内存(MB)
	VSZMB          uint64  `json:"vsz_mb"`           // 虚拟内存(MB)
	HeapObjects    uint64  `json:"heap_objects"`     // 堆对象数量
	MemoryPercent  float32 `json:"memory_percent"`   // 内存使用百分比
}

// SystemMemoryStats 系统内存统计
type SystemMemoryStats struct {
	TotalMB     uint64  `json:"total_mb"`
	AvailableMB uint64  `json:"available_mb"`
	UsedMB      uint64  `json:"used_mb"`
	UsedPercent float64 `json:"used_percent"`
}

// CPUStats CPU统计
type CPUStats struct {
	CPUPercent    float64 `json:"cpu_percent"`    // 进程CPU使用率
	SystemPercent float64 `json:"system_percent"` // 系统CPU使用率
	NumCPU        int     `json:"num_cpu"`        // CPU核心数
}

// FileDescriptorStats 文件描述符统计
type FileDescriptorStats struct {
	OpenFDs   int32   `json:"open_fds"`   // 打开的文件描述符数量
	MaxFDs    int64   `json:"max_fds"`    // 最大文件描述符数量
	UsedRatio float64 `json:"used_ratio"` // 使用比例
}

// GCStats GC统计
type GCStats struct {
	NumGC        uint32  `json:"num_gc"`         // GC次数
	PauseTotalMs uint64  `json:"pause_total_ms"` // GC总暂停时间(ms)
	PauseNs      uint64  `json:"pause_ns"`       // 最近一次GC暂停时间(ns)
	LastGC       string  `json:"last_gc"`        // 最近一次GC时间
	GCCPUPercent float64 `json:"gc_cpu_percent"` // GC占用的CPU百分比
	NextGCMB     uint64  `json:"next_gc_mb"`     // 下次GC触发阈值(MB)
}

// ResourceMonitor 资源监控器
type ResourceMonitor struct {
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	interval     time.Duration
	logWriter    io.WriteCloser
	jsonEncoder  *json.Encoder
	proc         *process.Process
	instanceID   string
	lastSnapshot *ResourceSnapshot
	leakDetector *LeakDetector
	started      bool
	stopOnce     sync.Once
	stopDone     chan struct{}
}

// LeakDetector 泄漏检测器
type LeakDetector struct {
	mu                    sync.RWMutex
	memoryGrowthThreshold float64 // 内存增长阈值(MB/min)
	goroutineThreshold    int     // goroutine数量阈值
	fdGrowthThreshold     float64 // 文件描述符增长阈值(个/min)
	alerts                []LeakAlert
	maxAlerts             int
}

// LeakAlert 泄漏告警
type LeakAlert struct {
	Timestamp    time.Time `json:"timestamp"`
	AlertType    string    `json:"alert_type"`
	Message      string    `json:"message"`
	CurrentVal   float64   `json:"current_val"`
	ThresholdVal float64   `json:"threshold_val"`
}

// ResourceMonitorConfig 资源监控配置
type ResourceMonitorConfig struct {
	LogDir             string        // 日志目录
	Interval           time.Duration // 采集间隔，默认30秒
	MaxSize            int           // 单个日志文件最大大小(MB)，默认100MB
	MaxBackups         int           // 保留的旧日志文件数量，默认30
	MaxAge             int           // 保留日志文件的天数，默认90
	Compress           bool          // 是否压缩旧日志，默认true
	InstanceID         string        // 实例ID
	MemGrowthThreshold float64       // 内存增长阈值(MB/min)，默认10MB/min
	GoroutineThreshold int           // goroutine数量阈值，默认10000
	FDGrowthThreshold  float64       // FD增长阈值(个/min)，默认100/min
}

var (
	globalResourceMonitor *ResourceMonitor
	resourceMonitorOnce   sync.Once
)

// InitResourceMonitor 初始化全局资源监控器
func InitResourceMonitor(config ResourceMonitorConfig) (*ResourceMonitor, error) {
	var err error
	resourceMonitorOnce.Do(func() {
		globalResourceMonitor, err = NewResourceMonitor(config)
	})
	return globalResourceMonitor, err
}

// GetResourceMonitor 获取全局资源监控器
func GetResourceMonitor() *ResourceMonitor {
	return globalResourceMonitor
}

// NewResourceMonitor 创建资源监控器
func NewResourceMonitor(config ResourceMonitorConfig) (*ResourceMonitor, error) {
	// 设置默认值
	if config.LogDir == "" {
		config.LogDir = "./logs"
	}
	if config.Interval == 0 {
		config.Interval = 30 * time.Second
	}
	if config.MaxSize == 0 {
		config.MaxSize = 100
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = 30
	}
	if config.MaxAge == 0 {
		config.MaxAge = 90
	}
	if config.InstanceID == "" {
		hostname, _ := os.Hostname()
		config.InstanceID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}
	if config.MemGrowthThreshold == 0 {
		config.MemGrowthThreshold = 10.0 // 10MB/min
	}
	if config.GoroutineThreshold == 0 {
		config.GoroutineThreshold = 10000
	}
	if config.FDGrowthThreshold == 0 {
		config.FDGrowthThreshold = 100.0 // 100/min
	}

	// 创建日志目录
	if err := os.MkdirAll(config.LogDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// 创建日志写入器
	logWriter := &lumberjack.Logger{
		Filename:   filepath.Join(config.LogDir, "resource_monitor.log"),
		MaxSize:    config.MaxSize,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,
		Compress:   config.Compress,
		LocalTime:  true,
	}

	// 获取当前进程
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	rm := &ResourceMonitor{
		ctx:         ctx,
		cancel:      cancel,
		interval:    config.Interval,
		logWriter:   logWriter,
		jsonEncoder: json.NewEncoder(logWriter),
		proc:        proc,
		instanceID:  config.InstanceID,
		leakDetector: &LeakDetector{
			memoryGrowthThreshold: config.MemGrowthThreshold,
			goroutineThreshold:    config.GoroutineThreshold,
			fdGrowthThreshold:     config.FDGrowthThreshold,
			alerts:                make([]LeakAlert, 0),
			maxAlerts:             1000,
		},
		stopDone: make(chan struct{}),
	}

	return rm, nil
}

// Start 启动资源监控
func (rm *ResourceMonitor) Start() {
	rm.mu.Lock()
	if rm.started {
		rm.mu.Unlock()
		return
	}
	rm.started = true
	rm.mu.Unlock()

	go rm.collectLoop()
}

// Stop 停止资源监控
func (rm *ResourceMonitor) Stop() error {
	rm.stopOnce.Do(func() {
		rm.mu.RLock()
		started := rm.started
		rm.mu.RUnlock()
		if !started {
			close(rm.stopDone)
			return
		}

		rm.cancel()
		<-rm.stopDone

		// 记录最终快照，确保停止前的资源状态落盘。
		if snapshot, err := rm.collectSnapshot(); err == nil {
			rm.writeSnapshot(snapshot)
		}
	})

	return rm.logWriter.Close()
}

// collectLoop 采集循环
func (rm *ResourceMonitor) collectLoop() {
	ticker := time.NewTicker(rm.interval)
	defer ticker.Stop()
	defer close(rm.stopDone)

	// 立即采集第一次
	if snapshot, err := rm.collectSnapshot(); err == nil {
		rm.writeSnapshot(snapshot)
		rm.updateLastSnapshot(snapshot)
	}

	for {
		select {
		case <-rm.ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := rm.collectSnapshot()
			if err != nil {
				continue
			}
			rm.writeSnapshot(snapshot)
			rm.detectLeaks(snapshot)
			rm.updateLastSnapshot(snapshot)
		}
	}
}

// collectSnapshot 采集资源快照
func (rm *ResourceMonitor) collectSnapshot() (*ResourceSnapshot, error) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	snapshot := &ResourceSnapshot{
		Timestamp:  time.Now(),
		InstanceID: rm.instanceID,
		PID:        int32(os.Getpid()),
		Goroutines: runtime.NumGoroutine(),
	}

	// 内存统计
	snapshot.MemoryStats = MemoryStats{
		AllocMB:        m.Alloc / 1024 / 1024,
		TotalAllocMB:   m.TotalAlloc / 1024 / 1024,
		SysMB:          m.Sys / 1024 / 1024,
		HeapAllocMB:    m.HeapAlloc / 1024 / 1024,
		HeapSysMB:      m.HeapSys / 1024 / 1024,
		HeapIdleMB:     m.HeapIdle / 1024 / 1024,
		HeapInUseMB:    m.HeapInuse / 1024 / 1024,
		HeapReleasedMB: m.HeapReleased / 1024 / 1024,
		StackInUseMB:   m.StackInuse / 1024 / 1024,
		StackSysMB:     m.StackSys / 1024 / 1024,
		HeapObjects:    m.HeapObjects,
	}

	// GC统计
	snapshot.GCStats = GCStats{
		NumGC:        m.NumGC,
		PauseTotalMs: m.PauseTotalNs / 1000000,
		NextGCMB:     m.NextGC / 1024 / 1024,
		GCCPUPercent: m.GCCPUFraction * 100,
	}
	if m.NumGC > 0 {
		snapshot.GCStats.PauseNs = m.PauseNs[(m.NumGC+255)%256]
		snapshot.GCStats.LastGC = time.Unix(0, int64(m.LastGC)).Format(time.RFC3339)
	}

	// 进程级统计
	if memInfo, err := rm.proc.MemoryInfo(); err == nil {
		snapshot.MemoryStats.RSSMB = memInfo.RSS / 1024 / 1024
		snapshot.MemoryStats.VSZMB = memInfo.VMS / 1024 / 1024
	}

	if memPercent, err := rm.proc.MemoryPercent(); err == nil {
		snapshot.MemoryStats.MemoryPercent = memPercent
	}

	if threads, err := rm.proc.NumThreads(); err == nil {
		snapshot.Threads = int(threads)
	}

	// CPU统计
	snapshot.CPUStats.NumCPU = runtime.NumCPU()
	if cpuPercent, err := rm.proc.CPUPercent(); err == nil {
		snapshot.CPUStats.CPUPercent = cpuPercent
	}
	if systemPercent, err := cpu.Percent(0, false); err == nil && len(systemPercent) > 0 {
		snapshot.CPUStats.SystemPercent = systemPercent[0]
	}

	// 文件描述符统计
	if numFDs, err := rm.proc.NumFDs(); err == nil {
		snapshot.FileDescriptors.OpenFDs = numFDs
		if rlimit, err := rm.proc.Rlimit(); err == nil {
			for _, limit := range rlimit {
				if limit.Resource == 7 { // RLIMIT_NOFILE
					snapshot.FileDescriptors.MaxFDs = int64(limit.Soft)
					if limit.Soft > 0 {
						snapshot.FileDescriptors.UsedRatio = float64(numFDs) / float64(limit.Soft)
					}
					break
				}
			}
		}
	}

	// 系统内存统计
	if vmStat, err := mem.VirtualMemory(); err == nil {
		snapshot.SystemMemoryStats = SystemMemoryStats{
			TotalMB:     vmStat.Total / 1024 / 1024,
			AvailableMB: vmStat.Available / 1024 / 1024,
			UsedMB:      vmStat.Used / 1024 / 1024,
			UsedPercent: vmStat.UsedPercent,
		}
	}

	return snapshot, nil
}

// writeSnapshot 写入快照到日志
func (rm *ResourceMonitor) writeSnapshot(snapshot *ResourceSnapshot) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.jsonEncoder.Encode(snapshot)
}

// updateLastSnapshot 更新最后一次快照
func (rm *ResourceMonitor) updateLastSnapshot(snapshot *ResourceSnapshot) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.lastSnapshot = snapshot
}

// detectLeaks 检测可能的泄漏
func (rm *ResourceMonitor) detectLeaks(current *ResourceSnapshot) {
	rm.mu.RLock()
	last := rm.lastSnapshot
	rm.mu.RUnlock()

	if last == nil {
		return
	}

	duration := current.Timestamp.Sub(last.Timestamp).Minutes()
	if duration <= 0 {
		return
	}

	// 检测内存泄漏（只关注正向增长，避免无符号下溢误报）
	memDelta := int64(current.MemoryStats.RSSMB) - int64(last.MemoryStats.RSSMB)
	memGrowth := float64(memDelta) / duration
	if memGrowth > rm.leakDetector.memoryGrowthThreshold {
		alert := LeakAlert{
			Timestamp:    current.Timestamp,
			AlertType:    "memory_leak_suspected",
			Message:      fmt.Sprintf("Memory growing at %.2f MB/min (threshold: %.2f MB/min)", memGrowth, rm.leakDetector.memoryGrowthThreshold),
			CurrentVal:   memGrowth,
			ThresholdVal: rm.leakDetector.memoryGrowthThreshold,
		}
		rm.leakDetector.addAlert(alert)
		rm.writeAlert(alert)
	}

	// 检测goroutine泄漏
	if current.Goroutines > rm.leakDetector.goroutineThreshold {
		alert := LeakAlert{
			Timestamp:    current.Timestamp,
			AlertType:    "goroutine_leak_suspected",
			Message:      fmt.Sprintf("Goroutine count %d exceeds threshold %d", current.Goroutines, rm.leakDetector.goroutineThreshold),
			CurrentVal:   float64(current.Goroutines),
			ThresholdVal: float64(rm.leakDetector.goroutineThreshold),
		}
		rm.leakDetector.addAlert(alert)
		rm.writeAlert(alert)
	}

	// 检测文件描述符泄漏
	fdGrowth := float64(current.FileDescriptors.OpenFDs-last.FileDescriptors.OpenFDs) / duration
	if fdGrowth > rm.leakDetector.fdGrowthThreshold {
		alert := LeakAlert{
			Timestamp:    current.Timestamp,
			AlertType:    "fd_leak_suspected",
			Message:      fmt.Sprintf("File descriptors growing at %.2f/min (threshold: %.2f/min)", fdGrowth, rm.leakDetector.fdGrowthThreshold),
			CurrentVal:   fdGrowth,
			ThresholdVal: rm.leakDetector.fdGrowthThreshold,
		}
		rm.leakDetector.addAlert(alert)
		rm.writeAlert(alert)
	}
}

// writeAlert 写入告警
func (rm *ResourceMonitor) writeAlert(alert LeakAlert) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	alertLog := map[string]interface{}{
		"type":  "leak_alert",
		"alert": alert,
	}
	rm.jsonEncoder.Encode(alertLog)
}

// addAlert 添加告警
func (ld *LeakDetector) addAlert(alert LeakAlert) {
	ld.mu.Lock()
	defer ld.mu.Unlock()

	ld.alerts = append(ld.alerts, alert)
	if len(ld.alerts) > ld.maxAlerts {
		ld.alerts = ld.alerts[1:]
	}
}

// GetLastSnapshot 获取最后一次快照
func (rm *ResourceMonitor) GetLastSnapshot() *ResourceSnapshot {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.lastSnapshot
}

// GetAlerts 获取所有告警
func (rm *ResourceMonitor) GetAlerts() []LeakAlert {
	rm.leakDetector.mu.RLock()
	defer rm.leakDetector.mu.RUnlock()

	alerts := make([]LeakAlert, len(rm.leakDetector.alerts))
	copy(alerts, rm.leakDetector.alerts)
	return alerts
}
