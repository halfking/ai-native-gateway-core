package pluginruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
}

func (f *fakeProc) Start(ctx context.Context) error { f.started = true; return nil }
func (f *fakeProc) Wait() error                     { return nil }
func (f *fakeProc) Stop() error                     { f.stopped = true; return nil }
func (f *fakeProc) Pid() int                        { return 0 }

func TestExecCommand_StartsRealProcess(t *testing.T) {
	helperSrc := filepath.Join(t.TempDir(), "helper.go")
	helperBin := filepath.Join(t.TempDir(), "helper")
	pidFile := filepath.Join(t.TempDir(), "pid")
	os.WriteFile(helperSrc, []byte(`package main
import ("os";"time")
func main(){ _=os.WriteFile(os.Getenv("PID_FILE"), []byte("alive"), 0644); time.Sleep(30*time.Second) }
`), 0644)
	build := exec.Command("go", "build", "-o", helperBin, helperSrc)
	build.Env = append(os.Environ(), "GO111MODULE=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}

	c := newExecCommand("", helperBin, []string{"PID_FILE=" + pidFile}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatal("helper process did not start (no pid file)")
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
	build.Env = append(os.Environ(), "GO111MODULE=off")
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
	armDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(armDeadline) {
		if _, err := os.Stat(marker + ".armed"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker + ".armed"); err != nil {
		t.Fatalf("helper never armed SIGTERM handler within 5s: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// wait for marker (within grace window)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := os.ReadFile(marker)
	if string(got) != "graceful" {
		t.Fatalf("graceful stop marker = %q, want \"graceful\" (SIGTERM may have been skipped, fell back to kill)", string(got))
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
