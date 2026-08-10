package admin

import (
	"sync"
	"testing"
	"time"
)

// recordingSink captures published tasks for assertions. Implements the
// local fan-out path (no Redis) by calling Publish directly.
type recordingSink struct {
	mu    sync.Mutex
	tasks []ProbeStreamTask
}

func (r *recordingSink) record(t ProbeStreamTask) {
	r.mu.Lock()
	r.tasks = append(r.tasks, t)
	r.mu.Unlock()
}

// TestProbeSSEHub_InMemoryFanOut verifies the hub delivers published tasks to
// a connected client channel even when Redis is not wired (single-instance /
// dev mode). This pins the "Redis-optional" contract that the gateway relies
// on when fpSlotRedis is nil.
func TestProbeSSEHub_InMemoryFanOut(t *testing.T) {
	hub := NewProbeSSEHub(nil) // no Redis
	if hub.Enabled() {
		t.Fatalf("hub must be disabled without Redis, got Enabled=true")
	}

	// Register a fake client by adding it to the clients map directly (mirrors
	// what HandleStream does).
	clientCh := make(chan ProbeStreamEnvelope, 8)
	hub.mu.Lock()
	hub.clients[clientCh] = struct{}{}
	hub.mu.Unlock()
	defer hub.removeClient(clientCh)

	task := ProbeStreamTask{
		ID:           "run-1",
		Source:       "node_probe",
		Status:       "ok",
		CredentialID: 42,
		RawModel:     "gpt-5.6",
		Attempt:      1,
		Timestamp:    time.Now().UnixMilli(),
	}
	hub.Publish(task)

	select {
	case env := <-clientCh:
		if env.Type != "completed" {
			t.Fatalf("event type = %q, want completed", env.Type)
		}
		if env.Task == nil || env.Task.ID != "run-1" {
			t.Fatalf("task not delivered: %+v", env.Task)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for fan-out")
	}

	// A failed task maps to the "failed" event type.
	hub.Publish(ProbeStreamTask{ID: "run-2", Source: "integrity", Status: "fail", Timestamp: time.Now().UnixMilli()})
	select {
	case env := <-clientCh:
		if env.Type != "failed" {
			t.Fatalf("event type = %q, want failed", env.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for failed event")
	}

	hub.Stop()
}

// TestProbeSSEHub_PublishNoopOnEmptyID guards against the hub emitting empty
// events when a producer forgets to set the task ID.
func TestProbeSSEHub_PublishNoopOnEmptyID(t *testing.T) {
	hub := NewProbeSSEHub(nil)
	clientCh := make(chan ProbeStreamEnvelope, 1)
	hub.mu.Lock()
	hub.clients[clientCh] = struct{}{}
	hub.mu.Unlock()

	hub.Publish(ProbeStreamTask{ID: "", Status: "ok"})

	select {
	case env := <-clientCh:
		t.Fatalf("empty-ID publish must be a no-op, got %+v", env)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing delivered
	}
	hub.Stop()
}

// TestEventTypeForStatus pins the status→event-type mapping the frontend
// switches on.
func TestEventTypeForStatus(t *testing.T) {
	cases := map[string]string{
		"pending":   "submitted",
		"in-flight": "started",
		"ok":        "completed",
		"fail":      "failed",
		"":          "",
		"weird":     "weird", // passthrough for unknown
	}
	for status, want := range cases {
		if got := eventTypeForStatus(status); got != want {
			t.Errorf("eventTypeForStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestProbeStreamTask_TsUnixMilli pins the default-to-now behaviour.
func TestProbeStreamTask_TsUnixMilli(t *testing.T) {
	if got := (ProbeStreamTask{Timestamp: 0}).TsUnixMilli(); got == 0 {
		t.Fatalf("TsUnixMilli must default to now (>0), got %d", got)
	}
	if got := (ProbeStreamTask{Timestamp: 12345}).TsUnixMilli(); got != 12345 {
		t.Fatalf("TsUnixMilli must return explicit timestamp, got %d", got)
	}
}
