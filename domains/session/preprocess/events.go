package preprocess

import (
	"context"
	"sync"
)

// EventKind enumerates the preprocess observation events (spec R11.9):
// raw_hit/build, sanitize_hit/build/degraded, compress_hit/build/variant_miss,
// lease wait, CAS conflict, artifact bytes/tokens saved.
type EventKind string

const (
	EventRawHit              EventKind = "raw_hit"
	EventRawBuild            EventKind = "raw_build"
	EventSanitizeHit         EventKind = "sanitize_hit"
	EventSanitizeBuild       EventKind = "sanitize_build"
	EventSanitizeDegraded    EventKind = "sanitize_degraded"
	EventCompressHit         EventKind = "compress_hit"
	EventCompressBuild       EventKind = "compress_build"
	EventCompressVariantMiss EventKind = "compress_variant_miss"
	EventLeaseWait           EventKind = "lease_wait"
	EventCASConflict         EventKind = "cas_conflict"
	EventArtifactSaved       EventKind = "artifact_saved" // bytes/tokens saved by reuse
)

// Event is the observation record emitted through EventSink.  Privacy rules
// (R11.9 / UT-SA-14): only hash prefixes (first 8 bytes, hex) and counters;
// never full hashes, never bodies, never sanitizer mappings.
type Event struct {
	Kind              EventKind
	TenantID          string
	SessionID         string
	RequestID         string
	Artifact          ArtifactKind
	Variant           string
	DepHashPrefix     string // first 8 bytes of the dependency hash, hex
	ContentHashPrefix string // first 8 bytes of the content hash, hex
	Count             int64  // generic counter (e.g. CAS attempts)
	Bytes             int64
	Tokens            int64
	AtUnixNano        int64
}

// EventSink receives preprocess events.  The package deliberately does not
// import liveactions/journey; integrators bridge this interface to the
// liveactions request-scoped action stream / journey events.
type EventSink interface {
	EmitPreprocessEvent(ctx context.Context, e Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(ctx context.Context, e Event)

func (f EventSinkFunc) EmitPreprocessEvent(ctx context.Context, e Event) { f(ctx, e) }

// NoopEventSink discards events.
type NoopEventSink struct{}

func (NoopEventSink) EmitPreprocessEvent(context.Context, Event) {}

// capturingSink is the in-test capture sink (race-safe: concurrent Preparations
// emit from multiple goroutines).
type capturingSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *capturingSink) EmitPreprocessEvent(_ context.Context, e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *capturingSink) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

func (c *capturingSink) countOf(k EventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Kind == k {
			n++
		}
	}
	return n
}

var _ EventSink = (EventSinkFunc)(nil)
var _ EventSink = NoopEventSink{}
