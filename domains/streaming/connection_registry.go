package streaming

import (
	"errors"
	"io"
	"sync"
	"time"
)

// connection_registry.go — 会话优化 v4 R1.6（FR-1 连接注册表，T4）
//
// ConnectionRegistry maps request_id → the client connection's serialized
// frame writer plus registration metadata, so executors, the scheduler and
// background tasks can write data frames or keepalive/thinking frames
// (FR-3) to the client by request id while a stream is alive.
//
// Hard rules (R1.6 / §14 G7):
//   - 并发安全：registry-level RWMutex + per-entry write mutex；
//   - 容量有界：默认 4096 条，满时 Register 返回 false，绝不阻塞；
//   - 连接关闭即注销：Unregister + optional onClose(reason) callback
//     (fired exactly once per registration, including write-deadline
//     disconnects and replacement by a duplicate request_id);
//   - 串行化：每条目单一写锁保证心跳与数据帧不交错（UT-SK-05）；
//   - 每次写 deadline：默认 30s，可配；超时视为客户端断开（N2/G7），
//     触发 onClose 回调并注销（UT-SK-09）。
//
// The registry is a process-local side channel only: the main data path
// keeps owning its SerializedStreamWriter; the registry wraps the SAME
// writer so every side-channel frame is serialized with in-flight data
// frames by the underlying writer (and additionally by the per-entry
// mutex for multi-producer side channels).

var (
	// ErrConnectionRegistryFull is returned by Register when the registry is
	// at capacity. The caller must NOT block; the request proceeds without a
	// registry entry (no side-channel writes, normal streaming unaffected).
	ErrConnectionRegistryFull = errors.New("connection registry full")
	// ErrConnectionNotRegistered is returned by write/lookup helpers when the
	// request id is unknown (never registered, already unregistered, or the
	// write deadline previously detached it).
	ErrConnectionNotRegistered = errors.New("connection not registered")
	// ErrConnectionClosed is returned when the entry exists but was already
	// closed (duplicate close path).
	ErrConnectionClosed = errors.New("connection closed")
	// ErrClientWriteDeadline reports a client write that exceeded the
	// per-write deadline: the client is considered disconnected (N2/G7) and
	// the entry is unregistered with reason write_deadline.
	ErrClientWriteDeadline = errors.New("client write deadline exceeded")
)

const (
	// DefaultConnectionRegistryCapacity bounds the live-entry map (R1.6).
	DefaultConnectionRegistryCapacity = 4096
	// DefaultClientWriteTimeout bounds one side-channel frame write (G7).
	DefaultClientWriteTimeout = 30 * time.Second
	// DefaultClosedHistoryDepth bounds the closed-connection audit ring
	// surfaced through the admin connection-registry projection.
	DefaultClosedHistoryDepth = 256
)

// FrameWriter is the unified client-frame write entry for the registry.
// Implementations must be safe for concurrent use; the registry serializes
// calls per entry anyway (single mutex per entry), so the simplest correct
// implementation just forwards to a SerializedStreamWriter.
type FrameWriter interface {
	// WriteFrame writes one complete SSE frame (comment, event or data
	// frame, including its trailing blank line) to the client connection.
	WriteFrame(frame string) error
}

// SerializedFrameWriter adapts the connection's *SerializedStreamWriter to
// FrameWriter. It routes through WriteTransportFrame so comment/thinking
// frames never enter the semantic wire capture (durable result bytes and
// chunk accounting stay untouched, UT-SK-08).
type SerializedFrameWriter struct {
	Writer *SerializedStreamWriter
}

// NewSerializedFrameWriter wraps sw; sw must be the single serialized writer
// of the client connection.
func NewSerializedFrameWriter(sw *SerializedStreamWriter) *SerializedFrameWriter {
	return &SerializedFrameWriter{Writer: sw}
}

// WriteFrame implements FrameWriter.
func (w *SerializedFrameWriter) WriteFrame(frame string) error {
	if w == nil || w.Writer == nil {
		return ErrConnectionClosed
	}
	if _, err := w.Writer.WriteTransportFrame([]byte(frame)); err != nil {
		return err
	}
	return w.Writer.FlushError()
}

// FlushFrameWriter adapts a plain io.Writer (e.g. httptest recorders or
// custom sinks) to FrameWriter, flushing when the writer supports it.
type FlushFrameWriter struct {
	W io.Writer
}

// WriteFrame implements FrameWriter.
func (w *FlushFrameWriter) WriteFrame(frame string) error {
	if w == nil || w.W == nil {
		return ErrConnectionClosed
	}
	if _, err := io.WriteString(w.W, frame); err != nil {
		return err
	}
	if f, ok := w.W.(flusher); ok {
		f.Flush()
	}
	return nil
}

// RegistrationMetadata carries the read-only connection facts registered
// alongside the writer. Only ids/labels — never body content or secrets.
type RegistrationMetadata struct {
	// Protocol is the client wire protocol label (protocolMetricLabel
	// vocabulary: openai_chat / openai_responses / anthropic / unknown).
	Protocol string
	// ClientType is the internal/clienttype normalized client kind
	// (cursor / claude-code / ... / unknown).
	ClientType string
	// TenantID is optional; used only for admin projection filtering.
	TenantID string
}

// ConnectionSnapshot is the read-only projection of one registry entry
// (admin connection-registry view + tests). Body content is never included.
type ConnectionSnapshot struct {
	RequestID     string    `json:"request_id"`
	Protocol      string    `json:"protocol,omitempty"`
	ClientType    string    `json:"client_type,omitempty"`
	TenantID      string    `json:"tenant_id,omitempty"`
	RegisteredAt  time.Time `json:"registered_at"`
	LastFrameAt   time.Time `json:"last_frame_at"`
	FramesWritten uint64    `json:"frames_written"`
	BytesWritten  uint64    `json:"bytes_written"`
	// Closed entries only: why the connection left the registry.
	CloseReason string `json:"close_reason,omitempty"`
	Closed      bool   `json:"closed"`
}

type registryEntry struct {
	id           string
	writer       FrameWriter
	meta         RegistrationMetadata
	onClose      func(reason string)
	registeredAt time.Time

	// mu serializes frame writes for this entry (UT-SK-05): heartbeats and
	// data frames from any producer can never interleave. Lock ordering:
	// never hold mu while taking ConnectionRegistry.mu.
	mu            sync.Mutex
	lastFrameAt   time.Time
	framesWritten uint64
	bytesWritten  uint64
	closed        bool
	closeReason   string
}

func (e *registryEntry) snapshot(closed bool, reason string) ConnectionSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := ConnectionSnapshot{
		RequestID:     e.id,
		Protocol:      e.meta.Protocol,
		ClientType:    e.meta.ClientType,
		TenantID:      e.meta.TenantID,
		RegisteredAt:  e.registeredAt,
		LastFrameAt:   e.lastFrameAt,
		FramesWritten: e.framesWritten,
		BytesWritten:  e.bytesWritten,
		Closed:        closed,
		CloseReason:   reason,
	}
	if e.closed {
		s.Closed = true
		s.CloseReason = e.closeReason
	}
	return s
}

// ConnectionRegistry is the process-local request_id → client writer map
// (R1.6). Construct one per gateway process; inject it into the streaming
// ingress (register/unregister) and the FR-3 thinking-frame bridge.
type ConnectionRegistry struct {
	mu      sync.RWMutex
	entries map[string]*registryEntry
	closed  []ConnectionSnapshot // bounded ring, oldest first evicted

	capacity      int
	writeTimeout  time.Duration
	closedHistory int

	// now is the clock seam (tests inject a fake clock).
	now func() time.Time
}

// NewConnectionRegistry builds a registry. capacity <= 0 →
// DefaultConnectionRegistryCapacity; writeTimeout <= 0 →
// DefaultClientWriteTimeout.
func NewConnectionRegistry(capacity int, writeTimeout time.Duration) *ConnectionRegistry {
	if capacity <= 0 {
		capacity = DefaultConnectionRegistryCapacity
	}
	if writeTimeout <= 0 {
		writeTimeout = DefaultClientWriteTimeout
	}
	return &ConnectionRegistry{
		entries:       make(map[string]*registryEntry),
		capacity:      capacity,
		writeTimeout:  writeTimeout,
		closedHistory: DefaultClosedHistoryDepth,
		now:           time.Now,
	}
}

// SetClock injects the clock used for registration/write timestamps. Tests
// only; must be called before any registration.
func (r *ConnectionRegistry) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// Register associates requestID with the connection's frame writer. It
// returns ErrConnectionRegistryFull (registration refused, caller does NOT
// block) when at capacity. A duplicate request_id replaces the previous
// entry, firing its onClose with reason "replaced". onClose may be nil; it
// is invoked at most once per successful registration.
func (r *ConnectionRegistry) Register(requestID string, w FrameWriter, meta RegistrationMetadata, onClose func(reason string)) error {
	if requestID == "" || w == nil {
		return ErrConnectionNotRegistered
	}
	r.mu.Lock()
	now := r.now()
	old, hadOld := r.entries[requestID]
	if !hadOld && len(r.entries) >= r.capacity {
		r.mu.Unlock()
		return ErrConnectionRegistryFull
	}
	entry := &registryEntry{
		id:           requestID,
		writer:       w,
		meta:         meta,
		onClose:      onClose,
		registeredAt: now,
		lastFrameAt:  now,
	}
	r.entries[requestID] = entry
	r.mu.Unlock()

	if hadOld {
		r.detach(old, "replaced")
	}
	return nil
}

// Unregister removes the entry and fires onClose(reason). Idempotent:
// a second call returns ErrConnectionNotRegistered.
func (r *ConnectionRegistry) Unregister(requestID string, reason string) error {
	r.mu.Lock()
	entry, ok := r.entries[requestID]
	if ok {
		delete(r.entries, requestID)
	}
	r.mu.Unlock()
	if !ok {
		return ErrConnectionNotRegistered
	}
	r.detach(entry, reason)
	return nil
}

// detach closes an already-removed entry: marks it closed, snapshots it into
// the bounded audit ring and fires the onClose callback.
func (r *ConnectionRegistry) detach(entry *registryEntry, reason string) {
	if entry == nil {
		return
	}
	entry.mu.Lock()
	if entry.closed {
		entry.mu.Unlock()
		return
	}
	entry.closed = true
	entry.closeReason = reason
	snap := ConnectionSnapshot{
		RequestID:     entry.id,
		Protocol:      entry.meta.Protocol,
		ClientType:    entry.meta.ClientType,
		TenantID:      entry.meta.TenantID,
		RegisteredAt:  entry.registeredAt,
		LastFrameAt:   entry.lastFrameAt,
		FramesWritten: entry.framesWritten,
		BytesWritten:  entry.bytesWritten,
		Closed:        true,
		CloseReason:   reason,
	}
	onClose := entry.onClose
	entry.onClose = nil
	entry.mu.Unlock()

	r.mu.Lock()
	r.closed = append(r.closed, snap)
	if len(r.closed) > r.closedHistory {
		r.closed = r.closed[len(r.closed)-r.closedHistory:]
	}
	r.mu.Unlock()

	if onClose != nil {
		onClose(reason)
	}
}

// WriteFrame writes one frame to the client identified by requestID through
// the per-entry serialized write path with the per-write deadline (G7):
//
//   - entry missing → ErrConnectionNotRegistered;
//   - write exceeds writeTimeout → the client is considered disconnected:
//     the entry is unregistered (reason write_deadline), onClose fires and
//     ErrClientWriteDeadline is returned (UT-SK-09);
//   - underlying write error → returned verbatim; the entry stays
//     registered (the main data path's SerializedStreamWriter detach logic
//     owns plain-write-error disconnects).
func (r *ConnectionRegistry) WriteFrame(requestID, frame string) error {
	r.mu.RLock()
	entry, ok := r.entries[requestID]
	nowFn := r.now
	timeout := r.writeTimeout
	r.mu.RUnlock()
	if !ok {
		return ErrConnectionNotRegistered
	}

	entry.mu.Lock()
	if entry.closed {
		entry.mu.Unlock()
		return ErrConnectionClosed
	}
	// Watchdog: bound one frame write by the deadline. The late result of a
	// deadline-exceeded write is discarded (buffered channel) — a real
	// net.Conn would unblock via SetWriteDeadline; arbitrary writers may
	// simply finish late.
	done := make(chan error, 1)
	go func() { done <- entry.writer.WriteFrame(frame) }()
	var err error
	timer := time.NewTimer(timeout)
	select {
	case err = <-done:
		timer.Stop()
	case <-timer.C:
		// Client considered disconnected (N2/G7): mark, detach, notify.
		// Counters stay untouched — the frame never completed.
		entry.mu.Unlock()
		// Only unregister if the map still points at THIS entry: a reconnect
		// may have replaced the entry for the same requestID between the
		// timeout and now; blindly deleting would evict the live replacement
		// and wrongly fire onClose for it.
		r.mu.Lock()
		cur, ok := r.entries[requestID]
		if ok && cur == entry {
			delete(r.entries, requestID)
		}
		r.mu.Unlock()
		if ok && cur == entry {
			r.detach(entry, "write_deadline")
		}
		return ErrClientWriteDeadline
	}
	entry.lastFrameAt = nowFn()
	entry.framesWritten++
	entry.bytesWritten += uint64(len(frame))
	entry.mu.Unlock()
	return err
}

// WriteComment writes one SSE comment frame (": <text>\n\n") through the
// serialized side channel. Convenience for keepalive/thinking comments.
func (r *ConnectionRegistry) WriteComment(requestID, text string) error {
	return r.WriteFrame(requestID, ": "+text+"\n\n")
}

// Lookup returns the live-entry snapshot for requestID.
func (r *ConnectionRegistry) Lookup(requestID string) (ConnectionSnapshot, bool) {
	r.mu.RLock()
	entry, ok := r.entries[requestID]
	r.mu.RUnlock()
	if !ok {
		return ConnectionSnapshot{}, false
	}
	return entry.snapshot(false, ""), true
}

// List returns snapshots of all live entries (read-only metadata; no body
// content) ordered for stable admin projection (map walk is unordered, so
// callers sort as needed — the snapshot set is what matters).
func (r *ConnectionRegistry) List() []ConnectionSnapshot {
	r.mu.RLock()
	entries := make([]*registryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.mu.RUnlock()
	out := make([]ConnectionSnapshot, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.snapshot(false, ""))
	}
	return out
}

// ClosedHistory returns up to n most-recent closed-entry snapshots
// (注销原因 audit, admin projection).
func (r *ConnectionRegistry) ClosedHistory(n int) []ConnectionSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n <= 0 || len(r.closed) == 0 {
		return nil
	}
	if n > len(r.closed) {
		n = len(r.closed)
	}
	out := make([]ConnectionSnapshot, n)
	copy(out, r.closed[len(r.closed)-n:])
	// newest last → reverse to newest first for the admin list view.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Len reports the number of live entries.
func (r *ConnectionRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Capacity reports the configured entry bound.
func (r *ConnectionRegistry) Capacity() int {
	return r.capacity
}

// WriteTimeout reports the configured per-write deadline.
func (r *ConnectionRegistry) WriteTimeout() time.Duration {
	return r.writeTimeout
}
