package upstream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

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
