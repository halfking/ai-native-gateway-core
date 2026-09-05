package pool

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNewPool_UsesRequestContextForFullDeadline(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "stream", ProviderID: 1, CredentialID: 1}, "", nil)
	if p.Client().Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %s, want 0", p.Client().Timeout)
	}
	if p.transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("ResponseHeaderTimeout = %s, want a bounded transport header timeout", p.transport.ResponseHeaderTimeout)
	}
	if p.transport.TLSHandshakeTimeout <= 0 || p.transport.ExpectContinueTimeout <= 0 {
		t.Fatalf("transport handshake timeouts must remain bounded: tls=%s expect=%s", p.transport.TLSHandshakeTimeout, p.transport.ExpectContinueTimeout)
	}
}
func TestPoolKeyString(t *testing.T) {
	key := PoolKey{
		IdentityHash: "abcdef1234567890",
		ProviderID:   100,
		CredentialID: 5,
	}
	s := key.String()
	if s != "abcdef1234567890/100/5" {
		t.Fatalf("unexpected key: %q", s)
	}
}

func TestNewPoolDefaultsToActive(t *testing.T) {
	key := PoolKey{IdentityHash: "a", ProviderID: 1, CredentialID: 1}
	p := NewPool(key, "", nil)
	if p.State() != PoolActive {
		t.Fatal("new pool should be active")
	}
}

func TestPoolFailureDegradation(t *testing.T) {
	key := PoolKey{IdentityHash: "b", ProviderID: 2, CredentialID: 2}
	p := NewPool(key, "", nil)

	for i := 0; i < degradedThreshold; i++ {
		p.RecordFailure()
	}
	if p.State() != PoolDegraded {
		t.Fatal("pool should be degraded after 3 failures")
	}

	p.RecordSuccess()
	p.RecordSuccess()
	p.RecordSuccess()
	if p.State() != PoolActive {
		t.Fatal("pool should be active after enough consecutive successes")
	}
}

func TestPoolManagerCreateAndGet(t *testing.T) {
	pm := NewPoolManager()
	key := PoolKey{IdentityHash: "c", ProviderID: 3, CredentialID: 3}

	p1 := pm.GetOrCreate(key, "")
	p2 := pm.GetOrCreate(key, "")
	if p1 != p2 {
		t.Fatal("GetOrCreate should return same instance")
	}
}

func TestPoolManagerStats(t *testing.T) {
	pm := NewPoolManager()
	pm.GetOrCreate(PoolKey{IdentityHash: "x", ProviderID: 1, CredentialID: 1}, "")
	pm.GetOrCreate(PoolKey{IdentityHash: "y", ProviderID: 2, CredentialID: 2}, "")

	stats := pm.Stats()
	if stats["active"] != 2 {
		t.Fatalf("expected 2 active pools, got %d", stats["active"])
	}
}

func TestPoolClose(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "d", ProviderID: 4, CredentialID: 4}, "", nil)
	p.Close()
	// Should not panic on double close
	p.Close()
}

func TestPoolManagerStopPreventsNewPools(t *testing.T) {
	pm := NewPoolManager()
	pm.Stop()
	if p := pm.GetOrCreate(PoolKey{IdentityHash: "stop", ProviderID: 9, CredentialID: 9}, ""); p != nil {
		t.Fatal("GetOrCreate should return nil after Stop")
	}
	// Should stay safe on repeated stop calls.
	pm.Stop()
}

func TestPoolAcquireReleaseRespectsCapacity(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "cap", ProviderID: 7, CredentialID: 7}, "", nil)
	ctx := context.Background()
	for i := 0; i < poolMaxActiveConns; i++ {
		if err := p.Acquire(ctx); err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := p.Acquire(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded when pool is full, got %v", err)
	}
	p.Release()
	if err := p.Acquire(ctx); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
}

func TestPoolAcquireFailsAfterClose(t *testing.T) {
	p := NewPool(PoolKey{IdentityHash: "closed", ProviderID: 8, CredentialID: 8}, "", nil)
	p.Close()
	if err := p.Acquire(context.Background()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("expected ErrPoolClosed, got %v", err)
	}
}

func TestPoolManagerIdentityProviderCredentialIsolation(t *testing.T) {
	pm := NewPoolManager(nil)
	t.Cleanup(func() {
		pm.CloseAll()
		pm.Stop()
	})

	base := PoolKey{IdentityHash: "identity-a", ProviderID: 10, CredentialID: 20}
	keys := []PoolKey{
		base,
		{IdentityHash: "identity-b", ProviderID: base.ProviderID, CredentialID: base.CredentialID},
		{IdentityHash: base.IdentityHash, ProviderID: 11, CredentialID: base.CredentialID},
		{IdentityHash: base.IdentityHash, ProviderID: base.ProviderID, CredentialID: 21},
	}

	seenPools := make(map[*Pool]struct{}, len(keys))
	seenTransports := make(map[*http.Transport]struct{}, len(keys))
	for _, key := range keys {
		p := pm.GetOrCreate(key, "")
		if p == nil {
			t.Fatalf("GetOrCreate(%v) returned nil", key)
		}
		if _, duplicate := seenPools[p]; duplicate {
			t.Fatalf("different key %v reused pool instance", key)
		}
		if _, duplicate := seenTransports[p.transport]; duplicate {
			t.Fatalf("different key %v reused transport instance", key)
		}
		seenPools[p] = struct{}{}
		seenTransports[p.transport] = struct{}{}
	}

	if got := pm.GetOrCreate(base, ""); got != pm.Get(base) {
		t.Fatal("same key did not reuse the existing pool")
	}
}

func TestPoolClientKeepAliveReuseForSameKey(t *testing.T) {
	var mu sync.Mutex
	connections := make(map[string]int)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		mu.Lock()
		connections[conn.RemoteAddr().String()]++
		mu.Unlock()
	}
	srv.Start()
	defer srv.Close()

	p := NewPool(PoolKey{IdentityHash: "reuse", ProviderID: 1, CredentialID: 1}, "", nil)
	defer p.Close()

	for i := 0; i < 2; i++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := p.Client().Do(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			resp.Body.Close()
			t.Fatalf("read response %d: %v", i, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close response %d: %v", i, err)
		}
	}

	mu.Lock()
	connectionCount := len(connections)
	mu.Unlock()
	if connectionCount != 1 {
		t.Fatalf("same-key sequential requests opened %d TCP connections, want 1 keep-alive connection", connectionCount)
	}
}

func TestPoolClientCancelReleasesAcquireSlot(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := NewPool(PoolKey{IdentityHash: "cancel", ProviderID: 2, CredentialID: 3}, "", nil)
	p.activeConns = make(chan struct{}, 1)
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.Client().Do(req)
		p.Release()
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("client error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled pool request did not return")
	}

	reacquireCtx, reacquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer reacquireCancel()
	if err := p.Acquire(reacquireCtx); err != nil {
		t.Fatalf("slot was not released after client cancellation: %v", err)
	}
	p.Release()
}
