package limiter

import (
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

// 本文件钉住「采样 goroutine 生命周期」的三条不变量。
//
// ★ 背景（2026-11-28，12h 审计轮 P1）：ResourceLimiter 的采样 goroutine
//   （monitorProcess / monitorCgroup）此前**在 cgroup 模式下永不退出**——
//   Stop() 里的 close(r.monitorCancel) 写在 `if r.mode == ModeMonitor`
//   分支内 ⇒ 生产平台（Linux + cgroupv2）每停一个插件就泄漏一个 goroutine
//   + 一个 5s ticker。泄漏的 goroutine 还在每 5s 读已被 rmdir 的 cgroup
//   文件（读失败被静默忽略）⇒ 表现为「goroutine 数单调增长 + 无声空转」。
//
// ★ 同时钉住两条被 P1 修复连带收口的隐患：
//   ① monitorDone 原先**只有** monitorProcess 会关闭 ⇒ cgroup 模式的
//      monitorCgroup 不关 ⇒ Stop() 一旦在 cgroup 模式下等它就会永久挂住；
//   ② close(r.monitorCancel) 原先走 `select { case <-ch: default: close(ch) }`
//      ——该判据在并发 Stop 下不成立（两个 Stop 同时落在 default 分支 ⇒
//      双重 close ⇒ panic）。

// startSamplerForTest 起一个真实进程并分配给 limiter，让采样 goroutine 真正跑起来。
func startSamplerForTest(t *testing.T, name string) (*ResourceLimiter, func()) {
	t.Helper()
	rl, err := NewResourceLimiter(name, ResourceLimits{MaxProcesses: 5})
	if err != nil {
		t.Fatalf("NewResourceLimiter failed: %v", err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process failed: %v", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	if err := rl.AssignProcess(cmd.Process.Pid); err != nil {
		cleanup()
		t.Fatalf("AssignProcess failed: %v", err)
	}
	// 等一拍，让采样循环真正进入 select（否则 Stop 可能早于 goroutine 起跑）。
	time.Sleep(100 * time.Millisecond)
	return rl, cleanup
}

// TestStopTerminatesSamplingGoroutine 钉住：Stop() 之后采样 goroutine 必须退出
// （monitorDone 被关闭）。这是 cgroup 模式泄漏的**可观测代理**——
// 只要 Stop() 把取消信号送达并等到了 monitorDone，两种模式的 goroutine
// 都不可能残留。
func TestStopTerminatesSamplingGoroutine(t *testing.T) {
	rl, cleanup := startSamplerForTest(t, "stop-terminates")
	defer cleanup()

	if err := rl.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	select {
	case <-rl.monitorDone:
		// 期望路径：采样 goroutine 已退出
	case <-time.After(3 * time.Second):
		t.Fatalf("Stop() 之后采样 goroutine 未退出（monitorDone 未关闭）——" +
			"取消信号未送达或 goroutine 未关闭 monitorDone ⇒ goroutine 泄漏")
	}
}

// TestStopCancelsSamplingInEveryMode 钉住缺陷本身：**Stop() 必须向采样
// goroutine 送达取消信号，且这个义务与运行模式无关**。
//
// ★ 为什么要按模式逐个跑：缺陷原貌是「close(monitorCancel) 写在
//
//	`if r.mode == ModeMonitor` 分支内」⇒ 只有 cgroup 模式中招。非 Linux
//	机器上 NewResourceLimiter 恒为 monitor 模式 ⇒ 一条只跑 monitor 模式的
//	测试对本次缺陷**恒绿**（变异自证实测：把修复改回按模式分支，monitor
//	模式的测试照样 PASS ⇒ 门是空的）。
//	本测试在真实采样 goroutine 起来之后**强制切换 rl.mode**，
//	于是「Stop 是否送达取消」在任意平台都能暴露按模式分支的写法。
//	（mode 是同包私有字段，测试直接改写是本仓既有手法。）
func TestStopCancelsSamplingInEveryMode(t *testing.T) {
	for _, mode := range []LimiterMode{ModeMonitor, ModeCgroup} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			rl, cleanup := startSamplerForTest(t, "cancel-in-every-mode")
			defer cleanup()

			// 采样 goroutine 已在跑（monitor 模式起的是 monitorProcess）。
			// 此后把模式改成被测值：Stop() 的取消义务必须仍然成立。
			rl.mode = mode

			if err := rl.Stop(); err != nil {
				t.Errorf("Stop failed: %v", err)
			}

			select {
			case <-rl.monitorCancel:
			case <-time.After(2 * time.Second):
				t.Fatalf("模式 %s 下 Stop() 未向采样 goroutine 送达取消信号"+
					"（monitorCancel 未关闭）⇒ 该模式下采样 goroutine 永不退出", mode)
			}

			select {
			case <-rl.monitorDone:
			case <-time.After(2 * time.Second):
				t.Fatalf("模式 %s 下 Stop() 之后采样 goroutine 未退出"+
					"（monitorDone 未关闭）⇒ goroutine 泄漏", mode)
			}
		})
	}
}

// TestStopCgroupModeTerminatesSamplingGoroutine 同上，但在**真机 cgroup 模式**
// 下验证——这正是 P1 缺陷所在的模式（生产平台 Linux）。
// 非 Linux 或 cgroupv2 不可用时跳过（本地 macOS 跑不了这一支）。
func TestStopCgroupModeTerminatesSamplingGoroutine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup 模式仅 Linux 可用")
	}
	rl, cleanup := startSamplerForTest(t, "stop-terminates-cgroup")
	defer cleanup()

	if rl.Mode() != ModeCgroup {
		t.Skip("cgroupv2 在本机不可用（已回退到 monitor 模式）")
	}

	if err := rl.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
	select {
	case <-rl.monitorDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("cgroup 模式下 Stop() 之后采样 goroutine 未退出（monitorDone 未关闭）")
	}
}

// TestStopDoesNotLeakSamplingGoroutine 钉住「停 N 次插件，goroutine 数不增长」。
// 这是缺陷在生产上的真实症状（插件重启/升级反复发生 ⇒ 单调增长）。
func TestStopDoesNotLeakSamplingGoroutine(t *testing.T) {
	// 先让后台噪声落定（runtime 自身的 goroutine 也会漂）。
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		rl, cleanup := startSamplerForTest(t, "leak-check")
		if err := rl.Stop(); err != nil {
			t.Errorf("Stop failed: %v", err)
		}
		cleanup()
	}

	// 采样 goroutine 退出前有一个调度延迟 ⇒ 轮询等它回落。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("Stop() 之后 goroutine 数未回落：before=%d after=%d ⇒ 采样 goroutine 泄漏",
		before, runtime.NumGoroutine())
}

// TestConcurrentStopDoesNotPanic 钉住：并发 Stop() 不得 panic。
// 双重 close(r.monitorCancel) 会 panic——修法是 stopOnce（结构上排除）。
func TestConcurrentStopDoesNotPanic(t *testing.T) {
	rl, cleanup := startSamplerForTest(t, "concurrent-stop")
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rl.Stop()
		}()
	}
	wg.Wait()

	select {
	case <-rl.monitorDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("并发 Stop() 之后采样 goroutine 未退出")
	}
}
