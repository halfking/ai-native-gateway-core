package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R28-P-1: the shared client must dispatch egress-profiled providers through
// the EgressTransportProvider before dialing — custom transport on proxy
// profile, default path on (nil, nil), and a hard error when the provider
// reports no healthy egress.

type fakeEgressProvider struct {
	rt     http.RoundTripper
	err    error
	called int
}

func (f *fakeEgressProvider) TransportFor(context.Context, int) (http.RoundTripper, error) {
	f.called++
	return f.rt, f.err
}

type cannedRoundTripper struct {
	calls int
}

func (c *cannedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	return rec.Result(), nil
}

func TestEgressMetaRoundTrip(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if _, ok := EgressMetaFrom(req.Context()); ok {
		t.Fatal("unstamped request must not carry egress meta")
	}
	req = WithEgressMeta(req, 42)
	meta, ok := EgressMetaFrom(req.Context())
	if !ok || meta.ProviderID != 42 {
		t.Fatalf("meta = %+v ok=%v", meta, ok)
	}
	// ProviderID <= 0 is a no-op (never route on an unknown provider).
	req2, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req2 = WithEgressMeta(req2, 0)
	if _, ok := EgressMetaFrom(req2.Context()); ok {
		t.Fatal("provider 0 must not stamp egress meta")
	}
}

func TestClientDoDispatchesEgressTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewWithRetries(0)
	rt := &cannedRoundTripper{}
	provider := &fakeEgressProvider{rt: rt}
	c.SetEgressProvider(provider)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req = WithEgressMeta(req, 42)
	resp, uErr := c.Do(req)
	if uErr != nil {
		t.Fatalf("egress dispatch failed: %v", uErr)
	}
	resp.Body.Close()
	if rt.calls != 1 || provider.called != 1 {
		t.Fatalf("request must ride the egress transport: rt=%d provider=%d", rt.calls, provider.called)
	}

	// Same dispatch on the identity-pool path (DoWithHTTPClient).
	poolCalls := 0
	poolRT := &cannedRoundTripper{}
	provider.rt = poolRT
	_, uErr = c.DoWithHTTPClient(req, &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		poolCalls++
		rec := httptest.NewRecorder()
		return rec.Result(), nil
	})})
	if uErr != nil {
		t.Fatalf("pool-path egress dispatch failed: %v", uErr)
	}
	if provider.called != 2 || poolCalls != 0 {
		t.Fatalf("egress must win over the pool transport: provider=%d poolDirect=%d", provider.called, poolCalls)
	}
	_ = poolRT
}

func TestClientDoEgressProviderNilKeepsDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewWithRetries(0)
	c.SetEgressProvider(&fakeEgressProvider{rt: nil, err: nil})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req = WithEgressMeta(req, 42)
	resp, uErr := c.Do(req)
	if uErr != nil {
		t.Fatalf("nil egress transport must keep the default path: %v", uErr)
	}
	resp.Body.Close()

	// No meta stamped → provider never consulted.
	plain, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	p2 := &fakeEgressProvider{}
	c.SetEgressProvider(p2)
	resp, uErr = c.Do(plain)
	if uErr != nil {
		t.Fatal(uErr)
	}
	resp.Body.Close()
	if p2.called != 0 {
		t.Fatal("unstamped request must bypass the egress provider")
	}
}

func TestClientDoEgressErrorFailsClosed(t *testing.T) {
	c := NewWithRetries(0)
	c.SetEgressProvider(&fakeEgressProvider{err: errors.New("no healthy node")})
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com", nil)
	req = WithEgressMeta(req, 42)
	_, uErr := c.Do(req)
	if uErr == nil || uErr.Kind != KindUpstreamDown {
		t.Fatalf("proxy-profile provider without a node must fail closed: %+v", uErr)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
