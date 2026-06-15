package memora

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sink is a bounded, async, fire-and-forget writer that funnels request
// and response messages to Memora without blocking the main request path.
//
// The sink is intentionally simple: a single buffered channel, N workers,
// and a context-derived graceful shutdown. A full queue means "we drop
// the write" — it MUST NOT block or panic the caller.
//
// When the sink detects sustained errors (>= 10 consecutive), it enters
// a backpressure mode: workers Ping the service before attempting
// AddMessage, and sleep for a cooldown period on failure. This prevents
// a dead Memora from tying up all workers on 5-second timeouts.
//
// Counters (Enqueued/Dropped/Errored) are exposed via Stats() for the
// gateway's /healthz, /api/system/memora-status, or metrics endpoint.
type Sink struct {
	client   *Client
	queue    chan WriteOp
	workers  int
	wg       sync.WaitGroup

	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	processed atomic.Uint64
	errored   atomic.Uint64

	consecutiveErrors atomic.Int64
	lastError         atomic.Value // string
	lastErrorAt       atomic.Value // time.Time
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
		slog.Debug("memora.sink dropped op (queue full)", "dropped_total", s.dropped.Load())
	}
}

// Stats returns counters for /healthz / observability.
type Stats struct {
	Enqueued         uint64 `json:"enqueued"`
	Dropped          uint64 `json:"dropped"`
	Processed        uint64 `json:"processed"`
	Errored          uint64 `json:"errored"`
	QueueLen         int    `json:"queue_len"`
	QueueCap         int    `json:"queue_cap"`
	ConsecutiveErrors int64 `json:"consecutive_errors"`
	LastError        string `json:"last_error"`
	LastErrorAt      string `json:"last_error_at"`
}

func (s *Sink) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	st := Stats{
		Enqueued:          s.enqueued.Load(),
		Dropped:           s.dropped.Load(),
		Processed:         s.processed.Load(),
		Errored:           s.errored.Load(),
		QueueLen:          len(s.queue),
		QueueCap:          cap(s.queue),
		ConsecutiveErrors: s.consecutiveErrors.Load(),
	}
	if v, ok := s.lastError.Load().(string); ok {
		st.LastError = v
	}
	if v, ok := s.lastErrorAt.Load().(time.Time); ok && !v.IsZero() {
		st.LastErrorAt = v.Format(time.RFC3339)
	}
	return st
}

const (
	// backpressureThreshold is the number of consecutive errors before
	// workers enter backpressure mode (Ping before write, sleep on fail).
	backpressureThreshold = 10
	// backpressureCooldown is how long a worker sleeps when in
	// backpressure mode and the service is still unreachable.
	backpressureCooldown = 30 * time.Second
)

func (s *Sink) worker() {
	defer s.wg.Done()
	for op := range s.queue {
		// Backpressure: if we've seen many consecutive failures, check
		// connectivity first with a cheap Ping. If the service is still
		// down, sleep for a cooldown to avoid burning all workers on
		// 5-second AddMessage timeouts.
		if s.consecutiveErrors.Load() >= backpressureThreshold {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := s.client.Ping(ctx); err != nil {
				cancel()
				s.recordError(err)
				time.Sleep(backpressureCooldown)
				continue // retry this op on next iteration
			}
			cancel()
			// Ping succeeded — service is back. Reset consecutive
			// errors so the worker can proceed normally.
			s.consecutiveErrors.Store(0)
		}

		ctx, cancel := context.WithTimeout(context.Background(), s.client.addTimeout)
		err := s.client.AddMessage(ctx, op.UserID, op.Messages, op.Info)
		cancel()
		s.processed.Add(1)
		if err != nil {
			s.recordError(err)
		} else {
			s.consecutiveErrors.Store(0)
		}
	}
}

// recordError updates error counters and logs with rate-limited verbosity.
func (s *Sink) recordError(err error) {
	s.errored.Add(1)
	consec := s.consecutiveErrors.Add(1)
	s.lastError.Store(err.Error())
	s.lastErrorAt.Store(time.Now())

	// Log: first 5 errors at Warn, then every 100th, to avoid log spam
	// during prolonged outages while still giving visibility.
	total := s.errored.Load()
	if total <= 5 || total%100 == 0 {
		slog.Warn("memora.sink write failed",
			"error", err,
			"errored_total", total,
			"consecutive", consec,
		)
	}
}
