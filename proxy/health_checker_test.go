package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPHealthCheckerStatusAndHeaders(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("User-Agent"); got != "llm-gateway-go/health-check" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control = %q", got)
		}
		w.Header().Set("Location", "http://127.0.0.1:9/redirect")
		w.WriteHeader(http.StatusFound)
	}))
	defer proxy.Close()
	parsed, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	checker := NewHTTPHealthChecker(time.Second)
	node := &Node{ID: 1, Name: "local", Protocol: ProtocolHTTP, Server: parsed.Hostname(), Port: port, HealthCheckURL: "http://upstream.invalid/health"}
	latency, err := checker.Check(context.Background(), node)
	if err != nil || latency < 1 {
		t.Fatalf("Check = latency %d, err %v; want successful 3xx check", latency, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("proxy requests = %d, want 1", requests.Load())
	}
}

func TestHTTPHealthCheckerRejectsNilAndUndialable(t *testing.T) {
	checker := NewHTTPHealthChecker(time.Second)
	if _, err := checker.Check(context.Background(), nil); err == nil {
		t.Fatal("nil node should fail")
	}
	if _, err := checker.Check(context.Background(), &Node{Protocol: "vless", Server: "node", Port: 443}); err == nil {
		t.Fatal("undialable node should fail")
	}
}

func TestHTTPHealthCheckerTransportPoolLifecycle(t *testing.T) {
	checker := NewHTTPHealthChecker(time.Second)
	first, err := checker.getOrCreateTransport("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	same, err := checker.getOrCreateTransport("http://127.0.0.1:8080")
	if err != nil || same != first {
		t.Fatalf("same proxy URL did not reuse transport: same=%v err=%v", same == first, err)
	}
	if _, err := checker.getOrCreateTransport("http://127.0.0.1:8081"); err != nil {
		t.Fatal(err)
	}
	checker.Close()
	fresh, err := checker.getOrCreateTransport("http://127.0.0.1:8080")
	if err != nil || fresh == first {
		t.Fatalf("Close did not clear transport pool: fresh=%v err=%v", fresh != first, err)
	}
}

func TestHTTPHealthCheckerCheckConcurrentNilContextAndEmpty(t *testing.T) {
	checker := NewHTTPHealthChecker(100 * time.Millisecond)
	out := checker.CheckConcurrent(nil, []*Node{{ID: 1, Protocol: "trojan", Server: "node", Port: 443}}, 1)
	var count int
	for result := range out {
		count++
		if result.OK || result.Err == nil {
			t.Fatalf("undialable result = %+v", result)
		}
	}
	if count != 1 {
		t.Fatalf("result count = %d, want 1", count)
	}
	closed := checker.CheckConcurrent(context.Background(), nil, 0)
	if _, ok := <-closed; ok {
		t.Fatal("empty result channel should be closed")
	}
}

func TestHTTPHealthCheckerTimeout(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	checker := NewHTTPHealthChecker(20 * time.Millisecond)
	node := &Node{ID: 1, Name: "slow", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8080, HealthCheckURL: target.URL}
	_, err := checker.Check(context.Background(), node)
	if err == nil {
		t.Fatal("slow health check should time out")
	}
}
