package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type countingRoundTripper struct {
	calls atomic.Int64
}

func (r *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

func TestDoWithHTTPClientUsesSuppliedTransport(t *testing.T) {
	transport := &countingRoundTripper{}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/v1/chat", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	upstream := NewWithRetries(0)
	defer upstream.Stop()
	resp, uErr := upstream.DoWithHTTPClient(req, client)
	if uErr != nil {
		t.Fatalf("DoWithHTTPClient error: %v", uErr)
	}
	defer resp.Body.Close()
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("supplied transport calls = %d, want 1", got)
	}
}

func TestRetryRejectsNonReplayableBody(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"retry"}`)),
			Request:    req,
		}, nil
	})
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/v1/chat", io.NopCloser(strings.NewReader("one-shot")))
	if err != nil {
		t.Fatal(err)
	}
	upstream := NewWithRetries(1)
	upstream.baseDelay = 0
	defer upstream.Stop()
	_, uErr := upstream.DoWithHTTPClient(req, client)
	if uErr == nil || !strings.Contains(uErr.Message, "not replayable") {
		t.Fatalf("error = %#v, want non-replayable body error", uErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
