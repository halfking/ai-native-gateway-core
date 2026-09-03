package shutdown

import (
	"context"
	"testing"
	"time"
)

func TestManagerShutdownWaitsInPhases(t *testing.T) {
	m := NewManager()
	if !m.Register(NonStream, "n") || !m.Register(Stream, "s") {
		t.Fatal("register failed")
	}
	done := make(chan Snapshot, 1)
	go func() { done <- m.Shutdown(context.Background(), time.Second, time.Second) }()
	deadline := time.Now().Add(time.Second)
	for !m.Started() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !m.Started() {
		t.Fatal("shutdown did not start")
	}
	if m.Register(Stream, "late") {
		t.Fatal("late registration accepted")
	}
	m.Unregister(NonStream, "n")
	m.Unregister(Stream, "s")
	select {
	case snap := <-done:
		if snap.Streams != 0 || snap.NonStreams != 0 || !snap.Started {
			t.Fatalf("snapshot=%+v", snap)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}

func TestManagerShutdownTimeout(t *testing.T) {
	m := NewManager()
	m.Register(Stream, "s")
	start := time.Now()
	snap := m.Shutdown(context.Background(), 0, 20*time.Millisecond)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("shutdown exceeded timeout")
	}
	if snap.Streams != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
}
