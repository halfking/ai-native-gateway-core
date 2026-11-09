package mcp

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestTenantDeletePreservesSharedNamesake(t *testing.T) {
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
			g, srv := newTestGateway(t, nil, func(c *Config) { c.Store = store })
			secret := scopeKey(t, g, "tenant-a", ScopeManage)
			ctx := context.Background()
			for _, tenant := range []string{"", "tenant-a"} {
				if err := store.PutAdapter(ctx, Adapter{Name: "namesake", TenantID: tenant}); err != nil {
					t.Fatal(err)
				}
				if err := store.PutTool(ctx, Tool{Name: "namesake", Adapter: "namesake", TenantID: tenant}); err != nil {
					t.Fatal(err)
				}
			}
			for _, resource := range []string{"adapters", "tools"} {
				status, _, _ := keyReq(t, srv.URL, http.MethodDelete, "/mcp/control/"+resource+"/namesake", secret, "")
				if status != http.StatusNoContent {
					t.Fatalf("delete %s status=%d", resource, status)
				}
			}
			if a, _ := store.GetAdapter(ctx, "", "namesake"); a == nil {
				t.Error("shared adapter deleted with tenant namesake")
			}
			if tool, _ := store.GetTool(ctx, "", "namesake"); tool == nil {
				t.Error("shared tool deleted with tenant namesake")
			}
		})
	}
}
