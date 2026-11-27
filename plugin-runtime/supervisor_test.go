package pluginruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/gocache"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
)

// gocacheRoot 返回仓根（★ 用于定位本包的【专用 GOCACHE】目录）。
func gocacheRoot(t *testing.T) string {
	t.Helper()
	root, err := gocache.RepoRoot()
	if err != nil {
		t.Fatalf("解析仓根失败：%v", err)
	}
	return root
}

func TestSupervisor_StartStop(t *testing.T) {
	fake := &fakeProc{}
	s := NewSupervisor(SupervisorConfig{
		SocketDir:     t.TempDir(),
		ContextSecret: []byte("s"),
	})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		fake.socketPath = socketPath
		return fake
	}

	st, err := s.Start(context.Background(), &Manifest{PluginID: "p1", PluginVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.Status != "starting" && st.Status != "ready" {
		t.Fatalf("status = %q", st.Status)
	}
	if !fake.started {
		t.Fatal("process not started")
	}
	if err := s.Stop("p1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !fake.stopped {
		t.Fatal("process not stopped")
	}
}

type fakeProc struct {
	socketPath string
	started    bool
	stopped    bool
	startErr   error
	startCount int
}

func (f *fakeProc) Start(ctx context.Context) error {
	f.startCount++
	f.started = true
	return f.startErr
}
func (f *fakeProc) Wait() error { return nil }
func (f *fakeProc) Stop() error { f.stopped = true; return nil }
func (f *fakeProc) Pid() int    { return 0 }

// 资源竞争下的落盘预算。helper 在正常路径下是**数十毫秒**内落盘
// （本机实测：两个用例单独跑 5/5 通过，1.1s 内跑完），所以放宽预算**不会**让
// 通过变慢——waitForFile 一看到文件就返回，预算只在失败/卡顿时才被耗尽。
//
// 为什么需要这么宽：本包在 make test 的全仓并发下实测耗时 21.9s~48.5s，
// 而单独跑只要 1.1s（20~48 倍差）。这两个用例各自会 `go build` 一个 helper
// 再 exec 起来，此时 Go 工具链正被全仓几百个包的编译抢占，新起的进程要等
// 到调度。原预算 15s 在这个包络里是**贴边**的：
//   - 2026-10-01 23:37 TestExecCommand_StartsRealProcess 红（整条 16.76s）
//   - 2026-10-02 00:11 TestExecCommand_GracefulStopSIGTERM 红（整条 15.27s）
//
// 两次都恰好卡在 15s 预算上，且两次之间**没有任何 Go 代码改动**。
// 注意这与 ctx 杀进程（另一处已修的缺陷）是**两个独立机制**——后者已单独立证。
const (
	// spawnBudget：等 helper 启动并落盘。
	spawnBudget = 60 * time.Second
	// stopGraceBudget：等 Stop() 走完 5s SIGTERM 优雅退出窗口并写出标记。
	stopGraceBudget = 20 * time.Second
)

// waitForFile 轮询等待 path 出现，直到 timeout。返回是否存在。
// 正常路径下 helper 在数十毫秒内落盘，此处只在冷缓存 / 全仓并发
// 等资源竞争时等待更久，避免硬编码短截止导致的 flaky。
func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}

func TestExecCommand_StartsRealProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：exec 依赖可执行位（Windows 只认 PATHEXT/扩展名）；R50 遗留 #9 skip 化治理")
	}
	helperSrc := filepath.Join(t.TempDir(), "helper.go")
	helperBin := filepath.Join(t.TempDir(), "helper")
	pidFile := filepath.Join(t.TempDir(), "pid")
	os.WriteFile(helperSrc, []byte(`package main
import ("os";"time")
func main(){ _=os.WriteFile(os.Getenv("PID_FILE"), []byte("alive"), 0644); time.Sleep(30*time.Second) }
`), 0644)
	build := exec.Command("go", "build", "-o", helperBin, helperSrc)
	// ★★ r1047 轮（★ 2026-10-28）：★ 子构建走【专用 GOCACHE】⇒ ⇒ 不再往用户
	// ★★ 的全局缓存里写产物（★ 理由与读数见 `internal/gocache` 的包注释）。
	build.Env = append(gocache.Env(gocacheRoot(t), "plugin-runtime"), "GO111MODULE=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}

	c := newExecCommand("", helperBin, []string{"PID_FILE=" + pidFile}, nil)
	// ctx 的寿命必须覆盖下面 waitForFile 的等待窗口。exec.CommandContext 在
	// ctx 到期时会**杀掉子进程**：旧写法 ctx=10s 而 waitForFile 等 15s，于是最后
	// 5 秒是在等一个已经被 ctx 杀掉的进程写的文件，必然等不到。满负载下子进程
	// 调度慢，10s 还没写完就被杀，这条门就红成「helper process did not start
	// (no pid file)」——实测单跑 5/5 通过、全量并行下偶发红（整条耗时从 ~1s
	// 涨到 16.76s，即 15s 全耗在等一个死进程）。同文件下方的优雅停止用例用的
	// 是 context.Background()，没有这个错配。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	if !waitForFile(pidFile, spawnBudget) {
		t.Fatalf("helper process did not start within %v (no pid file)", spawnBudget)
	}
	if c.Pid() == 0 {
		t.Fatal("Pid should be set after Start")
	}
}

func TestSupervisor_StartSetsManifestEnvAndVerifiesSignature(t *testing.T) {
	var capturedEnv []string
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		capturedEnv = env
		return &fakeProc{}
	}
	// Manifest with ManifestPath set (as LoadManifest would); no pubkey → signature skipped
	m := &Manifest{
		PluginID:      "p1",
		PluginVersion: "1",
		Runtime:       Runtime{Entrypoint: "/bin/x"},
		ManifestPath:  "/abs/plugin-manifest.json",
	}
	st, err := s.Start(context.Background(), m)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = st
	// MANIFEST env must be the absolute path
	foundManifest := ""
	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "AI_SESSION_MANAGER_MANIFEST=") {
			foundManifest = strings.TrimPrefix(e, "AI_SESSION_MANAGER_MANIFEST=")
			break
		}
	}
	if foundManifest != "/abs/plugin-manifest.json" {
		t.Fatalf("AI_SESSION_MANAGER_MANIFEST env = %q, want /abs/plugin-manifest.json", foundManifest)
	}
}

func TestSupervisor_StartRejectsBadSignature(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{
		SocketDir:     t.TempDir(),
		SigningPubkey: "deadbeef", // invalid pubkey length → VerifyManifestSignature returns error
	})
	// fakeFactory never called because Start should reject before exec
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		t.Fatal("exec should not happen when signature verification fails")
		return &fakeProc{}
	}
	m := &Manifest{PluginID: "p1", ManifestPath: "/nonexistent/plugin-manifest.json"}
	_, err := s.Start(context.Background(), m)
	if err == nil {
		t.Fatal("Start should reject when signature verification fails")
	}
}

func TestNewSupervisor_CreatesSocketDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "sockets") // does not exist
	_ = NewSupervisor(SupervisorConfig{SocketDir: dir})
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("NewSupervisor should create SocketDir, got %v", err)
	}
}

func TestExecCommand_GracefulStopSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：Windows 无 SIGTERM，优雅停止语义不可复现；R50 遗留 #9 skip 化治理")
	}
	// helper: registers SIGTERM handler; on signal writes marker file and exits cleanly.
	// Writes a ".armed" marker immediately after signal.Notify so the test can wait
	// deterministically (a fixed sleep is flaky because the freshly-compiled helper
	// binary can take >300ms to reach main under go test load).
	helperSrc := filepath.Join(t.TempDir(), "h.go")
	helperBin := filepath.Join(t.TempDir(), "h")
	marker := filepath.Join(t.TempDir(), "stopped")
	os.WriteFile(helperSrc, []byte(`package main
import ("os";"os/signal";"syscall")
func main(){
	c := make(chan os.Signal, 1); signal.Notify(c, syscall.SIGTERM)
	_ = os.WriteFile(os.Getenv("MARKER")+".armed", []byte("1"), 0644)
	<-c
	_ = os.WriteFile(os.Getenv("MARKER"), []byte("graceful"), 0644)
}`), 0644)
	build := exec.Command("go", "build", "-o", helperBin, helperSrc)
	// ★★ r1047 轮（★ 2026-10-28）：★ 子构建走【专用 GOCACHE】⇒ ⇒ 不再往用户
	// ★★ 的全局缓存里写产物（★ 理由与读数见 `internal/gocache` 的包注释）。
	build.Env = append(gocache.Env(gocacheRoot(t), "plugin-runtime"), "GO111MODULE=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}

	c := newExecCommand("", helperBin, []string{"MARKER=" + marker}, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Wait until the helper has armed its SIGTERM handler, so we don't race the
	// signal against signal.Notify (which would terminate the helper before it
	// could write the graceful marker).
	if !waitForFile(marker+".armed", spawnBudget) {
		t.Fatalf("helper never armed SIGTERM handler within %v", spawnBudget)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// wait for marker (within grace window); stopGraceBudget covers the SIGTERM
	// grace window plus scheduling lag under full-repo parallel load (27th-round
	// audit N6: comment still said "8s" after e5d70de0a widened the budget).
	if !waitForFile(marker, stopGraceBudget) {
		t.Fatalf("graceful stop marker never written within %v", stopGraceBudget)
	}
	got, _ := os.ReadFile(marker)
	if string(got) != "graceful" {
		t.Fatalf("graceful stop marker = %q, want \"graceful\" (SIGTERM may have been skipped, fell back to kill)", string(got))
	}
}

// envHas reports whether env contains name=value matching value.
func envHas(env []string, name, value string) bool {
	prefix := name + "="
	for _, e := range env {
		if e == prefix+value {
			return true
		}
	}
	return false
}

func TestSupervisor_StartDoesNotForwardDatabaseURL(t *testing.T) {
	const dsn = "postgres://asm_dev:x@127.0.0.1:5432/session_manager_dev?sslmode=disable"
	t.Setenv("AI_SESSION_MANAGER_DATABASE_URL", dsn)

	var capturedEnv []string
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		capturedEnv = env
		return &fakeProc{}
	}
	m := &Manifest{PluginID: "asm", PluginVersion: "1", Runtime: Runtime{Entrypoint: "/bin/x"}, ManifestPath: "/abs/plugin-manifest.json"}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "AI_SESSION_MANAGER_DATABASE_URL=") || strings.Contains(e, dsn) {
			t.Fatalf("database URL must not be forwarded: %q", e)
		}
	}
}
func TestSupervisor_RestartStopsOldAndStartsNew(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	var events []string
	var mu sync.Mutex
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &restartFakeProc{
			onStart: func() { mu.Lock(); events = append(events, "start:"+entrypoint); mu.Unlock() },
			onStop:  func() { mu.Lock(); events = append(events, "stop"); mu.Unlock() },
		}
	}
	m := &Manifest{PluginID: "p1", PluginVersion: "1", Runtime: Runtime{Entrypoint: "/bin/x"}}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Restart("p1"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"start:/bin/x", "stop", "start:/bin/x"}
	if len(events) != 3 || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSupervisor_RestartUnknownPlugin(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	if err := s.Restart("nope"); err == nil {
		t.Fatal("Restart of unknown plugin should error")
	}
}

// restartFakeProc implements command, recording Start/Stop events.
type restartFakeProc struct {
	pid     int
	onStart func()
	onStop  func()
}

func (f *restartFakeProc) Start(ctx context.Context) error {
	f.pid = 1
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}
func (f *restartFakeProc) Wait() error { return nil }
func (f *restartFakeProc) Stop() error {
	if f.onStop != nil {
		f.onStop()
	}
	return nil
}
func (f *restartFakeProc) Pid() int { return f.pid }

func TestSupervisor_UpgradeSwitchesManifest(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	var procs []*fakeProc
	sup.commandFactory = func(socketPath, entrypoint string, env []string) command {
		f := &fakeProc{socketPath: socketPath}
		procs = append(procs, f)
		return f
	}

	oldM := &Manifest{PluginID: "asm", PluginVersion: "0.1.0"}
	if _, err := sup.Start(context.Background(), oldM); err != nil {
		t.Fatalf("Start old: %v", err)
	}

	newM := &Manifest{PluginID: "asm", PluginVersion: "0.2.0"}
	if err := sup.Upgrade(context.Background(), newM); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	// new manifest is canonical
	if m := sup.ManifestOf("asm"); m.PluginVersion != "0.2.0" {
		t.Errorf("after upgrade, manifest version = %q, want 0.2.0", m.PluginVersion)
	}
	// two procs were created (old start + new start)
	if len(procs) != 2 {
		t.Errorf("procs created = %d, want 2", len(procs))
	}
	// old proc was stopped, new proc was started
	if !procs[0].stopped {
		t.Error("old proc should have been stopped")
	}
	if !procs[1].started {
		t.Error("new proc should have been started")
	}
}

func TestSupervisor_UpgradeRollbackOnStartFailure(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir()})
	sup.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	oldM := &Manifest{PluginID: "asm", PluginVersion: "0.1.0"}
	if _, err := sup.Start(context.Background(), oldM); err != nil {
		t.Fatalf("Start old: %v", err)
	}

	// swap factory so the next Start fails
	sup.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath, startErr: errors.New("boom")}
	}
	newM := &Manifest{PluginID: "asm", PluginVersion: "0.2.0"}
	err := sup.Upgrade(context.Background(), newM)
	if err == nil {
		t.Fatal("expected Upgrade to fail")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should wrap cause, got: %v", err)
	}
	// rollback: manifest is the old one
	if m := sup.ManifestOf("asm"); m.PluginVersion != "0.1.0" {
		t.Errorf("after failed upgrade, manifest = %q, want 0.1.0 (rollback)", m.PluginVersion)
	}
}

// D-S2（2026-11-14 审计轮）：Start 无「已在运行」互斥时并发 activate 产生
// 两个进程抢同一 <id>.sock，先者被覆盖成不可停止的孤儿进程。变异臂：摘掉
// already-running 守卫 ⇒ 本测试红（第二次 Start 成功）。
func TestSupervisor_StartAlreadyRunningRejected(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	m := &Manifest{PluginID: "dup", PluginVersion: "0.1.0"}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := s.Start(context.Background(), m)
	if err == nil {
		t.Fatal("second Start on running plugin must fail")
	}
	if second != nil {
		t.Fatalf("second Start must not return state, got %v", second)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error should mention already running, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ResourceLimiter 集成（2026-11-27 resource-limiter-integration-plan 阶段 1）
//
// 变异臂说明：摘掉 Start 中「limiter 创建 + 登记」即本组测试红
// （limiters map 恒空）；摘掉 Stop/Restart/Upgrade 的 limiter 清理则
// LimiterOf 断言红。
// ─────────────────────────────────────────────────────────────────────────────

// Manifest 声明 resource_limits 时：Start 创建 limiter 并登记，Stop 清理。
func TestSupervisor_StartWithResourceLimits(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	m := &Manifest{
		PluginID:      "limited",
		PluginVersion: "0.1.0",
		ResourceLimits: &ResourceLimits{
			Memory:       &MemoryLimit{Limit: "128M"},
			CPU:          &CPULimit{Limit: "25%"},
			MaxProcesses: 3,
		},
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	rl := s.LimiterOf("limited")
	if rl == nil {
		t.Fatal("ResourceLimiter not created for manifest with resource_limits")
	}
	if rl.Mode() != limiter.ModeMonitor && rl.Mode() != limiter.ModeCgroup {
		t.Errorf("unexpected limiter mode: %s", rl.Mode())
	}
	if err := s.Stop("limited"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s.LimiterOf("limited") != nil {
		t.Error("limiter should be deregistered after Stop")
	}
}

// 未配置 limits（且无 DefaultLimits）时：不创建 limiter，行为与历史一致。
func TestSupervisor_StartWithoutResourceLimits(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	if _, err := s.Start(context.Background(), &Manifest{PluginID: "plain", PluginVersion: "0.1.0"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if rl := s.LimiterOf("plain"); rl != nil {
		t.Fatal("no limiter should be created without resource_limits config")
	}
	if err := s.Stop("plain"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// DefaultLimits 兜底：Manifest 未声明时应用全局默认；Manifest 声明时优先于默认。
func TestSupervisor_DefaultLimitsFallback(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{
		SocketDir:     t.TempDir(),
		ContextSecret: []byte("s"),
		DefaultLimits: &ResourceLimits{Memory: &MemoryLimit{Limit: "512M"}, MaxProcesses: 10},
	})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	// a) Manifest 无声明 → 用默认
	if _, err := s.Start(context.Background(), &Manifest{PluginID: "fallback", PluginVersion: "0.1.0"}); err != nil {
		t.Fatalf("start fallback: %v", err)
	}
	if s.LimiterOf("fallback") == nil {
		t.Fatal("DefaultLimits should create a limiter when manifest omits resource_limits")
	}
	_ = s.Stop("fallback")

	// b) Manifest 有声明 → Manifest 优先（两者都非 nil，limiter 一定存在；
	//    这里断言的是配置路径不会因双份来源而冲突报错）
	own := &Manifest{
		PluginID:       "own",
		PluginVersion:  "0.1.0",
		ResourceLimits: &ResourceLimits{Memory: &MemoryLimit{Limit: "64M"}, MaxProcesses: 2},
	}
	if _, err := s.Start(context.Background(), own); err != nil {
		t.Fatalf("start own: %v", err)
	}
	if s.LimiterOf("own") == nil {
		t.Fatal("manifest resource_limits should create a limiter even with DefaultLimits set")
	}
	_ = s.Stop("own")
}

// Restart / Upgrade 都绕过 Stop，必须同样清理旧 limiter 并为新进程重建。
func TestSupervisor_RestartUpgradeLimiterLifecycle(t *testing.T) {
	s := NewSupervisor(SupervisorConfig{SocketDir: t.TempDir(), ContextSecret: []byte("s")})
	s.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{socketPath: socketPath}
	}
	m := &Manifest{
		PluginID:       "lc",
		PluginVersion:  "0.1.0",
		Runtime:        Runtime{Entrypoint: "v1"},
		ResourceLimits: &ResourceLimits{MaxProcesses: 4},
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	if s.LimiterOf("lc") == nil {
		t.Fatal("limiter missing after initial start")
	}

	// Restart：旧 limiter 清理，新 limiter 登记
	if err := s.Restart("lc"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if s.LimiterOf("lc") == nil {
		t.Fatal("limiter should be re-created after Restart")
	}

	// Upgrade：同上，且新 manifest 生效
	next := &Manifest{
		PluginID:       "lc",
		PluginVersion:  "0.2.0",
		Runtime:        Runtime{Entrypoint: "v2"},
		ResourceLimits: &ResourceLimits{MaxProcesses: 8},
	}
	if err := s.Upgrade(context.Background(), next); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if s.LimiterOf("lc") == nil {
		t.Fatal("limiter should be re-created after Upgrade")
	}
	if got := s.ManifestOf("lc").PluginVersion; got != "0.2.0" {
		t.Errorf("after upgrade version = %q, want 0.2.0", got)
	}

	// 收口 Stop：limiter 清理
	if err := s.Stop("lc"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s.LimiterOf("lc") != nil {
		t.Error("limiter should be deregistered after final Stop")
	}
}

// 真实 exec 路径：execCommand.prepare 注入 ResourceLimiter.Apply。
// 本机 macOS/非 cgroup 环境 Apply 是 no-op，断言「Start 全链路不被限制配置阻断」；
// prepare 是否真的接到 Apply 由 limiter 包的单元测试覆盖。
func TestSupervisor_RealExecWithLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-exec test in short mode")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	src := filepath.Join(dir, "helper.go")
	if err := os.WriteFile(src, []byte("package main\nimport (\n\t\"os\"\n)\nfunc main() {\n\tos.WriteFile(os.Args[1], []byte(\"ok\"), 0o644)\n\tselect {}\n}\n"), 0o644); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	buildCmd := exec.Command("go", "build", "-o", helper, src)
	// 专用 GOCACHE（isolation 门判据）：随树变化的一次性产物不落用户全局缓存
	//（实测全局缓存一度 72 GB ⇒ 磁盘占满 ⇒ 全量编译阶段红）。
	buildCmd.Env = append(os.Environ(), gocache.Env(gocacheRoot(t), "plugin-runtime")...)
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	s := NewSupervisor(SupervisorConfig{SocketDir: dir, ContextSecret: []byte("s")})
	// 真实 execCommand（不替换 commandFactory）
	m := &Manifest{
		PluginID:       "reallim",
		PluginVersion:  "0.1.0",
		Runtime:        Runtime{Entrypoint: helper},
		ResourceLimits: &ResourceLimits{Memory: &MemoryLimit{Limit: "64M"}, MaxProcesses: 5},
	}
	if _, err := s.Start(context.Background(), m); err != nil {
		t.Fatalf("start real exec with limits: %v", err)
	}
	if s.LimiterOf("reallim") == nil {
		t.Fatal("limiter missing for real-exec start")
	}
	if err := s.Stop("reallim"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
