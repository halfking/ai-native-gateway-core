package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bytewiseReply struct{ io.Reader }

func (b bytewiseReply) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return b.Reader.Read(p)
}

func TestDoRejectsFragmentedHTML(t *testing.T) {
	for _, body := range []string{"<html>challenge</html>", "\n <html>challenge</html>", strings.Repeat(" ", 64) + "<html>challenge</html>", " \t\n"} {
		t.Run(body, func(t *testing.T) {
			c := NewWithRetries(0)
			defer c.Stop()
			req, _ := http.NewRequest(http.MethodPost, "https://relay.example/v1/chat/completions", nil)
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}},
					Body: io.NopCloser(bytewiseReply{strings.NewReader(body)})}, nil
			})}
			_, err := c.DoWithHTTPClient(req, client)
			if err == nil || err.StatusCode != 502 {
				t.Fatalf("fragmented HTML accepted: %v", err)
			}
		})
	}
}

func TestDoRejectsNonAPIReplies(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"html", "text/html; charset=utf-8", "<html><title>Sign in</title></html>"},
		{"disguised html", "text/plain", "<!DOCTYPE html><html>challenge</html>"},
		{"empty", "application/json", ""},
		{"fake json", "application/json", "upstream unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL, strings.NewReader(`{}`))
			resp, err := NewWithRetries(2).Do(req)
			if err == nil || err.StatusCode != http.StatusBadGateway || err.Kind != KindUpstreamDown {
				t.Fatalf("response/error = %v/%v, want retryable 502 upstream_down", resp, err)
			}
			if hits.Load() != 1 {
				t.Fatalf("hits=%d: a non-API reply must yield to candidate recovery immediately", hits.Load())
			}
			if resp == nil || resp.StatusCode != http.StatusBadGateway || !strings.Contains(err.Message, "not an API reply") {
				t.Fatalf("non-API failure lost its diagnostic/status: %v / %v", resp, err)
			}
		})
	}
}

func TestDoDoesNotWaitForSSEBody(t *testing.T) {
	for _, ct := range []string{"text/event-stream", "", "application/json", "audio/mpeg", "application/x-ndjson", "application/ndjson"} {
		t.Run(ct, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer s.Close()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, strings.NewReader(`{}`))
			if ct != "audio/mpeg" {
				req.Header.Set("Accept", "text/event-stream")
			}
			if strings.Contains(ct, "ndjson") {
				req.Header.Set("Accept", "application/x-ndjson")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				resp, err := NewWithRetries(0).Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				_ = resp.Body.Close()
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				cancel()
				t.Fatal("client waited for SSE bytes before the stream reader could arm its deadline")
			}
		})
	}
}

func TestDoPreservesAPIReplyBytes(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"json", "application/json", ` {"n":1.50e2,"html":"<html>"}`, 200},
		{"sse", "text/event-stream", ": keepalive\n\ndata: {\"choices\":[]}\n\n", 200},
		{"binary", "audio/mpeg", "\x00\xff\x01", 200},
		{"xml", "application/xml", "<html>legitimate XML</html>", 200},
		{"no content", "", "", 204},
		{"client error", "text/html", "<html>bad request</html>", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			req, _ := http.NewRequest(http.MethodPost, s.URL, strings.NewReader(`{}`))
			resp, err := NewWithRetries(0).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, readErr := io.ReadAll(resp.Body)
			if readErr != nil || string(got) != tc.body || resp.StatusCode != tc.status {
				t.Fatalf("body/status=%q/%d (%v), want %q/%d", got, resp.StatusCode, readErr, tc.body, tc.status)
			}
		})
	}
}
