package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
)

// ─────────────────────────────────────────────────────────────────────────────
// §4.2 集成 + §8.1 功能验收（Linux only）：cgroup 硬限制真机端到端。
//
// 规划文档 §4.2/§4.3 要求在 Linux 环境实测：
//   - cgroup 目录创建（/sys/fs/cgroup/llm-gateway-plugins/<plugin_id>）
//   - memory.max / cpu.max / pids.max 三文件写入值正确
//   - 进程被分配进 cgroup（cgroup.procs 含插件 pid）
//   - OOM 行为（memory.events oom_kill 计数）
//
// macOS 开发机无 cgroup（本测试 Skip）；Linux 测试机以 root 或 cgroup 委派
// 环境运行 `go test ./plugin-runtime/controller/ -run TestE2ECgroup`。
// 无 cgroup 写权限时降级 Skip——CI 容器常见（需要 privileged 或委派），不算失败。
// scripts/test-resource-limits.sh 是本测试的 shell 包装（带结果汇总）。
// ─────────────────────────────────────────────────────────────────────────────

const e2eCgroupRoot = "/sys/fs/cgroup/llm-gateway-plugins"

// requireLinuxCgroupWrite 在无 Linux cgroup v2 写权限的环境直接 Skip。
func requireLinuxCgroupWrite(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("cgroupv2 not available (not Linux or not mounted)")
	}
	// 探测写权限：尝试建一层受控子目录并立即清理。
	probe := filepath.Join(e2eCgroupRoot, "e2e-probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Skipf("no write access to %s (need root or cgroup delegation): %v", e2eCgroupRoot, err)
	}
	_ = os.Remove(probe)
}

// readCgroupFile 读 cgroup 控制文件全文（trim 过）。
func readCgroupFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

// TestE2ECgroupHardLimits 全链路：controller 扫描（含默认限制合并）→
// supervisor 拉起真实插件进程 → cgroup 三文件值核对 → 进程归组 → 停止清理。
func TestE2ECgroupHardLimits(t *testing.T) {
	requireLinuxCgroupWrite(t)

	plugins, path := fixture(t) // two real helper-process plugins, bundle 无限制
	// 128M / 25% / 3：值足够小便于精确断言（§4.2 验收值）。
	limits := `
  resource_limits_enabled: true
  default_resource_limits:
    memory:
      limit: "128M"
    cpu:
      limit: "25%"
    max_processes: 3
`
	data := "server:\n  host: localhost\n  port: 8080\nplugins:\n  directory: " + `"` + plugins + `"` + "\n  enabled: [one, two]\n" + limits
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	c := startController(t, path)
	for _, id := range []string{"one", "two"} {
		if c.BaseURL(id) == "" {
			t.Fatalf("plugin %s did not become ready", id)
		}
	}

	sup := c.Supervisor()
	for _, id := range []string{"one", "two"} {
		t.Run(id, func(t *testing.T) {
			rl := sup.LimiterOf(id)
			if rl == nil {
				t.Fatalf("plugin %s has no limiter", id)
			}
			if rl.Mode() != limiter.ModeCgroup {
				t.Skipf("plugin %s limiter degraded to %s (no cgroup write at NewResourceLimiter time)", id, rl.Mode())
			}
			cgDir := filepath.Join(e2eCgroupRoot, id)

			// 1) cgroup 目录已创建
			if fi, err := os.Stat(cgDir); err != nil || !fi.IsDir() {
				t.Fatalf("cgroup dir %s not created: %v", cgDir, err)
			}

			// 2) memory.max = 128M = 134217728；memory.high = 90% 软限；swap 默认禁用
			if got := readCgroupFile(t, filepath.Join(cgDir, "memory.max")); got != "134217728" {
				t.Errorf("memory.max = %q, want 134217728", got)
			}
			if got := readCgroupFile(t, filepath.Join(cgDir, "memory.high")); got != strconv.Itoa(134217728*90/100) {
				t.Errorf("memory.high = %q, want %d", got, 134217728*90/100)
			}
			if got := readCgroupFile(t, filepath.Join(cgDir, "memory.swap.max")); got != "0" {
				t.Errorf("memory.swap.max = %q, want 0 (default no swap)", got)
			}

			// 3) cpu.max = 25% → "25000 100000"
			if got := readCgroupFile(t, filepath.Join(cgDir, "cpu.max")); got != "25000 100000" {
				t.Errorf("cpu.max = %q, want %q", got, "25000 100000")
			}

			// 4) pids.max = 3
			if got := readCgroupFile(t, filepath.Join(cgDir, "pids.max")); got != "3" {
				t.Errorf("pids.max = %q, want 3", got)
			}

			// 5) 进程已归组：cgroup.procs 非空（插件进程或其线程已被收编）。
			//    controller 的 health 拉起路径里 AssignProcess 已执行；helper 的
			//    子线程也随 leads 读入。逐条列出便于真机排障。
			procs := readCgroupFile(t, filepath.Join(cgDir, "cgroup.procs"))
			if procs == "" {
				t.Errorf("cgroup.procs empty; plugin pid should be assigned")
			} else {
				t.Logf("cgroup.procs: %s", strings.ReplaceAll(procs, "\n", " "))
			}

			// 6) memory.events 可读且含 oom_kill 行（§8.1 OOM 观察点；此测试不
			//    触发 OOM，只验证计数器通道存在。真 OOM 见 TestE2ECgroupOOMKill）。
			events := readCgroupFile(t, filepath.Join(cgDir, "memory.events"))
			if !strings.Contains(events, "oom_kill") {
				t.Errorf("memory.events missing oom_kill line: %q", events)
			} else {
				t.Logf("memory.events: %s", strings.ReplaceAll(events, "\n", " "))
			}
		})
	}

	// 7) 停止后 cgroup 清理（§8.1：插件停止时 cgroup 正确清理）
	if err := sup.Stop("one"); err != nil {
		t.Fatalf("stop one: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	removed := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(e2eCgroupRoot, "one")); os.IsNotExist(err) {
			removed = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !removed {
		t.Errorf("cgroup dir for 'one' not removed within 5s after Stop")
	}
}

// TestE2ECgroupOOMKill 验证 OOM 行为（§8.1）：独立 cgroup + 极小 memory.max，
// 子进程吃内存超限 → 内核 OOM kill → memory.events oom_kill ≥ 1。
// 不动 controller fixture 的 helper（那是所有控制器测试共用的进程模板，
// 塞内存炸弹会污染全包测试）——本测试用 limiter 包直接建 cgroup 自证内核语义。
func TestE2ECgroupOOMKill(t *testing.T) {
	requireLinuxCgroupWrite(t)

	dir := filepath.Join(e2eCgroupRoot, "e2e-oom-probe")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("cannot create probe cgroup: %v", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(dir)
		}
	}()

	write := func(name, val string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// 8MB 硬限（控制器进程自身内存远大于此，所以吃内存的必须是子进程）。
	write("memory.max", "8388608")
	// 不写 memory.high——让硬限直接 OOM，验证 oom_kill 路径而非回收路径。

	// 起 probe 主进程（sh 睡循环），拿 pid 后归组进 cgroup。
	setup := fmt.Sprintf("sh -c 'sleep 30 & echo $! > %s; wait'", filepath.Join(dir, "self_pid"))
	if err := runSh(setup); err != nil {
		t.Fatalf("start probe process: %v", err)
	}
	pidData, err := os.ReadFile(filepath.Join(dir, "self_pid"))
	if err != nil {
		t.Fatalf("read self_pid: %v", err)
	}
	pid := strings.TrimSpace(string(pidData))
	if pid == "" {
		t.Fatal("probe process did not write its pid")
	}
	// probe 主进程归组（其后续子进程继承 cgroup）。
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
		t.Fatalf("assign probe pid: %v", err)
	}

	// 归组吃内存子进程：直接把一个新 sh（吃内存）写进 cgroup.procs。
	oomSh := fmt.Sprintf("sh -c 'dd if=/dev/zero bs=1M count=64 2>/dev/null | cat > /dev/null' & echo $! > %s", filepath.Join(dir, "oom_pid"))
	if err := os.WriteFile(filepath.Join(dir, "starter.sh"), []byte(oomSh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runSh(fmt.Sprintf("nohup %s >/dev/null 2>&1 &", filepath.Join(dir, "starter.sh"))); err != nil {
		t.Fatalf("start oom process: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	oomPid := strings.TrimSpace(mustRead(t, filepath.Join(dir, "oom_pid")))
	if oomPid != "" {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(oomPid), 0o644); err != nil {
			t.Fatalf("assign oom pid: %v", err)
		}
	}

	// 轮询 oom_kill ≥ 1（内核 kill + 计数更新有延迟）。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		events := readCgroupFile(t, filepath.Join(dir, "memory.events"))
		if line := eventsLine(events, "oom_kill"); line != "" {
			if n, err := strconv.Atoi(line); err == nil && n >= 1 {
				t.Logf("oom_kill count = %d — kernel OOM semantics confirmed (pid %s killed by memory.max=8MB)", n, oomPid)
				cleanup = false
				// 有存活 probe 进程需先清（sleep 30）再删目录。
				_ = runSh(fmt.Sprintf("kill -9 %s 2>/dev/null; true", pid))
				_ = os.Remove(dir)
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("oom_kill never incremented within 15s — kernel did not OOM the 64MB allocation under 8MB limit")
}

// ── helpers ──

func runSh(cmd string) error {
	c := exec.Command("sh", "-c", cmd)
	return c.Run()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func eventsLine(events, key string) string {
	for _, line := range strings.Split(events, "\n") {
		if strings.HasPrefix(line, key+" ") {
			return strings.TrimSpace(line[len(key)+1:])
		}
	}
	return ""
}
