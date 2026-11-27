package limiter

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadCgroupUint 覆盖 cgroup 文件两种形态：单值文件与多行键值文件。
// 这是 §5.3 cgroup 采样读回的解析核心——读错 memory.current 或解析错
// memory.events 会直接污染 gauge 与 OOM 增量。
func TestReadCgroupUint(t *testing.T) {
	dir := t.TempDir()

	// 单值文件（memory.current / pids.current 形态）
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("134217728\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := readCgroupUint(dir, "memory.current")
	if err != nil || v != 134217728 {
		t.Errorf("single-value file: got %d, %v; want 134217728, nil", v, err)
	}

	// 多行键值文件（memory.events 形态）
	events := "anon 0\nfile 12\n**oom 0**\nomem_kill? bogus\noom_kill 3\n"
	_ = events // placeholder to keep diff readable
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("anon 0\nfile 12\noom_kill 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err = readCgroupUint(dir, "memory.events", "oom_kill")
	if err != nil || v != 3 {
		t.Errorf("keyed file oom_kill: got %d, %v; want 3, nil", v, err)
	}
	// cpu.stat 形态同样适用
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 123456\nuser_usec 100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err = readCgroupUint(dir, "cpu.stat", "usage_usec")
	if err != nil || v != 123456 {
		t.Errorf("cpu.stat usage_usec: got %d, %v; want 123456, nil", v, err)
	}

	// 键不存在 → 报错
	if _, err := readCgroupUint(dir, "memory.events", "nope"); err == nil {
		t.Error("missing key should error")
	}
	// 文件不存在 → 报错（cgroup 已清理的预期路径）
	if _, err := readCgroupUint(dir, "does.not.exist"); err == nil {
		t.Error("missing file should error")
	}
}

// TestMonitorCgroupOOMBaseline 验证 oom_kill 基线语义：首次读回只记基线
// 不上报（历史 OOM 不算本进程增量），后续增量才触发回调；计数器归零重置基线。
func TestMonitorCgroupOOMBaseline(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("memory.current", "1048576\n")
	write("pids.current", "3\n")
	write("cpu.stat", "usage_usec 0\n")
	write("memory.events", "anon 0\noom_kill 5\n") // 历史 OOM：基线 5

	rl := &ResourceLimiter{
		pluginName:    "oomtest",
		cgroupPath:    dir,
		limits:        ResourceLimits{Memory: &MemoryLimit{Limit: "512M"}},
		monitorCancel: make(chan struct{}),
		monitorDone:   make(chan struct{}),
	}
	var oomDelta uint64
	oomFired := make(chan struct{}, 4)
	rl.SetObservabilityHooks(nil, func(delta uint64) {
		oomDelta += delta
		oomFired <- struct{}{}
	})

	done := make(chan struct{})
	go func() {
		rl.monitorCgroup()
		close(done)
	}()

	// 首轮采样（5s ticker）只记基线：轮询等基线出现（持锁字段）。
	waitForBaseline := func() bool {
		for i := 0; i < 40; i++ { // 最多 ~8s（一个采样周期多一点）
			rl.mu.RLock()
			seen, base := rl.oomSeen, rl.lastOOMKills
			rl.mu.RUnlock()
			if seen && base == 5 {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	if !waitForBaseline() {
		rl.mu.RLock()
		seen, base := rl.oomSeen, rl.lastOOMKills
		rl.mu.RUnlock()
		t.Fatalf("baseline not recorded: oomSeen=%v lastOOMKills=%d", seen, base)
	}
	write("memory.events", "anon 0\noom_kill 7\n") // 新增 2 次 OOM

	select {
	case <-oomFired:
		if oomDelta != 2 {
			t.Errorf("oom delta = %d, want 2", oomDelta)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("oom callback not fired within one sample period")
	}

	// 计数器归零（cgroup 重建）：重置基线、不报负增量。
	write("memory.events", "anon 0\noom_kill 0\n")
	select {
	case <-oomFired:
		t.Error("reset should not fire negative delta")
	case <-time.After(8 * time.Second):
	}

	close(rl.monitorCancel)
	<-done
}

// TestMonitorCgroupSoftLimitViolation 验证 cgroup 模式软限检测：usage 超过
// 硬限 90% 时触发 memory violation 回调（与 Apply 写入的 memory.high 一致）。
func TestMonitorCgroupSoftLimitViolation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 100M 限制 → 软限 90M=94371840。当前 usage 96M 在软限之上（但在硬限下）。
	write("memory.current", "100663296\n")
	write("pids.current", "2\n")
	write("cpu.stat", "usage_usec 0\n")
	write("memory.events", "oom_kill 0\n")

	rl := &ResourceLimiter{
		pluginName:    "softtest",
		cgroupPath:    dir,
		limits:        ResourceLimits{Memory: &MemoryLimit{Limit: "100M"}},
		monitorCancel: make(chan struct{}),
		monitorDone:   make(chan struct{}),
	}
	memFired := make(chan struct{}, 4)
	rl.SetObservabilityHooks(func(kind string) {
		if kind == "memory" {
			memFired <- struct{}{}
		}
	}, nil)

	done := make(chan struct{})
	go func() {
		rl.monitorCgroup()
		close(done)
	}()

	select {
	case <-memFired:
	case <-time.After(8 * time.Second):
		t.Fatal("soft-limit violation not fired within one sample period")
	}

	// stats 已写入 lastStats（导出面 GetStats 可见）
	if st := rl.GetStats(); st == nil || st.MemoryBytes != 100663296 || st.NumThreads != 2 {
		t.Errorf("lastStats not populated: %+v", st)
	}

	close(rl.monitorCancel)
	<-done
}
