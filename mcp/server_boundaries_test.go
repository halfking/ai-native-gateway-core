package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDisabledRegistrationCannotBeCalledDirectly(t *testing.T) {
	for _, target := range []string{"adapter", "tool"} {
		t.Run(target, func(t *testing.T) {
			up := newFakeUpstream(t, nil)
			g, srv := newTestGateway(t, up)
			secret := issueKey(t, g, KeySpec{TenantID: "default"})
			if target == "adapter" {
				a, _ := g.store.GetAdapter(context.Background(), "default", "memora")
				a.Enabled = false
				if err := g.store.PutAdapter(context.Background(), *a); err != nil {
					t.Fatal(err)
				}
			} else if err := g.store.PutTool(context.Background(), Tool{Name: "memora__read", RawName: "read", Adapter: "memora", TenantID: "default", Visibility: VisibilityPublic, Enabled: false}); err != nil {
				t.Fatal(err)
			}
			status, _ := rpcCall(t, srv, "/mcp", secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "memora__read"}})
			if status != http.StatusForbidden {
				t.Fatalf("disabled %s call status=%d want403", target, status)
			}
			if up.lastBody != nil {
				t.Fatal("disabled registration reached upstream")
			}
		})
	}
}

func TestToolCacheDoesNotCrossTenantNamesakes(t *testing.T) {
	upA := newFakeUpstream(t, []map[string]any{{"name": "own-only"}})
	upB := newFakeUpstream(t, []map[string]any{{"name": "foreign-only"}})
	g, srv := newTestGateway(t, nil)
	for _, tc := range []struct{ tenant, endpoint string }{{"tenant-a", upA.url()}, {"tenant-b", upB.url()}} {
		if err := g.store.PutAdapter(context.Background(), Adapter{Name: "same", TenantID: tc.tenant, Endpoint: tc.endpoint, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		secret := issueKey(t, g, KeySpec{TenantID: tc.tenant})
		status, body := rpcCall(t, srv, "/mcp", secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		if status != http.StatusOK {
			t.Fatalf("list status=%d", status)
		}
		tools, _ := rpcResult(t, body)["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tenant %s tools=%v", tc.tenant, tools)
		}
		name := tools[0].(map[string]any)["name"]
		want := "same__own-only"
		if tc.tenant == "tenant-b" {
			want = "same__foreign-only"
		}
		if name != want {
			t.Fatalf("tenant %s got %s want %s", tc.tenant, name, want)
		}
	}
}

func TestAnalyzeCannotReadForeignStatsOrTopology(t *testing.T) {
	g, srv := newTestGateway(t, nil)
	secret := scopeKey(t, g, "tenant-a", ScopeAnalyze)
	if err := g.store.PutAdapter(context.Background(), Adapter{Name: "private-upstream", TenantID: "tenant-a", Endpoint: "http://127.0.0.1:9187"}); err != nil {
		t.Fatal(err)
	}
	g.audit.Record(AuditEvent{TenantID: "tenant-b", Tool: "foreign-secret-marker"})
	toolCallsTotal.WithLabelValues("foreign-secret-marker", "ok").Inc()
	for _, path := range []string{"/mcp/control/stats", "/mcp/control/routes"} {
		status, _, raw := keyReq(t, srv.URL, http.MethodGet, path, secret, "")
		if status != http.StatusOK || strings.Contains(raw, "foreign-secret-marker") || strings.Contains(raw, "127.0.0.1:9187") {
			t.Fatalf("analyze leaked foreign data or endpoint at %s status=%d", path, status)
		}
	}
	// Explicit tenant selection remains available to the management token.
	status, _, raw := keyReqWithHeader(t, srv.URL, http.MethodGet, "/mcp/control/routes", "admin-token", "X-Tenant-Id", "tenant-a", "")
	if status != http.StatusOK || !strings.Contains(raw, "127.0.0.1:9187") {
		t.Fatalf("admin topology unavailable: %s", raw)
	}
}

type usageBarrierStore struct {
	Store
	entered chan struct{}
	release chan struct{}
}

func (s *usageBarrierStore) PutKey(ctx context.Context, k Key) error {
	close(s.entered)
	<-s.release
	return s.Store.PutKey(ctx, k)
}
func (s *usageBarrierStore) TouchKey(ctx context.Context, tenant, id string, at time.Time) error {
	close(s.entered)
	<-s.release
	return s.Store.(interface {
		TouchKey(context.Context, string, string, time.Time) error
	}).TouchKey(ctx, tenant, id, at)
}

func TestUsageTouchCannotResurrectRevokedCredential(t *testing.T) {
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
			g, _ := newTestGateway(t, nil, func(c *Config) { c.Store = store })
			key, _, err := g.IssueKey(context.Background(), KeySpec{TenantID: "default"})
			if err != nil {
				t.Fatal(err)
			}
			barrier := &usageBarrierStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			g.store = barrier
			done := make(chan struct{})
			go func() { g.touchKey(context.Background(), &Identity{TenantID: "default", KeyID: key.ID}); close(done) }()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("usage update did not start")
			}
			key.Status = StatusRevoked
			if err := store.PutKey(context.Background(), *key); err != nil {
				t.Fatal(err)
			}
			close(barrier.release)
			<-done
			current, err := store.GetKey(context.Background(), "default", key.ID)
			if err != nil || current.Status != StatusRevoked || current.LastUsedAt == nil {
				t.Fatalf("usage update lost revocation or last-used timestamp: err=%v status=%s", err, current.Status)
			}
		})
	}
}

func TestApprovalGrantIsBoundToTenantAndRequester(t *testing.T) {
	for _, tc := range []struct{ name, tenant, requester string }{
		{"foreign_tenant", "tenant-b", ""},
		{"missing_requester_identity", "tenant-a", "user-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newFakeUpstream(t, nil)
			g, srv := newTestGateway(t, up)
			secret := issueKey(t, g, KeySpec{TenantID: "tenant-a"})
			ctx := context.Background()
			if err := g.store.PutTool(ctx, Tool{Name: "memora__protected", RawName: "protected", Adapter: "memora", TenantID: "tenant-a", Sensitivity: SensitivityHigh, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if err := g.store.PutApproval(ctx, Approval{ID: "grant", TenantID: tc.tenant, Requester: tc.requester, Tool: "memora__protected", Status: ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "memora__protected"}})
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytesReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("X-MCP-Approval", "grant")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("foreign grant status=%d want403", resp.StatusCode)
			}
		})
	}
}
