package pluginruntime

import (
	"context"
	"testing"
)

// BenchmarkSupervisorStartWithLimits §8.2：带资源限制的插件启动开销
// （验收线：插件启动时间增加 <50ms）。对照基准是不带限制的 Start——两者
// 之差即 ResourceLimiter 接线的净开销（limiter 构造 + prepare 注入 +
// AssignProcess）。fakeProc pid>0，monitor 路径包含采样 goroutine 拉起。
func BenchmarkSupervisorStartWithLimits(b *testing.B) {
	s := NewSupervisor(SupervisorConfig{SocketDir: b.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &benchProc{}
	}
	m := &Manifest{
		PluginID:       "bench-limited",
		PluginVersion:  "0.1.0",
		ResourceLimits: &ResourceLimits{Memory: &MemoryLimit{Limit: "128M"}, MaxProcesses: 3},
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Start(ctx, m); err != nil {
			b.Fatal(err)
		}
		if err := s.Stop("bench-limited"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSupervisorStartBaseline 不带 resource_limits 的历史行为对照。
func BenchmarkSupervisorStartBaseline(b *testing.B) {
	s := NewSupervisor(SupervisorConfig{SocketDir: b.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &benchProc{}
	}
	m := &Manifest{PluginID: "bench-plain", PluginVersion: "0.1.0"}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Start(ctx, m); err != nil {
			b.Fatal(err)
		}
		if err := s.Stop("bench-plain"); err != nil {
			b.Fatal(err)
		}
	}
}

// benchProc 是 benchmark 用的轻量 command（pid 恒定>0，无 socket 行为）。
type benchProc struct{}

func (f *benchProc) Start(_ context.Context) error { return nil }
func (f *benchProc) Wait() error                   { return nil }
func (f *benchProc) Stop() error                   { return nil }
func (f *benchProc) Pid() int                      { return 4321 }
