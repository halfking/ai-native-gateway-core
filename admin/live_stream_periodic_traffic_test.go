package admin

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type periodicFailWriter struct {
	*httptest.ResponseRecorder
	fail bool
}

func (w *periodicFailWriter) Write(p []byte) (int, error) {
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestPeriodicStateFailedWriteDoesNotRemember(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	w := &periodicFailWriter{ResponseRecorder: httptest.NewRecorder(), fail: true}
	c := &liveStreamClient{w: w}
	digest := [32]byte{1}
	if h.writeEventState(c, []byte(`{"state":1}`), "queue_snapshot", digest) || len(c.periodicState) != 0 {
		t.Fatal("failed write must not advance the state cache")
	}
	w.fail = false
	if !h.writeEventState(c, []byte(`{"state":1}`), "queue_snapshot", digest) || w.Body.Len() == 0 {
		t.Fatal("state must still be delivered after the failed write")
	}
	before := w.Body.Len()
	if !h.writeEventState(c, []byte(`{"state":1}`), "queue_snapshot", digest) || w.Body.Len() != before {
		t.Fatal("successfully delivered retry must subsequently deduplicate")
	}
}

func TestPeriodicStateWireBytes(t *testing.T) {
	h := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	w := periodicTestClient(h)
	nodes := make([]LiveNodeStatus, 500)
	for i := range nodes {
		nodes[i] = LiveNodeStatus{CredentialID: i + 1, CredentialLabel: fmt.Sprintf("fixture-%d", i), LastError: strings.Repeat("fixture ", 16)}
	}
	h.SetNodeStatusProvider(func() []LiveNodeStatus { return nodes })
	h.fanOutNodeUpdate()
	oneFrame := w.Body.Len()
	if oneFrame == 0 || periodicFrames(w) != 1 {
		t.Fatal("first stable state must deliver exactly one nonempty frame")
	}
	for range 99 {
		h.fanOutNodeUpdate()
	}
	if got := w.Body.Len(); got != oneFrame || periodicFrames(w) != 1 {
		t.Fatalf("100 stable ticks wrote %dB (first=%dB), unchanged snapshots are retransmitted", got, oneFrame)
	}
	t.Logf("fixture500 nodes/100 stable ticks: old approximately %dB, actual %dB; not measured production billing", oneFrame*100, w.Body.Len())
}
