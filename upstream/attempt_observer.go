package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/httpwrite"
)

// AttemptObserver is notified around transport calls, including HTTP redirects
// and observable resends inside a transport call. The opaque handle is returned
// unchanged to AfterAttempt; implementations must not send provider traffic.
type AttemptObserver interface {
	BeforeAttempt(*http.Request) any
	AfterAttempt(req *http.Request, handle any, resp *http.Response, err error)
}

// AttemptWriteObserver receives confirmed local send evidence. The time is
// taken at the socket write or the associated request write callback.
type AttemptWriteObserver interface {
	ObserveRequestWrite(handle any, at time.Time)
}

// AttemptErrorBodyObserver receives complete error bodies already captured by
// the retry client. The handle identifies the physical send that returned them.
// The body is read-only and valid for the duration of the callback.
type AttemptErrorBodyObserver interface {
	ObserveErrorBody(handle any, resp *http.Response, body []byte)
}

type observedAttempt struct {
	bodyObserver AttemptErrorBodyObserver
	handle       any
}

type attemptObserverKey struct{}
type transportReplayKey struct{}

// IsTransportReplay reports whether the observer is preparing a resend inside
// one RoundTrip, as opposed to the caller starting another RoundTrip.
func IsTransportReplay(req *http.Request) bool {
	replay, _ := req.Context().Value(transportReplayKey{}).(bool)
	return replay
}

func WithAttemptObserver(req *http.Request, observer AttemptObserver) *http.Request {
	if req == nil || observer == nil {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), attemptObserverKey{}, observer))
}

// DoObserved lets executor direct-client fallbacks observe the same transport
// calls as Client retries.
func DoObserved(req *http.Request, client *http.Client) (*http.Response, error) {
	resp, err, _ := doObserved(req, client)
	return resp, err
}

func doObserved(req *http.Request, client *http.Client) (*http.Response, error, observedAttempt) {
	if client == nil {
		return nil, &Error{Message: "nil upstream HTTP client"}, observedAttempt{}
	}
	observer, _ := req.Context().Value(attemptObserverKey{}).(AttemptObserver)
	if observer == nil {
		resp, err := client.Do(req)
		return resp, err, observedAttempt{}
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	bodyObserver, _ := observer.(AttemptErrorBodyObserver)
	observed := &attemptTransport{transport: transport, observer: observer}
	requestClient := *client
	requestClient.Transport = observed
	resp, err := requestClient.Do(req)
	return resp, err, observedAttempt{bodyObserver: bodyObserver, handle: observed.latest}
}

type attemptTransport struct {
	transport http.RoundTripper
	observer  AttemptObserver
	latest    any
}

func (t *attemptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	handle := t.observer.BeforeAttempt(req)
	var traced *attemptTrace
	if writer, ok := t.observer.(AttemptWriteObserver); ok {
		traced = &attemptTrace{observer: t.observer, writer: writer, request: req, handle: handle}
		trace := &httptrace.ClientTrace{
			GotConn:      traced.gotConn,
			WroteHeaders: traced.wroteHeaders,
			WroteRequest: traced.wroteRequest,
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	}
	resp, err := t.transport.RoundTrip(req)
	if traced != nil {
		handle = traced.finish(resp != nil && err == nil)
	}
	t.latest = handle
	t.observer.AfterAttempt(req, handle, resp, err)
	return resp, err
}

var errTransportReplay = errors.New("transport retried without a final response")

// attemptTrace keeps the handle for each write on the current transport leg.
// Go waits for the prior writer before starting a retry. A final write callback
// can still arrive after RoundTrip returns, so the last handle stays available.
type attemptTrace struct {
	mu           sync.Mutex
	observer     AttemptObserver
	writer       AttemptWriteObserver
	request      *http.Request
	handle       any
	got          bool
	started      bool
	finished     bool
	response     bool
	confirmed    bool
	writeAt      time.Time
	detachSocket func()
	socket       *httpwrite.Observation
}

func (t *attemptTrace) gotConn(info httptrace.GotConnInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	// Go waits for the earlier writer before retrying. A tracked HTTP/1
	// connection with no socket bytes can reuse the original intent.
	if t.got && t.started && (t.socket == nil || !t.socket.FirstWrite().IsZero()) {
		t.confirmWrite(true)
		t.observer.AfterAttempt(t.request, t.handle, nil, errTransportReplay)
		replay := t.request.WithContext(context.WithValue(t.request.Context(), transportReplayKey{}, true))
		t.handle = t.observer.BeforeAttempt(replay)
	}
	t.releaseSocket()
	t.got = true
	t.started = false
	t.confirmed = false
	t.writeAt = time.Time{}
	t.socket, t.detachSocket = httpwrite.Observe(info.Conn, t.socketWrote)
}

func (t *attemptTrace) wroteHeaders() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
}

func (t *attemptTrace) wroteRequest(info httptrace.WroteRequestInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Even when the write fails, partial request bytes may have left the client.
	t.started = true
	if info.Err == nil {
		t.writeAt = time.Now().UTC()
		t.socket.WatchFlush()
		t.confirmWrite(false)
	}
}

func (t *attemptTrace) socketWrote(evidence *httpwrite.Observation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.socket == evidence {
		t.confirmWrite(false)
	}
}

func (t *attemptTrace) confirmWrite(writerDone bool) {
	if t.confirmed || t.writeAt.IsZero() {
		return
	}
	at := t.socket.ConfirmedWrite(writerDone)
	if at.IsZero() {
		if !t.response {
			return
		}
		at = t.writeAt
	}
	t.confirmed = true
	t.writer.ObserveRequestWrite(t.handle, at)
}

func (t *attemptTrace) releaseSocket() {
	if t.detachSocket != nil {
		t.detachSocket()
		t.detachSocket = nil
	}
}

func (t *attemptTrace) finish(response bool) any {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.finished = true
	t.response = response
	t.confirmWrite(true)
	t.releaseSocket()
	return t.handle
}
