package state

import (
	"sync"
	"time"
)

// KeyInfo captures the resolved identity of the API key that will be
// billed for this request. Populated when leaving StateReceived.
type KeyInfo struct {
	ID       string
	TenantID string
	Scopes   []string
}

// Candidate describes a routing candidate. Score is the routing score
// produced by the routing domain; higher is better.
type Candidate struct {
	ProviderID string
	Model      string
	Score      float64
}

// Policy carries routing policy overrides for this request.
type Policy struct {
	Name   string
	Params map[string]any
}

// RetryContext summarises the retry chain that produced this request.
type RetryContext struct {
	Attempt     int
	MaxAttempts int
	LastError   string
}

// PreStream is the keep-alive ticker that writes ": keep-alive\n\n"
// frames before the upstream first byte arrives. Runtime invokes Stop()
// on every terminal transition so the ticker cannot leak past
// cancellation.
type PreStream interface {
	Stop()
}

// Compressor is the body compression worker. Done() returns a channel
// that closes when compression finishes; the runtime observes it via
// the EventCompressingDone / EventCompressingSkipped transition.
type Compressor interface {
	Done() <-chan struct{}
	Output() []byte
}

// Executor is the upstream dispatcher. It is a marker interface here —
// concrete implementations live in the executor domain and are injected
// by the higher-level handler.
type Executor interface{}

// Armor is the response wrapping stage (e.g. tool_call reconstruction).
// Marker for now; the runtime only tracks its lifecycle.
type Armor interface{}

// StreamWriter is the boundary that emits SSE frames back to the
// client. It is the single point through which the runtime writes data
// and the sentinel "[DONE]" frame, and its Closed() flag prevents the
// runtime from double-writing after cancellation.
type StreamWriter interface {
	WriteFrame(payload []byte) (int, error)
	WriteDone() error
	Closed() bool
}

// EventLogEntry is a structured record of a single state transition.
// Useful for audit / debugging and for the test assertions.
type EventLogEntry struct {
	At    time.Time
	From  RequestState
	To    RequestState
	Event Event
	Note  string
}

// RequestContext holds all per-request state required by the runtime.
// It is intentionally non-pointer-to-interface heavy: most fields are
// populated once (in the constructor / setters) and only the lifecycle
// hooks (cancel, eventLog, preStream, etc.) are mutated. All mutations
// go through Runtime, never directly, to keep the lock discipline
// simple: there is exactly one writer (the runtime) and the runtime
// serialises every transition.
type RequestContext struct {
	mu sync.RWMutex

	// Identity & request payload — set once at construction.
	requestID  string
	tenantID   string
	keyInfo    *KeyInfo
	bodyBytes  []byte
	model      string
	candidates []Candidate
	policy     *Policy
	retryCtx   *RetryContext

	// Cancellation: Once-guarded so Cancel() is idempotent across
	// multiple signal sources (client, upstream, watchdog).
	cancelOnce sync.Once
	cancelled  chan struct{}
	cancelErr  error

	// Lifecycle hooks / collaborators.
	preStream    PreStream
	compressor   Compressor
	executor     Executor
	armor        Armor
	streamWriter StreamWriter

	// Append-only event log.
	eventLog []EventLogEntry
}

// NewRequestContext returns an empty RequestContext with the
// cancellation channel initialised. Callers populate the rest of the
// fields through the setters; the runtime reads them with read locks.
func NewRequestContext(requestID, tenantID string) *RequestContext {
	return &RequestContext{
		requestID: requestID,
		tenantID:  tenantID,
		cancelled: make(chan struct{}),
	}
}

// SetKeyInfo stores the resolved key identity.
func (c *RequestContext) SetKeyInfo(k *KeyInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keyInfo = k
}

// SetBody stores the request body.
func (c *RequestContext) SetBody(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodyBytes = b
}

// SetModel stores the requested model name.
func (c *RequestContext) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = m
}

// SetCandidates stores the routing candidate set.
func (c *RequestContext) SetCandidates(cands []Candidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.candidates = cands
}

// SetPolicy stores the routing policy override.
func (c *RequestContext) SetPolicy(p *Policy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = p
}

// SetRetryCtx stores the retry chain context.
func (c *RequestContext) SetRetryCtx(r *RetryContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retryCtx = r
}

// SetPreStream installs the keep-alive ticker.
func (c *RequestContext) SetPreStream(p PreStream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preStream = p
}

// SetCompressor installs the body compression worker.
func (c *RequestContext) SetCompressor(c2 Compressor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compressor = c2
}

// SetExecutor installs the upstream dispatcher.
func (c *RequestContext) SetExecutor(e Executor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executor = e
}

// SetArmor installs the response wrapping stage.
func (c *RequestContext) SetArmor(a Armor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armor = a
}

// SetStreamWriter installs the SSE writer.
func (c *RequestContext) SetStreamWriter(w StreamWriter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamWriter = w
}

// RequestID returns the immutable request id.
func (c *RequestContext) RequestID() string { return c.requestID }

// TenantID returns the immutable tenant id.
func (c *RequestContext) TenantID() string { return c.tenantID }

// KeyInfo returns a snapshot of the resolved key identity.
func (c *RequestContext) KeyInfo() *KeyInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keyInfo
}

// Body returns a snapshot of the request body bytes.
func (c *RequestContext) Body() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]byte, len(c.bodyBytes))
	copy(out, c.bodyBytes)
	return out
}

// Model returns the requested model name.
func (c *RequestContext) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// Candidates returns a snapshot of the routing candidates.
func (c *RequestContext) Candidates() []Candidate {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Candidate, len(c.candidates))
	copy(out, c.candidates)
	return out
}

// Policy returns the routing policy override.
func (c *RequestContext) Policy() *Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.policy
}

// RetryCtx returns the retry chain context.
func (c *RequestContext) RetryCtx() *RetryContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.retryCtx
}

// Compressor returns the installed compressor (may be nil).
func (c *RequestContext) Compressor() Compressor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compressor
}

// PreStream returns the installed keep-alive ticker (may be nil).
func (c *RequestContext) PreStream() PreStream {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.preStream
}

// StreamWriter returns the installed SSE writer (may be nil).
func (c *RequestContext) StreamWriter() StreamWriter {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.streamWriter
}

// Cancelled returns the cancellation channel. It is closed exactly once
// regardless of how many callers invoke Cancel.
func (c *RequestContext) Cancelled() <-chan struct{} { return c.cancelled }

// CancelErr returns the cancellation reason (nil until Cancel runs).
func (c *RequestContext) CancelErr() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cancelErr
}

// EventLog returns a snapshot of the transition log.
func (c *RequestContext) EventLog() []EventLogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]EventLogEntry, len(c.eventLog))
	copy(out, c.eventLog)
	return out
}

// appendEventLog is called by the runtime under the runtime's own
// write lock — it does not take c.mu because the runtime serialises
// every transition.
func (c *RequestContext) appendEventLog(e EventLogEntry) {
	c.eventLog = append(c.eventLog, e)
}

// performCancel is invoked by Runtime.Cancel under the runtime's lock.
// It closes the channel exactly once and records the reason.
func (c *RequestContext) performCancel(err error) {
	c.cancelOnce.Do(func() {
		c.cancelErr = err
		close(c.cancelled)
	})
}
