package streaming

import (
	"errors"
	"net/http"
	"sync"
	"testing"
)

// recordingWriter captures everything written through the serialized writer
// and can simulate a dead connection.
type recordingWriter struct {
	mu       sync.Mutex
	body     []byte
	failNext bool
	flushes  int
	header   http.Header
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{header: http.Header{}}
}

func (r *recordingWriter) Header() http.Header { return r.header }

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		return 0, errors.New("write: broken pipe")
	}
	r.body = append(r.body, p...)
	return len(p), nil
}

func (r *recordingWriter) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

func (r *recordingWriter) WriteHeader(int) {}

func (r *recordingWriter) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.body)
}

// SR-W1 (doc 18 §9.3): once an attempt is committed, keepalive, status frames
// and the protocol bridge share one serialized write channel.
func TestSerializedStreamWriterPassesBytesThroughInOrder(t *testing.T) {
	rec := newRecordingWriter()
	sw := NewSerializedStreamWriter(rec, rec)

	if _, err := sw.Write([]byte("a")); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if _, err := sw.Write([]byte("b")); err != nil {
		t.Fatalf("write b: %v", err)
	}
	sw.Flush()

	if got := rec.snapshot(); got != "ab" {
		t.Fatalf("body = %q, want %q", got, "ab")
	}
	if rec.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", rec.flushes)
	}
}

// doc 18 §9.3: a failed write marks the connection detached; the writer never
// touches the ResponseWriter again.
func TestSerializedStreamWriterDetachesOnWriteFailure(t *testing.T) {
	rec := newRecordingWriter()
	rec.failNext = true
	sw := NewSerializedStreamWriter(rec, rec)

	if _, err := sw.Write([]byte("boom")); err == nil {
		t.Fatal("expected error from failing write")
	}
	if !sw.Detached() {
		t.Fatal("writer should be detached after failed write")
	}

	// Recover the underlying writer: subsequent writes must still be
	// refused (latch is sticky).
	rec.failNext = false
	if _, err := sw.Write([]byte("late")); err == nil {
		t.Fatal("write after detach must fail")
	}
	if got := rec.snapshot(); got != "" {
		t.Fatalf("detached writer must not write, got %q", got)
	}

	// Flush after detach must not reach the ResponseWriter either.
	before := rec.flushes
	sw.Flush()
	if rec.flushes != before {
		t.Fatal("flush after detach must not touch the ResponseWriter")
	}
}

// Explicit Detach (client disconnect observed elsewhere) has the same latch
// semantics as a failed write.
func TestSerializedStreamWriterExplicitDetach(t *testing.T) {
	rec := newRecordingWriter()
	sw := NewSerializedStreamWriter(rec, rec)

	sw.Detach()
	if _, err := sw.Write([]byte("x")); err == nil {
		t.Fatal("write after explicit detach must fail")
	}
	if got := rec.snapshot(); got != "" {
		t.Fatalf("detached writer must not write, got %q", got)
	}
}

// Concurrent writers must be serialized: bytes from different goroutines may
// interleave, but no individual Write may be torn.
func TestSerializedStreamWriterSerializesConcurrentWrites(t *testing.T) {
	rec := newRecordingWriter()
	sw := NewSerializedStreamWriter(rec, rec)

	const writers = 8
	const chunks = 50
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < chunks; j++ {
				if _, err := sw.Write([]byte("frame\n")); err != nil {
					t.Errorf("write failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := len(rec.snapshot()); got != writers*chunks*len("frame\n") {
		t.Fatalf("body length = %d, want %d (torn write detected)", got, writers*chunks*len("frame\n"))
	}
}
