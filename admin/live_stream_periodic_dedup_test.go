package admin

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func periodicTestClient(h *LiveStreamSSEHub) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.mu.Lock()
	h.clients[&liveStreamClient{w: w, fl: w, isSuper: true}] = struct{}{}
	h.mu.Unlock()
	return w
}

func periodicFrames(w *httptest.ResponseRecorder) int {
	return strings.Count(w.Body.String(), "event: message\n")
}

func TestPeriodicStateOnlySuppressesUnchangedContent(t *testing.T) {
	for _, kind := range []string{"queue", "nodes"} {
		t.Run(kind, func(t *testing.T) {
			h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
			w := periodicTestClient(h)
			queue := &LiveQueueSnapshot{Enabled: true, Wired: true, Models: []LiveQueueLaneSnapshot{{Model: "test", Depth: 1}}}
			nodes := []LiveNodeStatus{{CredentialID: 1, InFlight: 1}}
			h.SetQueueSnapshotProvider(func() *LiveQueueSnapshot {
				queue.SourceVersion++ // production increments on every sample, not state changes
				return queue
			})
			h.SetNodeStatusProvider(func() []LiveNodeStatus { return nodes })
			emit := h.fanOutQueueSnapshot
			if kind == "nodes" {
				emit = h.fanOutNodeUpdate
			}
			emit()
			emit()
			if got := periodicFrames(w); got != 1 {
				t.Fatalf("unchanged %s sent %d frames, want 1", kind, got)
			}
			// In-place provider mutations must not alias the remembered state.
			queue.Models[0].Depth = 2
			nodes[0].InFlight = 2
			emit()
			queue.Models = nil
			nodes = []LiveNodeStatus{}
			emit()
			if got := periodicFrames(w); got != 3 {
				t.Fatalf("changed/empty %s sent %d frames, want 3", kind, got)
			}
			if kind == "queue" && !strings.Contains(w.Body.String(), `"sourceVersion":3`) {
				t.Fatal("dedup must preserve the real sample version on delivered changed state")
			}
			if !strings.Contains(w.Body.String(), `"`+map[string]string{"nodes": "nodes_present", "queue": "queue_present"}[kind]+`":true`) {
				t.Fatal("authoritative state presence was lost")
			}
		})
	}
}

func TestPeriodicStateNewClientStillGetsCurrentContent(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	h.SetNodeStatusProvider(func() []LiveNodeStatus { return []LiveNodeStatus{{CredentialID: 7}} })
	first := periodicTestClient(h)
	h.fanOutNodeUpdate()
	second := periodicTestClient(h)
	h.fanOutNodeUpdate()
	if periodicFrames(first) != 1 || periodicFrames(second) != 1 {
		t.Fatalf("frames old/new=%d/%d, want 1/1", periodicFrames(first), periodicFrames(second))
	}
}

func TestPeriodicStateReplayInvalidatesRememberedSnapshot(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	w := periodicTestClient(h)
	h.SetNodeStatusProvider(func() []LiveNodeStatus { return []LiveNodeStatus{{CredentialID: 7, InFlight: 2}} })
	h.fanOutNodeUpdate() // Q1 reaches a client already registered for initial replay.
	for client := range h.clients {
		// The handler can finish serializing its earlier Q0 after Q1 was sent.
		h.writeEvent(client, []byte(`{"type":"initial_data","nodes_present":true,"nodes":[{"credential_id":7,"in_flight":1}]}`))
	}
	h.fanOutNodeUpdate()
	if got := periodicFrames(w); got != 3 {
		t.Fatalf("late initial replay left client stale: frames=%d, want Q1,Q0,Q1", got)
	}
}

func TestPeriodicStateDoesNotSuppressRequestEvents(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	w := periodicTestClient(h)
	env := LiveStreamEnvelope{Type: "request", Request: &LiveRequest{RequestID: "same-request"}}
	h.fanOut(env)
	h.fanOut(env)
	if got := periodicFrames(w); got != 2 {
		t.Fatalf("request frames=%d, want both lifecycle events", got)
	}
	h.fanOutKeepalive()
	if !strings.Contains(w.Body.String(), ":keepalive\n\n") {
		t.Fatal("keepalive comment was suppressed")
	}
}

func TestPeriodicStateConcurrentFanout(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	w := periodicTestClient(h)
	h.SetNodeStatusProvider(func() []LiveNodeStatus { return []LiveNodeStatus{{CredentialID: 7}} })
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(h.fanOutNodeUpdate)
	}
	wg.Wait()
	if got := periodicFrames(w); got != 1 {
		t.Fatalf("concurrent identical frames=%d, want 1", got)
	}
}
