package streaming

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// StreamSession owns the client connection's serialized writer and heartbeat
// lifecycle. Protocol bridges write through Writer while heartbeat comments
// use the same lock as transport-only frames.
type StreamSession struct {
	writer    *serializedResponseWriter
	interval  time.Duration
	heartbeat []byte
	stopCh    chan struct{}
	doneCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	lifecycle sync.Mutex
	started   bool
	paused    atomic.Bool
}

// NewStreamSession creates a heartbeat owner without committing HTTP headers.
// Callers must finish authentication and request validation before starting it.
func NewStreamSession(w http.ResponseWriter, interval time.Duration, heartbeat string) *StreamSession {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if heartbeat == "" {
		heartbeat = sseKeepaliveComment
	}
	serialized, ok := w.(*serializedResponseWriter)
	if !ok {
		serialized = NewSerializedResponseWriter(w)
	}
	return &StreamSession{
		writer:    serialized,
		interval:  interval,
		heartbeat: []byte(heartbeat),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Writer is the only ResponseWriter protocol bridges should use for this
// session after it has been created.
func (s *StreamSession) Writer() http.ResponseWriter { return s.writer }

// Start launches the independent heartbeat ticker. Repeated calls are no-ops.
func (s *StreamSession) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		s.lifecycle.Lock()
		s.started = true
		go s.loop(ctx)
		s.lifecycle.Unlock()
	})
}

func (s *StreamSession) loop(ctx context.Context) {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if !s.paused.Load() {
				_ = s.Heartbeat()
			}
		}
	}
}

// Heartbeat writes and flushes one transport comment without affecting
// semantic capture or commit counters.
func (s *StreamSession) Heartbeat() error {
	if s == nil {
		return nil
	}
	if _, err := s.writer.SerializedWriter().WriteTransportFrame(s.heartbeat); err != nil {
		return err
	}
	return s.writer.SerializedWriter().FlushError()
}

// WriteTransportFrame sends another protocol-safe transport frame through the
// heartbeat channel. It is intended for SSE comments, not semantic data.
func (s *StreamSession) WriteTransportFrame(frame string) error {
	if s == nil || frame == "" {
		return nil
	}
	if _, err := s.writer.SerializedWriter().WriteTransportFrame([]byte(frame)); err != nil {
		return err
	}
	return s.writer.SerializedWriter().FlushError()
}

func (s *StreamSession) Pause() {
	if s != nil {
		s.paused.Store(true)
	}
}

func (s *StreamSession) Resume() {
	if s != nil {
		s.paused.Store(false)
	}
}

// Stop terminates the ticker and waits for it to exit. It is idempotent.
func (s *StreamSession) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.lifecycle.Lock()
	started := s.started
	s.lifecycle.Unlock()
	if started {
		<-s.doneCh
	}
}
