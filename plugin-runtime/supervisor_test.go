package pluginruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

	c := newExecCommand("", helperBin, []string{"PID_FILE=" + pidFile})
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
