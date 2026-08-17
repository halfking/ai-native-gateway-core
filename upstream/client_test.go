package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestRetryAfterFromHeaders(t *testing.T) {
	t.Run("delta seconds", func(t *testing.T) {
		got := RetryAfterFromHeaders(http.Header{"Retry-After": []string{"7"}})
		if got != 7*time.Second {
			t.Fatalf("retry-after = %s, want 7s", got)
		}
	})
	t.Run("reset timestamp wins", func(t *testing.T) {
		reset := time.Now().Add(11 * time.Second).Unix()
		headers := make(http.Header)
		headers.Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset))
		headers.Set("Retry-After", "1")
		got := RetryAfterFromHeaders(headers)
		if got < 10*time.Second || got > 11*time.Second {
			t.Fatalf("reset retry-after = %s, want about 11s", got)
		}
	})
	t.Run("http date", func(t *testing.T) {
		reset := time.Now().Add(9 * time.Second)
		got := RetryAfterFromHeaders(http.Header{"Retry-After": []string{reset.UTC().Format(http.TimeFormat)}})
		if got < 8*time.Second || got > 9*time.Second {
			t.Fatalf("date retry-after = %s, want about 9s", got)
		}
	})
	t.Run("invalid and past values return zero", func(t *testing.T) {
		if got := RetryAfterFromHeaders(http.Header{"Retry-After": []string{"nope"}}); got != 0 {
			t.Fatalf("invalid retry-after = %s, want zero", got)
		}
		if got := RetryAfterFromHeaders(http.Header{"Retry-After": []string{"0"}}); got != 0 {
			t.Fatalf("zero retry-after = %s, want zero", got)
		}
	})
	t.Run("huge delta capped at 31 days", func(t *testing.T) {
		got := RetryAfterFromHeaders(http.Header{"Retry-After": []string{"99999999"}})
		const maxCap = 31 * 24 * time.Hour
		if got != maxCap {
			t.Fatalf("huge retry-after = %s, want %s (31d cap)", got, maxCap)
		}
	})
}

func TestDo_PreservesRetryAfterOn429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "13")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewWithRetries(0)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, uErr := client.Do(req)
	if resp == nil || uErr != nil {
		t.Fatalf("Do() response/error = %v/%v, want response and nil error", resp, uErr)
	}
	if got := RetryAfterFromHeaders(resp.Header); got != 13*time.Second {
		t.Fatalf("retry-after = %s, want 13s", got)
	}
}

func TestDo_PreservesRetryAfterOnRetryExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewWithRetries(0)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, uErr := client.Do(req)
	if uErr == nil || uErr.RetryAfter != 5*time.Second {
		t.Fatalf("error retry-after = %v, want 5s", uErr)
	}
}

func TestDo_SuccessFirstTry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer server.Close()

	client := New()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte(`{"model":"gpt-4"}`)))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte(`{"model":"gpt-4"}`))), nil
	}

	resp, uErr := client.Do(req)
	if uErr != nil {
		t.Fatalf("unexpected error: %v", uErr)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDo_RetryOn500(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New()
	body := []byte(`{"model":"gpt-4"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, uErr := client.Do(req)
	if uErr != nil {
		t.Fatalf("unexpected error: %v", uErr)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestDo_RetryBodyRewind(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{
		hc:         &http.Client{Timeout: 5 * time.Second},
		maxRetries: 1,
		baseDelay:  10 * time.Millisecond,
	}

	originalBody := []byte(`{"model":"gpt-4","prompt":"hello"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(originalBody))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(originalBody)), nil
	}

	_, _ = client.Do(req)

	if len(bodies) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(bodies))
	}
	for i, body := range bodies {
		if body != string(originalBody) {
			t.Errorf("attempt %d: body mismatch.\n  got:  %q\n  want: %q", i+1, body, string(originalBody))
		}
	}
}

func TestDo_ExhaustRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{
		hc:         &http.Client{Timeout: 5 * time.Second},
		maxRetries: 2,
		baseDelay:  10 * time.Millisecond,
	}
	body := []byte(`{}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	_, uErr := client.Do(req)
	if uErr == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if uErr.Kind != KindUpstreamDown {
		t.Errorf("expected KindUpstreamDown, got %q", uErr.Kind)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", attempts.Load())
	}
}

func TestDo_ConnectionError(t *testing.T) {
	client := &Client{
		hc:         &http.Client{Timeout: 2 * time.Second},
		maxRetries: 1,
		baseDelay:  10 * time.Millisecond,
	}
	body := []byte(`{}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:1/fail", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	_, uErr := client.Do(req)
	if uErr == nil {
		t.Fatal("expected error for connection refused")
	}
	if !errorsx.IsRetryable(errorsx.ErrorKind(uErr.Kind)) {
		t.Errorf("expected retryable kind for connection error, got %q", uErr.Kind)
	}
}

type otherSideClosedRoundTripper struct {
	attempts atomic.Int32
}

func (rt *otherSideClosedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.attempts.Add(1)
	return nil, fmt.Errorf("provider transport: other side closed")
}

func TestDo_OtherSideClosedIsNetworkRetryable(t *testing.T) {
	rt := &otherSideClosedRoundTripper{}
	client := &Client{
		hc:         &http.Client{Transport: rt},
		maxRetries: 1,
		baseDelay:  time.Millisecond,
	}
	body := []byte(`{}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://provider.test/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	_, uErr := client.Do(req)
	if uErr == nil {
		t.Fatal("expected other-side-closed error")
	}
	if uErr.Kind != KindNetwork {
		t.Fatalf("error kind = %q, want %q", uErr.Kind, KindNetwork)
	}
	if !errorsx.IsRetryable(uErr.Kind) {
		t.Fatalf("error kind %q must be retryable", uErr.Kind)
	}
	if got := rt.attempts.Load(); got != 2 {
		t.Fatalf("round trips = %d, want 2", got)
	}
}

func TestDo_NonRetryable429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := &Client{
		hc:         &http.Client{Timeout: 5 * time.Second},
		maxRetries: 2,
		baseDelay:  10 * time.Millisecond,
	}
	body := []byte(`{}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, uErr := client.Do(req)
	if uErr != nil {
		t.Fatalf("unexpected error: %v", uErr)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry for 429), got %d", attempts.Load())
	}
}

func TestNewWithRetries_UsesRequestContextForFullDeadline(t *testing.T) {
	client := NewWithRetries(0)
	if client.hc.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %s, want 0", client.hc.Timeout)
	}
	transport, ok := client.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport type = %T, want *http.Transport", client.hc.Transport)
	}
	if transport.ResponseHeaderTimeout != defaultHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, defaultHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout <= 0 || transport.ExpectContinueTimeout <= 0 {
		t.Fatalf("transport handshake timeouts must remain bounded: tls=%s expect=%s", transport.TLSHandshakeTimeout, transport.ExpectContinueTimeout)
	}
}

func TestNewWithRetries_DefaultResponseHeaderTimeout(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RESPONSE_HEADER_TIMEOUT", "")
	client := NewWithRetries(0)
	transport, ok := client.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport type = %T, want *http.Transport", client.hc.Transport)
	}
	if transport.ResponseHeaderTimeout != 120*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 2m", transport.ResponseHeaderTimeout)
	}
}

func TestNewWithRetries_ResponseHeaderTimeoutOverride(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "seconds", value: "45", want: 45 * time.Second},
		{name: "duration", value: "75s", want: 75 * time.Second},
		{name: "invalid", value: "nope", want: 120 * time.Second},
		{name: "non-positive", value: "0", want: 120 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_RESPONSE_HEADER_TIMEOUT", tc.value)
			client := NewWithRetries(0)
			transport, ok := client.hc.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("client transport type = %T, want *http.Transport", client.hc.Transport)
			}
			if transport.ResponseHeaderTimeout != tc.want {
				t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, tc.want)
			}
		})
	}
}

func TestCaptureErrorBodyRestoresBody(t *testing.T) {
	const body = `{"error":{"message":"upstream unavailable"}}`
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	got := captureErrorBody(resp, true)
	if string(got) != body {
		t.Fatalf("captured body = %q, want %q", got, body)
	}
	restored, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored response body failed: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored response body = %q, want %q", restored, body)
	}
}

func TestDo_ExhaustedRetryResponseBodyRemainsReadable(t *testing.T) {
	const body = `{"error":{"message":"upstream unavailable"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := NewWithRetries(0)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	resp, uErr := client.Do(req)
	if uErr == nil || resp == nil {
		t.Fatalf("Do() error/response = %v/%v, want both", uErr, resp)
	}
	defer resp.Body.Close()
	restored, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read exhausted response body failed: %v", err)
	}
	if string(restored) != body {
		t.Fatalf("restored response body = %q, want %q", restored, body)
	}
}

// TestError_NilReceiver_2026_07_20 documents and guards against the
// nil-pointer dereference that crashed minimax-m3 / gpt-5.6-luna chat
// requests with `panic: runtime error: invalid memory address or nil
// pointer dereference` at upstream/client.go:61.
//
// The crash trigger was Go's classic "typed-nil wrapped in non-nil
// error interface" gotcha:
//
//	var uErr *upstream.Error = nil       // typed nil pointer
//	var iface error = uErr               // iface != nil (has type tag)
//	iface.Error()                        // used to dereference nil uErr
//
// `ClassifyResult(uErr, statusCode)` previously masked this with a
// defer-recover; the proper fix is to make (*Error).Error() and
// (*Error).Unwrap() nil-receiver safe so the panic cannot happen at
// any call site.
func TestError_NilReceiver_2026_07_20(t *testing.T) {
	var nilPtr *Error // typed-nil pointer, NOT a nil interface

	// Sanity-check the gotcha: typed-nil pointer wrapped in an interface
	// is NOT nil at the interface level. This is the precondition for
	// the bug we are guarding against.
	var iface error = nilPtr
	if iface == nil {
		t.Fatalf("typed-nil wrapped in interface must be non-nil; test setup is wrong")
	}

	// Guard 1: (*Error).Error() must not panic and must return a
	// deterministic string when called on a typed-nil receiver.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("(*Error).Error() panicked on nil receiver: %v", r)
		}
	}()
	if got := nilPtr.Error(); got == "" {
		t.Fatalf("(*Error).Error() returned empty string for nil receiver; want sentinel like <nil upstream.Error>")
	}

	// Guard 2: (*Error).Unwrap() must not panic and must return nil
	// when called on a typed-nil receiver.
	if got := nilPtr.Unwrap(); got != nil {
		t.Fatalf("(*Error).Unwrap() returned %v on nil receiver; want nil", got)
	}
	if got := nilPtr.UpstreamStatusCode(); got != 0 {
		t.Fatalf("(*Error).UpstreamStatusCode() returned %d on nil receiver; want 0", got)
	}
	if got := nilPtr.UpstreamBody(); got != nil {
		t.Fatalf("(*Error).UpstreamBody() returned %q on nil receiver; want nil", got)
	}
}

// TestError_NonNilReceiver_2026_07_20 confirms the nil-receiver guards
// above did not regress the normal path. A populated *Error must still
// render its full "[Kind] Message: Err" string.
func TestError_NonNilReceiver_2026_07_20(t *testing.T) {
	e := &Error{
		Kind:       KindRateLimit,
		Message:    "rate limited",
		Err:        fmt.Errorf("429 hit"),
		StatusCode: 429,
		Body:       []byte(`{"error":"rate limited"}`),
	}
	got := e.Error()
	want := "[rate_limit] rate limited: 429 hit"
	if got != want {
		t.Fatalf("(*Error).Error() = %q, want %q", got, want)
	}
	if err := e.Unwrap(); err == nil || err.Error() != "429 hit" {
		t.Fatalf("(*Error).Unwrap() returned %v, want non-nil with %q", err, "429 hit")
	}
	if got := e.UpstreamStatusCode(); got != 429 {
		t.Fatalf("(*Error).UpstreamStatusCode() = %d, want 429", got)
	}
	if got := string(e.UpstreamBody()); got != `{"error":"rate limited"}` {
		t.Fatalf("(*Error).UpstreamBody() = %q, want captured body", got)
	}
}

// The 2026-08-08 apiclaude.cc body, verbatim from 154's journald. Kept as a
// package-level literal so the classification and body-preservation tests
// cannot drift apart from each other.
const overloadBody = `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"upstream_error"}}`

// TestDo_OverloadBodyOn502IsUpstreamOverloaded pins the 2026-08-08 fix.
//
// Do() classified 5xx responses from the status code alone, so a relay that
// answered 502 with "our servers are currently overloaded, please try again
// later" was indistinguishable from a relay that was actually down: both
// became KindUpstreamDown. 154 logged 14 such responses in 24h as
// err_kind=upstream_down while the very next attempt on the same credential
// returned 200 — the body said "transient" and we threw it away.
func TestDo_OverloadBodyOn502IsUpstreamOverloaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(overloadBody))
	}))
	defer server.Close()

	client := NewWithRetries(0)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, uErr := client.Do(req)
	if uErr == nil {
		t.Fatal("expected an error for 502")
	}
	if uErr.Kind != errorsx.KindUpstreamOverloaded {
		t.Errorf("Kind = %q, want %q", uErr.Kind, errorsx.KindUpstreamOverloaded)
	}
	// The diagnostic message must carry the upstream's own words, otherwise
	// request_logs.response_preview is useless for post-mortems.
	if !strings.Contains(uErr.Message, "currently overloaded") {
		t.Errorf("Message = %q, want it to contain the upstream text", uErr.Message)
	}
	// captureErrorBody must have restored the stream: downstream diagnostic
	// readers run after classification and would otherwise see an empty body.
	if resp == nil || resp.Body == nil {
		t.Fatal("expected a readable response body after classification")
	}
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "currently overloaded") {
		t.Errorf("restored body = %q, want the original payload", string(rest))
	}
}

// TestDo_5xxClassificationBoundaries locks the scope of the new kind. Only
// 500/502 with an overload-shaped body may become KindUpstreamOverloaded:
// 503/529 remain KindConcurrent because those statuses are a genuine
// concurrency signal that credentialhealth's tuner deliberately reacts to by
// ratcheting concurrency_limit_auto down, and a 5xx with an ordinary body
// remains KindUpstreamDown.
func TestDo_5xxClassificationBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   errorsx.ErrorKind
	}{
		{"500_overload_body", 500, overloadBody, errorsx.KindUpstreamOverloaded},
		{"502_overload_body", 502, overloadBody, errorsx.KindUpstreamOverloaded},
		{"502_ordinary_body_stays_upstream_down", 502, `bad gateway`, errorsx.KindUpstreamDown},
		{"500_ordinary_body_stays_upstream_down", 500, `internal server error`, errorsx.KindUpstreamDown},
		{"503_overload_body_stays_concurrent", 503, overloadBody, errorsx.KindConcurrent},
		{"529_overload_body_stays_concurrent", 529, overloadBody, errorsx.KindConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := NewWithRetries(0)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			_, uErr := client.Do(req)
			if uErr == nil {
				t.Fatalf("status %d: expected an error", tc.status)
			}
			if uErr.Kind != tc.want {
				t.Errorf("status %d body %q: Kind = %q, want %q", tc.status, tc.body, uErr.Kind, tc.want)
			}
		})
	}
}

// TestClampInFlightRetryAfter guards the second, tighter bound added in
// 2026-08-08. clampRetryAfter's 31-day ceiling is sized for a credential
// cooling window written to the DB; reusing it to time a sleep while the
// caller's connection is open would let one malformed header hang a live
// request for weeks.
func TestClampInFlightRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero means no hint", 0, 0},
		{"negative means no hint", -3 * time.Second, 0},
		{"short hint passes through", 3 * time.Second, 3 * time.Second},
		{"at the ceiling", maxInFlightRetryAfter, maxInFlightRetryAfter},
		{"long hint is capped", 600 * time.Second, maxInFlightRetryAfter},
		{"absurd hint is capped", maxRetryAfter, maxInFlightRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampInFlightRetryAfter(tc.in); got != tc.want {
				t.Errorf("ClampInFlightRetryAfter(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestDo_HonorsRetryAfterForBackoff verifies the provider sets the pace.
// Before this fix every delay was a blind 500ms*2^attempt, so an upstream
// asking for 1s was retried at 500ms — too early to help, and the wasted
// attempt counted against the retry budget.
func TestDo_HonorsRetryAfterForBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(overloadBody))
	}))
	defer server.Close()

	// baseDelay is deliberately 10ms: if the Retry-After hint were ignored
	// the second attempt would fire almost immediately and the elapsed-time
	// assertion below would fail.
	client := &Client{
		hc:         &http.Client{Timeout: 5 * time.Second},
		maxRetries: 1,
		baseDelay:  10 * time.Millisecond,
	}
	body := []byte(`{}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	start := time.Now()
	_, uErr := client.Do(req)
	elapsed := time.Since(start)

	if uErr == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (1 + 1 retry)", attempts.Load())
	}
	if elapsed < time.Second {
		t.Errorf("elapsed = %s, want >= 1s (the upstream's Retry-After was ignored)", elapsed)
	}
	// Sanity bound: honouring the hint must not compound into a long stall.
	if elapsed > 4*time.Second {
		t.Errorf("elapsed = %s, want < 4s", elapsed)
	}
}
