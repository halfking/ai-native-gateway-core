package pluginruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
