package limiter

import (
	"runtime"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// §8.2 性能验收基线。验收线：
//   - 插件启动时间增加 < 50ms（NewResourceLimiter + Apply + AssignProcess）
//   - ResourceLimiter 内存开销 < 10MB per plugin
//   - 监控模式 CPU 开销 < 1%（5s 周期采样，常态只有 ticker 空转）
// 跑法：go test ./plugin-runtime/limiter/ -bench . -benchmem -run '^$'
// 基线记录于 resource-limiter-integration-plan.md §3.9（每次变更后回填）。
// ─────────────────────────────────────────────────────────────────────────────

// BenchmarkNewResourceLimiter 构造成本（含 cgroup 探测/目录创建路径，macOS 上
// 走 monitor 分支；Linux 容器上含 subtree_control 写入）。
func BenchmarkNewResourceLimiter(b *testing.B) {
	limits := ResourceLimits{
		Memory:       &MemoryLimit{Limit: "512M"},
		CPU:          &CPULimit{Limit: "50%"},
		MaxProcesses: 10,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rl, err := NewResourceLimiter("bench", limits)
		if err != nil {
			b.Fatal(err)
		}
		_ = rl.Stop()
	}
}

// BenchmarkApply monitor 模式 Apply 是 no-op（热路径零成本）；cgroup 模式为
// 三个文件写入。Apply 在每次插件启动时执行一次。
func BenchmarkApply(b *testing.B) {
	limits := ResourceLimits{
		Memory:       &MemoryLimit{Limit: "512M"},
		CPU:          &CPULimit{Limit: "50%"},
		MaxProcesses: 10,
	}
	rl, err := NewResourceLimiter("bench-apply", limits)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rl.Stop() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := rl.Apply(nil); err != nil && runtime.GOOS != "linux" {
			// monitor 模式 Apply(nil) 是 no-op nil error；cgroup 模式不 deref
			// cmd（只写 SysProcAttr，见 applyCgroupLimits 尾部）。
			b.Fatal(err)
		}
	}
}

// BenchmarkLimiterMemoryFootprint 单 limiter 常驻内存（验收线 <10MB/plugin）。
// 采样：构造后读 HeapAlloc 增量（含 struct/monitor 通道/采样缓冲）。
func BenchmarkLimiterMemoryFootprint(b *testing.B) {
	limits := ResourceLimits{Memory: &MemoryLimit{Limit: "512M"}}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	rl, err := NewResourceLimiter("bench-mem", limits)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rl.Stop() }()

	// 模拟一次采样写回（lastStats 分配路径）
	rl.mu.Lock()
	rl.lastStats = &ProcessStats{}
	rl.mu.Unlock()

	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc
	b.ReportMetric(float64(delta), "alloc-bytes/op")
	if delta > 10<<20 {
		b.Fatalf("limiter footprint %d bytes exceeds 10MB budget", delta)
	}
}

// BenchmarkReadCgroupUint cgroup 读回解析成本（§5.3 采样路径，5s 一次 ×每插件）。
func BenchmarkReadCgroupUint(b *testing.B) {
	rl, err := NewResourceLimiter("bench-read", ResourceLimits{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rl.Stop() }()
	if rl.cgroupPath == "" {
		b.Skip("no cgroup on this platform; parse cost negligible for monitor mode")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readCgroupUint(rl.cgroupPath, "memory.current")
	}
}
