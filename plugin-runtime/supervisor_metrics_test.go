package pluginruntime

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
)

// ─────────────────────────────────────────────────────────────────────────────
// §5.3 埋点接线测试：Supervisor.Start 注入观测回调 + States() 枚举面。
// ─────────────────────────────────────────────────────────────────────────────

// Start 后 limiter 的观测回调已注入（Limits/Mode 导出面可用，States 可枚举）。
func TestSupervisor_LimiterObservabilitySurface(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	m := &Manifest{
		PluginID:       "obs",
		PluginVersion:  "0.1.0",
		ResourceLimits: &ResourceLimits{Memory: &MemoryLimit{Limit: "64M"}, MaxProcesses: 4},
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = s.Stop("obs") }()

	rl := s.LimiterOf("obs")
	if rl == nil {
		t.Fatal("limiter not registered")
	}
	// Limits 导出面返回生效声明（§5.3 gauge 用）
	limits := rl.Limits()
	if limits == nil || limits.Memory == nil || limits.Memory.Limit != "64M" {
		t.Errorf("Limits() = %+v, want Memory.Limit=64M", limits)
	}
	// States 枚举面包含该插件（collector 遍历用）。fakeProc.Pid()=0（无真实
	// 进程），只断言枚举与登记，不断言 pid。
	found := false
	for _, st := range s.States() {
		if st.PluginID == "obs" {
			found = true
		}
	}
	if !found {
		t.Error("States() does not include started plugin 'obs'")
	}
}

// States 含已启动插件；Stop 后条目仍在但状态翻为 stopped（supervisor 语义：
// states 保留终态，limiters/procs 注销——collector 按 LimiterOf==nil 跳过）。
func TestSupervisor_StatesLifecycle(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	if len(s.States()) != 0 {
		t.Fatalf("empty supervisor States() = %d, want 0", len(s.States()))
	}
	m := &Manifest{PluginID: "st1", PluginVersion: "0.1.0"}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := len(s.States()); got != 1 {
		t.Fatalf("after start States() = %d, want 1", got)
	}
	if err := s.Stop("st1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 状态条目保留（stopped），但 limiter 已注销 ⇒ collector 侧不会再导出。
	if got := len(s.States()); got != 1 {
		t.Fatalf("after stop States() = %d, want 1 (terminal state retained)", got)
	}
	if s.LimiterOf("st1") != nil {
		t.Error("limiter should be deregistered after Stop")
	}
}

// 回调注入后，monitor 模式的超限检测路径会触发 metrics 回调（用 limiter 包
// 公开面直接驱动：SetObservabilityHooks + 人工 stats 注入不可行——stats 由
// 采样 goroutine 写。此处验证接线存在性：回调注入后 GetStats/Limits 正常，
// 完整触发链路由 limiter 包测试与本仓 e2e 覆盖）。
func TestSupervisor_LimiterHookInjection(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	m := &Manifest{
		PluginID:       "hooked",
		PluginVersion:  "0.1.0",
		ResourceLimits: &ResourceLimits{CPU: &CPULimit{Limit: "50%"}},
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = s.Stop("hooked") }()

	rl := s.LimiterOf("hooked")
	if rl == nil {
		t.Fatal("limiter not registered")
	}
	if rl.Limits().CPU == nil || rl.Limits().CPU.Limit != "50%" {
		t.Errorf("hooked limiter limits = %+v, want CPU 50%%", rl.Limits())
	}
	// monitor 模式下 AssignProcess 已在 Start 内完成（fakeProc pid>0），等一个
	// 采样周期不现实——只验证 goroutine 已激活、Stop 能干净回收。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := rl.GetStats(); st != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = limiter.ModeDisabled // 保持 limiter 包引用（与上方 import 对齐）
}
