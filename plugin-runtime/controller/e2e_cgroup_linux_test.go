package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	// 128M / 25% / 64：memory/cpu 用小值便于精确断言（§4.2 验收值）；
	// max_processes 取 64——裸金属多核宿主上 CGO Go 进程 runtime 启动即需
	// 多线程（GOMAXPROCS=核数），pids.max=3 会在 pthread_create 直接 SIGABRT
	// （211 PVE 宿主 32 核实测撞死；容器虚拟化层线程少侥幸通过）。断言跟随
	// 声明值，不硬编码 3。
	limits := `
  resource_limits_enabled: true
  default_resource_limits:
    memory:
      limit: "128M"
    cpu:
      limit: "25%"
    max_processes: 64
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
			// memory.high 内核按 page（4K）向下取整：120795955 → 120795136。
			wantHigh := (134217728 * 90 / 100) &^ 4095 // floor to page size
			if got := readCgroupFile(t, filepath.Join(cgDir, "memory.high")); got != strconv.Itoa(wantHigh) {
				t.Errorf("memory.high = %q, want %d (90%% of limit, page-aligned)", got, wantHigh)
			}
			if got := readCgroupFile(t, filepath.Join(cgDir, "memory.swap.max")); got != "0" {
				t.Errorf("memory.swap.max = %q, want 0 (default no swap)", got)
			}

			// 3) cpu.max = 25% → "25000 100000"
			if got := readCgroupFile(t, filepath.Join(cgDir, "cpu.max")); got != "25000 100000" {
				t.Errorf("cpu.max = %q, want %q", got, "25000 100000")
			}

			// 4) pids.max = 64（声明值；fixtures 上文注释——CGO Go 进程需要
			//    线程配额，不能压到 3）
			if got := readCgroupFile(t, filepath.Join(cgDir, "pids.max")); got != "64" {
				t.Errorf("pids.max = %q, want 64", got)
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

	// 7) 停止后 cgroup 清理（§8.1：插件停止时 cgroup 正确清理）。
	// 已知环境限制：Docker Desktop 的 cgroup v2 虚拟化层即使目录已空也拒绝
	// unlinkat（operation not permitted）——裸金属/常规委派主机不受影响。
	// 为不掩盖真实回归，仍以目录消失为断言；虚拟化层拒删时给出 SKIP 建议
	// 而非静默降级。
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
		t.Errorf("cgroup dir for 'one' not removed within 5s after Stop " +
			"(virtualization layers like Docker Desktop may deny unlinkat — rerun on bare-metal/delegated host)")
	}
}

// TestE2ECgroupOOMHelper 是 OOM 测试的子进程形态：分配并触碰 64MB 内存。
// 通过 `go test -run TestE2ECgroupOOMHelper` 由 TestE2ECgroupOOMKill 以
// 自身二进制 exec 拉起（controller helper 同款模式），避免给仓库引入
// 额外的内存炸弹可执行物。
func TestE2ECgroupOOMHelper(t *testing.T) {
	if os.Getenv("OOM_HELPER") == "" {
		t.Skip("helper process only")
	}
	buf := make([]byte, 64<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1 // 触碰每个页，强制物理分配
	}
	time.Sleep(30 * time.Second) // 保持存活，等内核 OOM 判定
	os.Exit(0)
}

// TestE2ECgroupOOMKill 验证 OOM 行为（§8.1）：独立 cgroup + 极小 memory.max，
// 子进程分配触碰 64MB 内存 → 内核 OOM kill → memory.events oom_kill ≥ 1。
// 不动 controller fixture 的 helper（那是所有控制器测试共用的进程模板，
// 塞内存炸弹会污染全包测试）——本测试用独立 cgroup 自证内核语义。
func TestE2ECgroupOOMKill(t *testing.T) {
	requireLinuxCgroupWrite(t)

	dir := filepath.Join(e2eCgroupRoot, "e2e-oom-probe")
	_ = os.Remove(dir) // 上轮残留清理（有进程占用时 rmdir 失败无害）
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("cannot create probe cgroup: %v", err)
	}

	write := func(name, val string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// 8MB 硬限（控制器进程自身内存远大于此，所以吃内存的必须是子进程）。
	write("memory.max", "8388608")

	// 吃内存子进程：测试二进制自拉起 helper（分配触碰 64MB）。
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	oom := exec.Command(exe, "-test.run=^TestE2ECgroupOOMHelper$", "-test.timeout=60s")
	oom.Env = append(os.Environ(), "OOM_HELPER=1")
	if err := oom.Start(); err != nil {
		t.Fatalf("start oom helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(oom.Process.Pid)), 0o644); err != nil {
		_ = oom.Process.Kill()
		t.Fatalf("assign helper pid: %v", err)
	}

	// 轮询 oom_kill ≥ 1（内核 kill + 计数更新有延迟）。
	killed := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		events := readCgroupFile(t, filepath.Join(dir, "memory.events"))
		if line := eventsLine(events, "oom_kill"); line != "" {
			if n, err := strconv.Atoi(line); err == nil && n >= 1 {
				t.Logf("oom_kill count = %d — kernel OOM semantics confirmed (helper pid %d killed by memory.max=8MB)", n, oom.Process.Pid)
				killed = true
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = oom.Process.Kill()
	_ = oom.Wait()
	if !killed {
		t.Fatal("oom_kill never incremented within 30s — kernel did not OOM the 64MB allocation under 8MB limit")
	}

	// 清理：组内进程已死，直接删组。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Rmdir(dir); err == nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = syscall.Rmdir(dir) // 尽力而为；残留不判失败（环境性 EBUSY）
}

// ── helpers ──

func eventsLine(events, key string) string {
	for _, line := range strings.Split(events, "\n") {
		if strings.HasPrefix(line, key+" ") {
			return strings.TrimSpace(line[len(key)+1:])
		}
	}
	return ""
}
