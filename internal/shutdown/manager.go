package shutdown

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type Kind uint8

const (
	Stream Kind = iota
	NonStream
)

type Snapshot struct {
	Streams    int
	NonStreams int
	Started    bool
}

type Manager struct {
	stateMu    sync.Mutex
	streams    sync.Map
	nonStreams sync.Map
	started    atomic.Bool
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Register(kind Kind, id string) bool {
	if m == nil || id == "" {
		return false
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.started.Load() {
		return false
	}
	if kind == Stream {
		m.streams.Store(id, struct{}{})
	} else {
		m.nonStreams.Store(id, struct{}{})
	}
	return true
}
func (m *Manager) Unregister(kind Kind, id string) {
	if m == nil {
		return
	}
	if kind == Stream {
		m.streams.Delete(id)
	} else {
		m.nonStreams.Delete(id)
	}
}
func (m *Manager) Snapshot() Snapshot {
	s, n := 0, 0
	if m != nil {
		m.streams.Range(func(_, _ any) bool { s++; return true })
		m.nonStreams.Range(func(_, _ any) bool { n++; return true })
	}
	return Snapshot{Streams: s, NonStreams: n, Started: m != nil && m.started.Load()}
}
func (m *Manager) Started() bool { return m != nil && m.started.Load() }

// Shutdown marks the manager closed to new requests, waits for non-stream
// requests first, then streams. A zero timeout skips that phase.
func (m *Manager) Shutdown(ctx context.Context, nonStreamTimeout, streamTimeout time.Duration) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.stateMu.Lock()
	m.started.Store(true)
	m.stateMu.Unlock()
	wait := func(kind Kind, timeout time.Duration) {
		if timeout <= 0 {
			return
		}
		deadline, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			s := m.Snapshot()
			count := s.Streams
			if kind == NonStream {
				count = s.NonStreams
			}
			if count == 0 {
				return
			}
			select {
			case <-deadline.Done():
				return
			case <-ticker.C:
			}
		}
	}
	wait(NonStream, nonStreamTimeout)
	wait(Stream, streamTimeout)
	return m.Snapshot()
}
