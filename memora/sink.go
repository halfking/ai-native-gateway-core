package memora

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Sink is a bounded, async, fire-and-forget writer that funnels request
// and response messages to Memora without blocking the main request path.
//
// The sink is intentionally simple: a single buffered channel, N workers,
// and a context-derived graceful shutdown. A full queue means "we drop
// the write" — it MUST NOT block or panic the caller.
//
// Counters (Enqueued/Dropped/Errored) are exposed via Stats() for the
// gateway's /healthz or metrics endpoint.
type Sink struct {
	client   *Client
	queue    chan WriteOp
	workers  int
	wg       sync.WaitGroup

	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	processed atomic.Uint64
	errored   atomic.Uint64
}

// WriteOp is one item of work in the sink.
type WriteOp struct {
	UserID   string
	Messages []Message
	Info     map[string]any
}

// NewSink builds a sink. workers and queueSize are clamped to sane
// defaults if non-positive.
func NewSink(client *Client, workers, queueSize int) *Sink {
	if workers <= 0 {
		workers = 4
	}
	if queueSize <= 0 {
		queueSize = 2048
	}
	return &Sink{
		client:  client,
		queue:   make(chan WriteOp, queueSize),
		workers: workers,
	}
}

// Start launches worker goroutines. Idempotent.
func (s *Sink) Start() {
	if s == nil || s.client == nil || s.client.Disabled() {
		return
	}
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	slog.Info("memora.sink started", "workers", s.workers, "queue_cap", cap(s.queue))
}

// Stop waits for queued items to drain. Pass a context to bound the wait.
// A nil context blocks until all queued items are processed.
func (s *Sink) Stop(ctx context.Context) {
	if s == nil || s.client == nil || s.client.Disabled() {
		return
	}
	close(s.queue)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	if ctx == nil {
		<-done
		slog.Info("memora.sink stopped cleanly")
		return
	}
	select {
	case <-done:
		slog.Info("memora.sink stopped cleanly")
	case <-ctx.Done():
		slog.Warn("memora.sink stop timed out", "dropped", s.dropped.Load())
	}
}

// Enqueue is the ONLY way callers feed the sink. It is O(1) and never
// blocks: a full queue causes the op to be dropped and counted.
func (s *Sink) Enqueue(op WriteOp) {
	if s == nil || s.client == nil || s.client.Disabled() || op.UserID == "" {
		return
	}
	select {
	case s.queue <- op:
		s.enqueued.Add(1)
	default:
		s.dropped.Add(1)
		// Log at debug to avoid log spam.
		slog.Debug("memora.sink dropped op (queue full)", "dropped_total", s.dropped.Load())
	}
}

// Stats returns counters for /healthz / observability.
type Stats struct {
	Enqueued  uint64
	Dropped   uint64
	Processed uint64
	Errored   uint64
	QueueLen  int
	QueueCap  int
}

func (s *Sink) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:  s.enqueued.Load(),
		Dropped:   s.dropped.Load(),
		Processed: s.processed.Load(),
		Errored:   s.errored.Load(),
		QueueLen:  len(s.queue),
		QueueCap:  cap(s.queue),
	}
}

func (s *Sink) worker() {
	defer s.wg.Done()
	for op := range s.queue {
		ctx, cancel := context.WithTimeout(context.Background(), s.client.addTimeout)
		err := s.client.AddMessage(ctx, op.UserID, op.Messages, op.Info)
		cancel()
		s.processed.Add(1)
		if err != nil {
			s.errored.Add(1)
			slog.Debug("memora.sink write failed",
				"user_id", op.UserID,
				"error", err,
				"errored_total", s.errored.Load(),
			)
		}
	}
}
