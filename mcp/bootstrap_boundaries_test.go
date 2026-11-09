package mcp

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapClaimRequiresOriginalToken(t *testing.T) {
	g, _ := newTestGateway(t, nil)
	svc := NewBootstrapService(g, BootstrapConfig{AutoApprove: true})
	result, status := svc.Apply(context.Background(), ApplyRequest{}, "127.0.0.1")
	if status != http.StatusOK {
		t.Fatalf("apply status=%d", status)
	}
	if _, status := svc.Claim(context.Background(), result.RequestID, "wrong-token", "127.0.0.1"); status != http.StatusForbidden {
		t.Fatalf("wrong token status=%d, want 403", status)
	}
	if _, status := svc.Claim(context.Background(), result.RequestID, result.ClaimToken, "127.0.0.1"); status != http.StatusOK {
		t.Fatalf("original token status=%d, want 200", status)
	}
}

type claimReadBarrierStore struct {
	Store
	readers atomic.Int32
	release chan struct{}
}

func (s *claimReadBarrierStore) GetBootstrap(ctx context.Context, kind, token string) (*BootstrapRecord, error) {
	record, err := s.Store.GetBootstrap(ctx, kind, token)
	if err == nil && record != nil && record.Plaintext != "" {
		if s.readers.Add(1) == 24 {
			close(s.release)
		}
		select {
		case <-s.release:
		case <-time.After(5 * time.Second):
			return nil, context.DeadlineExceeded
		}
	}
	return record, err
}

func TestBootstrapClaimIsAtomicAcrossServices(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			store := NewMemoryStore()
			if kind == "file" {
				var err error
				store, err = NewFileStore(filepath.Join(t.TempDir(), "store.json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			// A regressed Get+Put claim must let all readers capture the same
			// plaintext before any write; an atomic Store claim bypasses this seam.
			barrier := &claimReadBarrierStore{Store: store, release: make(chan struct{})}
			g, _ := newTestGateway(t, nil, func(c *Config) { c.Store = barrier })
			cfg := BootstrapConfig{AutoApprove: true, ReadIPLimit: 1000}
			svc := NewBootstrapService(g, cfg)
			result, status := svc.Apply(context.Background(), ApplyRequest{}, "127.0.0.1")
			if status != http.StatusOK {
				t.Fatalf("apply status=%d", status)
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range 24 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					other := NewBootstrapService(g, cfg)
					_, status := other.Claim(context.Background(), result.RequestID, result.ClaimToken, "127.0.0.1")
					if status == http.StatusOK {
						successes.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatalf("successful claims=%d, want exactly 1", successes.Load())
			}
		})
	}
}

func TestBootstrapApprovalCannotBypassDelegation(t *testing.T) {
	for _, tc := range []struct {
		name, tenant string
		scopes       []string
		restricted   bool
	}{
		{"foreign", "tenant-b", []string{ScopeManage, ScopeRead, ScopeExecute}, false},
		{"missing_execute", "tenant-a", []string{ScopeManage}, false},
		{"restricted", "tenant-a", []string{ScopeManage, ScopeRead, ScopeExecute}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, srv := newTestGateway(t, nil, func(c *Config) { c.Bootstrap = BootstrapConfig{Enabled: true, TenantID: "tenant-a"} })
			request, status := g.bootstrap.Apply(context.Background(), ApplyRequest{}, "127.0.0.1")
			if status != http.StatusOK {
				t.Fatalf("apply status=%d", status)
			}
			spec := KeySpec{TenantID: tc.tenant, Scopes: tc.scopes}
			if tc.restricted {
				spec.AllowedTools = []string{"memora__read"}
			}
			secret := issueKey(t, g, spec)
			status, _, _ = keyReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/requests/"+request.RequestID+"/approve", secret, "")
			if status != http.StatusForbidden {
				t.Fatalf("restricted approver status=%d want 403", status)
			}
			status, _, _ = adminReq(t, srv.URL, http.MethodPost, "/mcp/control/keys/requests/"+request.RequestID+"/approve", "")
			if status != http.StatusOK {
				t.Fatalf("admin approve status=%d want 200", status)
			}
		})
	}
}
