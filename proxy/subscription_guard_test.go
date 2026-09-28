package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sequenceSubscriptionResolver struct {
	mu        sync.Mutex
	responses [][]netip.Addr
	calls     int
}

func (r *sequenceSubscriptionResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.calls
	r.calls++
	if index >= len(r.responses) {
		index = len(r.responses) - 1
	}
	return r.responses[index], nil
}

func (r *sequenceSubscriptionResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func publicTestResolver() *sequenceSubscriptionResolver {
	return &sequenceSubscriptionResolver{responses: [][]netip.Addr{{netip.MustParseAddr("8.8.8.8")}}}
}

func TestSubscriptionGuardRejectsPrivateTargetsBeforeDial(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "false")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("http://public.example:8080"))
	}))
	defer srv.Close()

	p := NewMultiFormatParserWithClient(&http.Client{Timeout: time.Second})
	_, err := p.fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "prohibited address") {
		t.Fatalf("private literal error = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("private endpoint was contacted %d times", hits.Load())
	}

	p.resolver = &sequenceSubscriptionResolver{responses: [][]netip.Addr{{
		netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254"),
	}}}
	p.dialContext = func(context.Context, string, string) (net.Conn, error) {
		t.Error("mixed public/private DNS answer reached dial")
		return nil, fmt.Errorf("unexpected dial")
	}
	_, err = p.fetch(context.Background(), "http://public.test/sub")
	if err == nil || !strings.Contains(err.Error(), "prohibited address") {
		t.Fatalf("mixed DNS answer error = %v", err)
	}
}

func TestSubscriptionGuardAllowsExplicitPrivateHTTPS(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "true")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("expected HTTPS")
		}
		_, _ = w.Write([]byte("http://public.example:8080"))
	}))
	defer srv.Close()

	p := NewMultiFormatParserWithClient(srv.Client())
	if !p.allowPrivate {
		t.Fatal("explicit private-subscription opt-in was not loaded")
	}
	body, err := p.fetch(context.Background(), srv.URL)
	if err != nil || string(body) != "http://public.example:8080" {
		t.Fatalf("explicit private HTTPS fetch body=%q err=%v", body, err)
	}
}

func TestSubscriptionGuardChecksEveryRedirect(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "false")
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		privateHits.Add(1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer private.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.test" {
			t.Errorf("Host = %q, want public.test", r.Host)
		}
		http.Redirect(w, r, private.URL+"/metadata", http.StatusFound)
	}))
	defer public.Close()

	p := NewMultiFormatParserWithClient(&http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}})
	p.resolver = publicTestResolver()
	p.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "8.8.8.8:80" {
			t.Errorf("dialed %q, want pinned public IP", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, public.Listener.Addr().String())
	}
	_, err := p.fetch(context.Background(), "http://public.test/start")
	if err == nil || !strings.Contains(err.Error(), "prohibited address") {
		t.Fatalf("private redirect error = %v", err)
	}
	if privateHits.Load() != 0 {
		t.Fatalf("redirect contacted private endpoint %d times", privateHits.Load())
	}
}

func TestSubscriptionGuardPinsResolvedAddressAgainstRebinding(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "false")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.test" {
			t.Errorf("Host = %q", r.Host)
		}
		_, _ = w.Write([]byte("http://node.example:8080"))
	}))
	defer srv.Close()
	resolver := &sequenceSubscriptionResolver{responses: [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	p := NewMultiFormatParserWithClient(&http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}})
	p.resolver = resolver
	p.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "8.8.8.8:80" {
			t.Errorf("dialed %q after DNS changed", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	body, err := p.fetch(context.Background(), "http://public.test/sub")
	if err != nil || string(body) != "http://node.example:8080" {
		t.Fatalf("pinned fetch body=%q err=%v", body, err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("DNS was resolved %d times, want one pinned resolution", resolver.callCount())
	}
}

func TestSubscriptionGuardPinsProxyConnectTarget(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "false")
	connectHost := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectHost <- r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := NewMultiFormatParserWithClient(&http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	})
	p.resolver = publicTestResolver()
	_, _ = p.fetch(context.Background(), "https://public.test/sub")
	select {
	case host := <-connectHost:
		if host != "8.8.8.8:443" {
			t.Fatalf("CONNECT target = %q, want pinned 8.8.8.8:443", host)
		}
	default:
		t.Fatal("proxy never received CONNECT")
	}
}

func TestSubscriptionGuardHTTPSProxyUsesProxyTLSNameAndPinnedConnect(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROXY_SUBSCRIPTION_ALLOW_PRIVATE", "false")
	connectHost := make(chan string, 1)
	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectHost <- r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := proxyServer.Client().Transport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(proxyURL)
	p := NewMultiFormatParserWithClient(&http.Client{Timeout: time.Second, Transport: base})
	p.resolver = publicTestResolver()
	_, _ = p.fetch(context.Background(), "https://public.test/sub")
	select {
	case host := <-connectHost:
		if host != "8.8.8.8:443" {
			t.Fatalf("HTTPS proxy CONNECT target = %q, want pinned IP", host)
		}
	default:
		t.Fatal("TLS proxy was not reached; proxy certificate name may be wrong")
	}
}

func TestPublicSubscriptionIPClassification(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "198.18.0.1", "0.0.0.0", "::1", "fd00::1",
		"fe80::1", "::ffff:127.0.0.1", "2001:db8::1", "2002:c0a8:0101::1",
	} {
		if publicSubscriptionIP(netip.MustParseAddr(raw)) {
			t.Errorf("%s classified as public", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicSubscriptionIP(netip.MustParseAddr(raw)) {
			t.Errorf("%s classified as private", raw)
		}
	}
}
