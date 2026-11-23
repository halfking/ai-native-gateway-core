package httpwrite

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// TrackTransport adds local HTTP/1 write evidence to an owned transport with
// an explicit DialContext. Call it before the transport is used. TLS and
// unencrypted HTTP/2 connections use HTTP response evidence.
func TrackTransport(transport *http.Transport) {
	dial := transport.DialContext
	if dial == nil || (transport.Protocols != nil && transport.Protocols.UnencryptedHTTP2()) {
		return
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &socketWriteConn{Conn: conn}, nil
	}
}

type socketWriteConn struct {
	net.Conn
	active atomic.Pointer[Observation]
}

// Observation records the first successful byte transfer for one request.
type Observation struct {
	firstNanos atomic.Int64
	awaitFlush atomic.Bool
	flushed    atomic.Bool
	failed     atomic.Bool
	observe    func(*Observation)
}

// Observe attaches to a tracked HTTP/1 connection for the duration of a request.
func Observe(conn net.Conn, notify func(*Observation)) (*Observation, func()) {
	tracked, ok := conn.(*socketWriteConn)
	if !ok {
		return nil, nil
	}
	observation := &Observation{observe: notify}
	tracked.active.Store(observation)
	return observation, func() { tracked.active.CompareAndSwap(observation, nil) }
}

// FirstWrite returns the time at which the connection first accepted bytes.
func (o *Observation) FirstWrite() time.Time {
	if o != nil {
		if n := o.firstNanos.Load(); n != 0 {
			return time.Unix(0, n).UTC()
		}
	}
	return time.Time{}
}

// WatchFlush follows a successful Go request callback. The HTTP/1 write loop
// can still have buffered request bytes to flush after that callback.
func (o *Observation) WatchFlush() {
	if o != nil {
		o.awaitFlush.Store(true)
	}
}

// ConfirmedWrite requires a successful final socket flush or a completed
// writer with no socket error and at least one byte written.
func (o *Observation) ConfirmedWrite(writerDone bool) time.Time {
	if o == nil || o.failed.Load() || !(writerDone || o.flushed.Load()) {
		return time.Time{}
	}
	return o.FirstWrite()
}

func (c *socketWriteConn) Write(p []byte) (int, error) {
	evidence := c.active.Load()
	flush := evidence != nil && evidence.awaitFlush.Load()
	n, err := c.Conn.Write(p)
	if evidence != nil {
		if n > 0 {
			evidence.firstNanos.CompareAndSwap(0, time.Now().UnixNano())
		}
		if err != nil || n != len(p) {
			evidence.failed.Store(true)
		} else if flush {
			evidence.flushed.Store(true)
			evidence.observe(evidence)
		}
	}
	return n, err
}
