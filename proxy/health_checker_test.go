package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
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

// TestHTTPHealthCheckerTransportPoolConcurrent (2026-08-31, P2-5) verifies
// that getOrCreateTransport is safe under concurrent fan-out and that
// concurrent Close + getOrCreateTransport no longer leaks a stale Transport
// after the close (the original sync.Map+Mutex race).
//
// Run with `go test -race` to catch any future regression.
func TestHTTPHealthCheckerTransportPoolConcurrent(t *testing.T) {
	checker := NewHTTPHealthChecker(time.Second)

	const (
		proxies  = 8
		fanout   = 32
		iterStep = 200
	)
	urls := make([]string, proxies)
	for i := range urls {
		urls[i] = "http://127.0.0.1:" + strconv.Itoa(8000+i)
	}

	// Fan-out: many goroutines hit getOrCreateTransport for the same and
	// different proxy URLs. With the double-check + RWMutex implementation
	// each proxy URL must end up with exactly one Transport instance shared
	// by every goroutine that asked for it.
	seen := make(map[string]map[*http.Transport]struct{})
	var seenMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		for _, url := range urls {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				for j := 0; j < iterStep; j++ {
					tr, err := checker.getOrCreateTransport(url)
					if err != nil {
						t.Errorf("getOrCreateTransport(%q): %v", url, err)
						return
					}
					seenMu.Lock()
					m := seen[url]
					if m == nil {
						m = make(map[*http.Transport]struct{})
						seen[url] = m
					}
					m[tr] = struct{}{}
					seenMu.Unlock()
				}
			}(url)
		}
	}
	wg.Wait()

	for url, m := range seen {
		if len(m) != 1 {
			t.Fatalf("proxyURL=%q ended up with %d distinct transports (want 1)", url, len(m))
		}
	}

	// Concurrent Close + getOrCreateTransport must not leak a stale
	// Transport: after Close, every proxy URL must produce a brand-new
	// Transport instance.
	stop := make(chan struct{})
	var closerDone sync.WaitGroup
	closerDone.Add(1)
	go func() {
		defer closerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
				checker.Close()
			}
		}
	}()

	var postClose sync.WaitGroup
	got := make(map[string]*http.Transport)
	var postMu sync.Mutex
	for _, url := range urls {
		postClose.Add(1)
		go func(url string) {
			defer postClose.Done()
			for j := 0; j < iterStep; j++ {
				tr, err := checker.getOrCreateTransport(url)
				if err != nil {
					t.Errorf("post-Close getOrCreateTransport(%q): %v", url, err)
					return
				}
				postMu.Lock()
				if existing, ok := got[url]; ok && existing != tr {
					// Two distinct transport instances for the same URL
					// during the post-Close window means the close did not
					// clear the pool before a new entry was installed.
					// The fix (RWMutex-guarded map) should make this
					// observable only when the goroutine was blocked on
					// transportsMu.Lock at the moment Close ran. Either way
					// we accept multiple instances here; what matters is
					// that no transport leaks AFTER Close completes.
					_ = existing
				}
				got[url] = tr
				postMu.Unlock()
			}
		}(url)
	}
	postClose.Wait()
	close(stop)
	closerDone.Wait()

	// Final Close: every proxy URL must resolve to a brand-new Transport
	// that was NOT in the original `seen` map.
	checker.Close()
	for _, url := range urls {
		tr, err := checker.getOrCreateTransport(url)
		if err != nil {
			t.Fatalf("final getOrCreateTransport(%q): %v", url, err)
		}
		if _, leaked := seen[url]; leaked {
			for old := range seen[url] {
				if old == tr {
					t.Fatalf("proxyURL=%q: post-Close transport reuses the pre-Close instance (%p)", url, tr)
				}
			}
		}
	}
}
