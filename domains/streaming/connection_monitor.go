package streaming

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ConnectionMonitor watches a streaming client's request context and optional
// CloseNotify signal. It never writes probe bytes to the response; transport
// errors are discovered by the normal Write/Flush path.
type ConnectionMonitor struct {
	ctx          context.Context
	cancel       context.CancelFunc
	interval     time.Duration
	idleTimeout  time.Duration
	now          func() time.Time
	probe        func() bool
	ticker       *time.Ticker
	lastActiveNS atomic.Int64
	stopOnce     sync.Once
	done         chan struct{}
}

type ConnectionMonitorOption func(*ConnectionMonitor)

func WithConnectionMonitorInterval(d time.Duration) ConnectionMonitorOption {
	return func(m *ConnectionMonitor) {
		if d > 0 {
			m.interval = d
		}
	}
}
func WithConnectionMonitorIdleTimeout(d time.Duration) ConnectionMonitorOption {
	return func(m *ConnectionMonitor) {
		if d > 0 {
			m.idleTimeout = d
		}
	}
}
func WithConnectionMonitorClock(now func() time.Time) ConnectionMonitorOption {
	return func(m *ConnectionMonitor) {
		if now != nil {
			m.now = now
		}
	}
}
func WithConnectionMonitorProbe(probe func() bool) ConnectionMonitorOption {
	return func(m *ConnectionMonitor) {
		if probe != nil {
			m.probe = probe
		}
	}
}

// NewConnectionMonitor derives a cancellable context for a stream. The
// returned context is canceled when the client context closes, CloseNotify
// fires, or a safe connection probe reports failure after the idle threshold.
func NewConnectionMonitor(parent context.Context, w http.ResponseWriter, opts ...ConnectionMonitorOption) (context.Context, *ConnectionMonitor) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m := &ConnectionMonitor{
		ctx: ctx, cancel: cancel, interval: time.Second, idleTimeout: 10 * time.Second,
		now: time.Now, probe: func() bool { return true }, done: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	m.lastActiveNS.Store(m.now().UnixNano())
	if n, ok := w.(http.CloseNotifier); ok {
		go func() {
			select {
			case <-n.CloseNotify():
				m.cancel()
			case <-m.done:
			case <-ctx.Done():
			}
		}()
	}
	go m.run()
	return ctx, m
}

func (m *ConnectionMonitor) run() {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			last := time.Unix(0, m.lastActiveNS.Load())
			if now.Sub(last) < m.idleTimeout {
				continue
			}
			if m.probe != nil && !m.probe() {
				m.cancel()
				return
			}
		}
	}
}

func (m *ConnectionMonitor) Touch() {
	if m != nil {
		m.lastActiveNS.Store(m.now().UnixNano())
	}
}

func (m *ConnectionMonitor) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { m.cancel(); <-m.done })
}

// monitoredResponseWriter updates the monitor only after a successful write or
// flush, keeping the monitor independent from protocol framing.
type monitoredResponseWriter struct {
	delegate http.ResponseWriter
	monitor  *ConnectionMonitor
}

func (w *monitoredResponseWriter) Header() http.Header    { return w.delegate.Header() }
func (w *monitoredResponseWriter) WriteHeader(status int) { w.delegate.WriteHeader(status) }
func (w *monitoredResponseWriter) Write(p []byte) (int, error) {
	n, err := w.delegate.Write(p)
	if err == nil && n == len(p) {
		w.monitor.Touch()
	}
	return n, err
}
func (w *monitoredResponseWriter) Flush() {
	if f, ok := w.delegate.(http.Flusher); ok {
		f.Flush()
		w.monitor.Touch()
	}
}
func (w *monitoredResponseWriter) FlushError() error {
	if f, ok := w.delegate.(interface{ FlushError() error }); ok {
		err := f.FlushError()
		if err == nil {
			w.monitor.Touch()
		}
		return err
	}
	w.Flush()
	return nil
}
