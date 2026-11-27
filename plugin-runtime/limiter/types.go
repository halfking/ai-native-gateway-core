// Package limiter 将插件进程的资源限制接线到根包 Supervisor。
//
// 移植自子模块 plugin-runtime/supervisor/resource_limiter.go（原依赖
// discovery.ResourceLimits），改为本包自持的 ResourceLimits 线协议类型，
// 与根包 pluginruntime.Manifest 通过类型别名对接（见 types.go）。
//
// Linux 上走 cgroup v2 硬限制；cgroup 不可用时自动降级为监控模式。
// macOS 及其他平台仅监控 + 警告（无硬限制）。
package limiter

// ResourceLimits 定义插件进程的资源限制配置（manifest 中 resource_limits 字段）。
type ResourceLimits struct {
	// Memory 内存限制（可选）
	Memory *MemoryLimit `json:"memory,omitempty"`
	// CPU 限制（可选）
	CPU *CPULimit `json:"cpu,omitempty"`
	// MaxProcesses 最大进程/线程数（默认 10）
	MaxProcesses int `json:"max_processes,omitempty"`
}

// MemoryLimit 定义内存限制。
type MemoryLimit struct {
	// Limit 内存上限，格式：100M, 1G, 512MB, 256Mi
	Limit string `json:"limit"`
	// Swap 是否允许使用 swap（Linux only，默认 false）
	Swap bool `json:"swap,omitempty"`
}

// CPULimit 定义 CPU 限制。
type CPULimit struct {
	// Limit CPU 百分比限制，格式：25%, 50%, 100%
	Limit string `json:"limit"`
}
