package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SR-W2 (doc 20 A-P2-6): serializedResponseWriter routes one connection's
// body writes through a shared SerializedStreamWriter. The pre-stream
// keepalive installs it first and the handler reassigns its writer to
// psk.Writer(), so keepalive comments and bridge frames can never interleave.

func TestSerializedResponseWriterDelegatesHeaderAndStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSerializedResponseWriter(rec)
	w.Header().Set("X-Test", "1")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("data: x\n\n"))
	require.NoError(t, err)
	w.Flush()

	assert.Equal(t, "1", rec.Header().Get("X-Test"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "data: x\n\n", rec.Body.String())
}

// syncRecorder is a mutex-guarded ResponseWriter for the concurrency tests:
// all writes still funnel through the SerializedStreamWriter, but the guard
// keeps any direct assertions race-free.
type syncRecorder struct {
	mu     sync.Mutex
	buf    strings.Builder
	header http.Header
}

func newSyncRecorder() *syncRecorder        { return &syncRecorder{header: make(http.Header)} }
func (s *syncRecorder) Header() http.Header { return s.header }
func (s *syncRecorder) WriteHeader(int)     {}
func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncRecorder) Flush()         {}
func (s *syncRecorder) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }

func TestSerializedResponseWriterConcurrentProducersNeverInterleave(t *testing.T) {
	f := newSyncRecorder()
	w := NewSerializedResponseWriter(f)
	frame := "event: content_block_delta\ndata: {\"delta\":{\"text\":\"hello world\"}}\n\n"
	comment := sseKeepaliveComment

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = w.Write([]byte(frame))
		}()
		go func() {
			defer wg.Done()
			_, _ = w.Write([]byte(comment))
		}()
	}
	wg.Wait()

	out := f.String()
	if n := strings.Count(out, frame); n != 32 {
		t.Fatalf("wire contains %d intact frames, want 32 — frames interleaved or lost:\n%q", n, out)
	}
	if n := strings.Count(out, comment); n != 32 {
		t.Fatalf("wire contains %d intact comments, want 32:\n%q", n, out)
	}
}

// TestPreStreamKeepaliveWriterSharesSerializedChannel verifies the handler
// wiring contract: psk.Writer() is the same serialized connection view the
// keepalive loop writes through, so a direct producer (bridge frame) keeps
// its bytes intact and ordered after the prewarm comment.
func TestPreStreamKeepaliveWriterSharesSerializedChannel(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour, "req-1")
	require.True(t, ok)
	w := psk.Writer()

	const chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	_, err := w.Write([]byte(chunk))
	require.NoError(t, err)
	psk.stop()

	got := rec.Body.String()
	idxComment := strings.Index(got, sseKeepaliveComment)
	idxChunk := strings.Index(got, chunk)
	require.GreaterOrEqual(t, idxComment, 0)
	require.GreaterOrEqual(t, idxChunk, 0)
	assert.Less(t, idxComment, idxChunk, "prewarm comment must precede bridge bytes")
	assert.True(t, strings.HasSuffix(got, chunk), "bridge frame must land intact on the connection, got %q", got)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
}

// TestPreStreamKeepaliveCommentsAndFramesNeverInterleave drives the real
// writeComment path against a direct frame producer on psk.Writer(): both
// funnel through the shared SerializedStreamWriter, so every frame arrives
// intact no matter how the two producers overlap.
func TestPreStreamKeepaliveCommentsAndFramesNeverInterleave(t *testing.T) {
	rec := httptest.NewRecorder()
	psk, ok := startPreStreamKeepalive(rec, time.Hour, "req-2")
	require.True(t, ok)
	w := psk.Writer()

	frame := "data: {\"choices\":[{\"delta\":{\"content\":\"hello world\"}}]}\n\n"
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			psk.writeComment(sseKeepaliveComment)
		}()
		go func() {
			defer wg.Done()
			_, _ = w.Write([]byte(frame))
		}()
	}
	wg.Wait()
	psk.stop()

	got := rec.Body.String()
	if n := strings.Count(got, frame); n != 32 {
		t.Fatalf("wire contains %d intact frames, want 32 — keepalive/frame interleave:\n%q", n, got)
	}
	if n := strings.Count(got, sseKeepaliveComment); n != 33 { // 32 + the prewarm comment
		t.Fatalf("wire contains %d intact comments, want 33:\n%q", n, got)
	}
}
