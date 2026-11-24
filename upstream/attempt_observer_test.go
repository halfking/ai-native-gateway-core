package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/httpwrite"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type observedBody struct {
	attempt, status int
	body            string
}

type observedFinish struct {
	attempt, status int
	err             error
}

type observerCount struct {
	mu            sync.Mutex
	before, after int
	bodies        []observedBody
	writes        []int
	replays       []int
	finishes      []observedFinish
}

func (o *observerCount) ObserveRequestWrite(handle any, _ time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writes = append(o.writes, handle.(int))
}

func (o *observerCount) BeforeAttempt(req *http.Request) any {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.before++
	if IsTransportReplay(req) {
		o.replays = append(o.replays, o.before)
	}
	return o.before
}
func (o *observerCount) AfterAttempt(_ *http.Request, handle any, resp *http.Response, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.after++
	var status int
	if resp != nil {
		status = resp.StatusCode
	}
	o.finishes = append(o.finishes, observedFinish{attempt: handle.(int), status: status, err: err})
}
func (o *observerCount) ObserveErrorBody(handle any, resp *http.Response, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bodies = append(o.bodies, observedBody{attempt: handle.(int), status: resp.StatusCode, body: string(body)})
}

type attemptRoundTrip func(*http.Request) (*http.Response, error)

func (f attemptRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientAttemptObserverSeesEachInternalRetry(t *testing.T) {
	const failedBody = `{"usage":{"prompt_tokens":10}}`
	observer := &observerCount{}
	calls := 0
	client := NewWithRetries(1)
	defer client.Stop()
	client.baseDelay = 0
	httpClient := &http.Client{Transport: attemptRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		status := http.StatusOK
		body := `{"ok":true}`
		if calls == 1 {
			status = http.StatusServiceUnavailable
			body = failedBody
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://provider.invalid/v1/chat/completions", strings.NewReader(`{}`))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(`{}`)), nil }
	req = WithAttemptObserver(req, observer)
	resp, uErr := client.DoWithHTTPClient(req, httpClient)
	if uErr != nil {
		t.Fatalf("DoWithHTTPClient error: %v", uErr)
	}
	if resp == nil || calls != 2 || observer.before != 2 || observer.after != 2 {
		t.Fatalf("resp=%v sends=%d observer=%+v", resp, calls, observer)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("successful response body = %q, err=%v", body, err)
	}
	if len(observer.bodies) != 1 || observer.bodies[0] != (observedBody{attempt: 1, status: http.StatusServiceUnavailable, body: failedBody}) {
		t.Fatalf("error body observation attached to wrong attempt: %+v", observer.bodies)
	}
}

func TestClientAttemptErrorBodyRequiresCompleteCapture(t *testing.T) {
	const validPrefix = `{"usage":{"prompt_tokens":10}}`
	for _, tc := range []struct {
		name string
		body io.Reader
		size int
	}{
		{"bounded prefix", strings.NewReader(validPrefix + strings.Repeat(" ", maxErrorBodyBytes) + "suffix"), maxErrorBodyBytes},
		{"interrupted body", io.MultiReader(strings.NewReader(validPrefix), iotest.ErrReader(io.ErrUnexpectedEOF)), len(validPrefix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &observerCount{}
			client := NewWithRetries(0)
			defer client.Stop()
			transport := attemptRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(tc.body)}, nil
			})
			req, _ := http.NewRequest(http.MethodPost, "https://provider.invalid/v1/chat/completions", nil)
			resp, uErr := client.DoWithHTTPClient(WithAttemptObserver(req, observer), &http.Client{Transport: transport})
			if uErr == nil || resp == nil || len(uErr.Body) != tc.size {
				t.Fatalf("diagnostic response changed: response=%v error=%+v", resp, uErr)
			}
			defer resp.Body.Close()
			if observer.after != 1 || len(observer.bodies) != 0 {
				t.Fatalf("incomplete error used as usage evidence: %+v", observer.bodies)
			}
		})
	}
}

func TestClientAttemptObservesFullErrorBodyAtKnownLengthLimit(t *testing.T) {
	const json = `{"usage":{"prompt_tokens":10}}`
	body := json + strings.Repeat(" ", maxErrorBodyBytes-len(json))
	observer := &observerCount{}
	client := NewWithRetries(0)
	defer client.Stop()
	transport := attemptRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
			ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://provider.invalid/v1/chat/completions", nil)
	resp, uErr := client.DoWithHTTPClient(WithAttemptObserver(req, observer), &http.Client{Transport: transport})
	if uErr == nil || resp == nil || len(observer.bodies) != 1 || observer.bodies[0].body != body {
		t.Fatalf("complete body at the cap was not used: response=%v error=%+v", resp, uErr)
	}
	defer resp.Body.Close()
}

func TestObservedRedirectRespectsClientPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || string(body) != "payload" {
			t.Errorf("redirect method=%q body=%q err=%v", r.Method, body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name          string
		checkRedirect func(*http.Request, []*http.Request) error
		status        int
		sends         int
	}{
		{"follow", nil, http.StatusNoContent, 2},
		{"stop", func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, http.StatusTemporaryRedirect, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &observerCount{}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/first", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := DoObserved(WithAttemptObserver(req, observer), &http.Client{CheckRedirect: tc.checkRedirect})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			observer.mu.Lock()
			defer observer.mu.Unlock()
			if resp.StatusCode != tc.status || observer.before != tc.sends || observer.after != tc.sends {
				t.Fatalf("status=%d observed sends=%d/%d", resp.StatusCode, observer.before, observer.after)
			}
		})
	}
}

func TestLateRequestWriteKeepsOriginalRedirectHop(t *testing.T) {
	observer := &observerCount{}
	var firstWrite func(httptrace.WroteRequestInfo)
	transport := attemptRoundTrip(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		resp := &http.Response{Request: r, Header: make(http.Header), Body: http.NoBody, StatusCode: http.StatusOK}
		if r.URL.Path == "/first" {
			firstWrite = trace.WroteRequest
			resp.StatusCode = http.StatusTemporaryRedirect
			resp.Header.Set("Location", "/next")
		} else {
			firstWrite(httptrace.WroteRequestInfo{})
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return resp, nil
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.invalid/first", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := DoObserved(WithAttemptObserver(req, observer), &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !slices.Equal(observer.writes, []int{1, 2}) {
		t.Fatalf("write handles=%v, want [1 2]", observer.writes)
	}
}

func TestObservedHTTP1TransportReplayKeepsFinalBodyOnSecondSend(t *testing.T) {
	const requestBody = "payload"
	const replyBody = `{"usage":{"prompt_tokens":12}}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/warm" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != requestBody || r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "test-only" {
			t.Errorf("provider received method=%s body=%q err=%v", r.Method, body, err)
		}
		if requests.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack local HTTP connection: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, replyBody)
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: new(net.Dialer).DialContext}
	httpwrite.TrackTransport(transport)
	httpClient := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	defer httpClient.CloseIdleConnections()
	warm, err := httpClient.Get(server.URL + "/warm")
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Body.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/model", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "test-only")
	observer := &observerCount{}
	client := NewWithRetries(0)
	defer client.Stop()
	resp, upstreamErr := client.DoWithHTTPClient(WithAttemptObserver(req, observer), httpClient)
	if resp == nil || upstreamErr == nil || resp.StatusCode != http.StatusServiceUnavailable || requests.Load() != 2 {
		t.Fatalf("observed response=%v error=%v provider received=%d", resp, upstreamErr, requests.Load())
	}
	defer resp.Body.Close()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.before != 2 || !slices.Equal(observer.writes, []int{1, 2}) || !slices.Equal(observer.replays, []int{2}) {
		t.Fatalf("attempts=%d writes=%v replays=%v", observer.before, observer.writes, observer.replays)
	}
	if !slices.Equal(observer.finishes, []observedFinish{{1, 0, errTransportReplay}, {2, http.StatusServiceUnavailable, nil}}) {
		t.Fatalf("finish records=%+v", observer.finishes)
	}
	if !slices.Equal(observer.bodies, []observedBody{{2, http.StatusServiceUnavailable, replyBody}}) {
		t.Fatalf("error body observation=%+v", observer.bodies)
	}
}

type rejectFlushConn struct {
	net.Conn
	reject  atomic.Bool
	partial bool
}

func (c *rejectFlushConn) Write(data []byte) (int, error) {
	if c.reject.Swap(false) {
		n := 0
		if c.partial {
			n, _ = c.Conn.Write(data[:1])
		}
		return n, errors.New("fixture interrupted the socket flush")
	}
	return c.Conn.Write(data)
}

func TestObservedHTTP1ZeroByteFlushDoesNotConfirmSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked bool
		rows    int
	}{
		{"tracked socket", true, 1},
		{"opaque socket", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/model" {
					received.Add(1)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			var first atomic.Pointer[rejectFlushConn]
			dialer := new(net.Dialer)
			transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := dialer.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				wrapped := &rejectFlushConn{Conn: conn}
				first.CompareAndSwap(nil, wrapped)
				return wrapped, nil
			}}
			defer transport.CloseIdleConnections()
			if tc.tracked {
				httpwrite.TrackTransport(transport)
			}
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			warm, err := client.Get(server.URL + "/warm")
			if err != nil {
				t.Fatal(err)
			}
			_ = warm.Body.Close()
			first.Load().reject.Store(true)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/model", nil)
			if err != nil {
				t.Fatal(err)
			}
			var writeCallbacks atomic.Int32
			trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					writeCallbacks.Add(1)
				}
			}}
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
			observer := &observerCount{}
			resp, err := DoObserved(WithAttemptObserver(req, observer), client)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			observer.mu.Lock()
			defer observer.mu.Unlock()
			if resp.StatusCode != http.StatusNoContent || received.Load() != 1 || writeCallbacks.Load() != 2 {
				t.Fatalf("status=%d supplier receipts=%d Go callbacks=%d", resp.StatusCode, received.Load(), writeCallbacks.Load())
			}
			if observer.before != tc.rows || observer.after != tc.rows || !slices.Equal(observer.writes, []int{tc.rows}) {
				t.Fatalf("attempts=%d finishes=%d confirmed sends=%v", observer.before, observer.after, observer.writes)
			}
			if !tc.tracked && observer.finishes[0] != (observedFinish{attempt: 1, err: errTransportReplay}) {
				t.Fatalf("opaque first write was not kept uncertain: %+v", observer.finishes)
			}
		})
	}
}

func TestObservedHTTP1PartialFlushKeepsDeliveryUnconfirmed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the partial request unexpectedly reached the HTTP handler")
	}))
	defer server.Close()
	var goWrites atomic.Int32
	dialer := new(net.Dialer)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		wrapped := &rejectFlushConn{Conn: conn, partial: true}
		wrapped.reject.Store(true)
		return wrapped, nil
	}}
	httpwrite.TrackTransport(transport)
	defer transport.CloseIdleConnections()
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			goWrites.Add(1)
		}
	}}
	ctx := httptrace.WithClientTrace(t.Context(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	observer := &observerCount{}
	resp, err := DoObserved(WithAttemptObserver(req, observer), &http.Client{Transport: transport, Timeout: 3 * time.Second})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if resp != nil || err == nil || goWrites.Load() != 1 {
		t.Fatalf("fixture response=%v err=%v Go callbacks=%d", resp, err, goWrites.Load())
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.before != 1 || observer.after != 1 || len(observer.writes) != 0 || observer.finishes[0].err == nil {
		t.Fatalf("partial flush evidence attempts=%d sent=%v finishes=%+v", observer.before, observer.writes, observer.finishes)
	}
}

func TestAttemptTraceKeepsUncertainWritesAndCollapsesConnectionOnlyRetries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wroteHeaders bool
		writeFailed  bool
		attempts     int
	}{
		{"connection only", false, false, 1},
		{"write failed before headers", false, true, 2},
		{"headers without write result", true, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &observerCount{}
			transport := attemptRoundTrip(func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				trace.GotConn(httptrace.GotConnInfo{})
				if tc.wroteHeaders {
					trace.WroteHeaders()
				}
				if tc.writeFailed {
					trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("network write incomplete")})
				}
				trace.GotConn(httptrace.GotConnInfo{})
				trace.WroteRequest(httptrace.WroteRequestInfo{})
				return &http.Response{Request: req, StatusCode: http.StatusOK, Body: http.NoBody}, nil
			})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.invalid/", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := DoObserved(WithAttemptObserver(req, observer), &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if observer.before != tc.attempts || !slices.Equal(observer.writes, []int{tc.attempts}) || len(observer.finishes) != tc.attempts ||
				observer.finishes[tc.attempts-1] != (observedFinish{tc.attempts, http.StatusOK, nil}) {
				t.Fatalf("attempts=%d writes=%v finishes=%+v", observer.before, observer.writes, observer.finishes)
			}
			if tc.attempts > 1 && observer.finishes[0] != (observedFinish{1, 0, errTransportReplay}) {
				t.Fatalf("uncertain first attempt=%+v", observer.finishes[0])
			}
		})
	}
}

func TestObservedHTTP2RefusedStreamUsesSeparateAttempts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer conn.Close()
		serverResult <- refuseFirstHTTP2Stream(conn)
	}()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: 4 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/model", nil)
	if err != nil {
		t.Fatal(err)
	}
	observer := &observerCount{}
	resp, err := DoObserved(WithAttemptObserver(req, observer), client)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if resp.ProtoMajor != 2 || observer.before != 2 || !slices.Equal(observer.replays, []int{2}) || !slices.Equal(observer.writes, []int{2}) ||
		!slices.Equal(observer.finishes, []observedFinish{{1, 0, errTransportReplay}, {2, http.StatusOK, nil}}) {
		t.Fatalf("protocol=%s attempts=%d replays=%v confirmed=%v finishes=%+v", resp.Proto, observer.before, observer.replays, observer.writes, observer.finishes)
	}
}

func refuseFirstHTTP2Stream(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return err
	}
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != http2.ClientPreface {
		return errors.New("unexpected HTTP/2 preface")
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		return err
	}
	refused := false
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			return err
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				if err := framer.WriteSettingsAck(); err != nil {
					return err
				}
			}
		case *http2.HeadersFrame:
			if !refused {
				if err := framer.WriteRSTStream(f.StreamID, http2.ErrCodeRefusedStream); err != nil {
					return err
				}
				refused = true
				continue
			}
			var header bytes.Buffer
			if err := hpack.NewEncoder(&header).WriteField(hpack.HeaderField{Name: ":status", Value: "200"}); err != nil {
				return err
			}
			return framer.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndStream: true, EndHeaders: true, BlockFragment: header.Bytes()})
		}
	}
}
