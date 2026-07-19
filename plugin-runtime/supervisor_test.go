package pluginruntime

import (
	"context"
	"testing"
)

func TestSupervisor_StartStop(t *testing.T) {
	fake := &fakeProc{}
	s := NewSupervisor(SupervisorConfig{
		SocketDir:     t.TempDir(),
		ContextSecret: []byte("s"),
	})
	s.commandFactory = func(socketPath string) command {
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
