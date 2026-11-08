package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	adapter := Adapter{Name: "memora", Endpoint: "http://x", TenantID: "t1", Enabled: true, CreatedAt: time.Now()}
	if err := store.PutAdapter(ctx, adapter); err != nil {
		t.Fatalf("put adapter: %v", err)
	}
	got, err := store.GetAdapter(ctx, "t1", "memora")
	if err != nil || got == nil || got.Endpoint != "http://x" {
		t.Fatalf("get adapter: %v %+v", err, got)
	}
	// 其它租户看不到私有适配器，但能看到共享的。
	if other, _ := store.GetAdapter(ctx, "t2", "memora"); other != nil {
		t.Fatalf("tenant t2 should not see tenant t1 adapter")
	}
	if err := store.PutAdapter(ctx, Adapter{Name: "shared", Endpoint: "http://s", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("put shared adapter: %v", err)
	}
	if other, _ := store.GetAdapter(ctx, "t2", "shared"); other == nil {
		t.Fatalf("tenant t2 should see shared adapter")
	}
	list, err := store.ListAdapters(ctx, "t2")
	if err != nil || len(list) != 1 || list[0].Name != "shared" {
		t.Fatalf("list adapters: %v %+v", err, list)
	}
	if err := store.DeleteAdapter(ctx, "t1", "memora"); err != nil {
		t.Fatalf("delete adapter: %v", err)
	}
	if got, _ := store.GetAdapter(ctx, "t1", "memora"); got != nil {
		t.Fatalf("adapter should be deleted")
	}
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "mcp-store.json")

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if err := store.PutAdapter(ctx, Adapter{Name: "memora", Endpoint: "http://x", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("put adapter: %v", err)
	}
	if err := store.PutTool(ctx, Tool{Name: "memora__a", Adapter: "memora", Visibility: VisibilityPublic, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not written: %v", err)
	}

	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload file store: %v", err)
	}
	adapter, err := reloaded.GetAdapter(ctx, "", "memora")
	if err != nil || adapter == nil {
		t.Fatalf("adapter lost after reload: %v %+v", err, adapter)
	}
	tool, err := reloaded.GetTool(ctx, "", "memora__a")
	if err != nil || tool == nil {
		t.Fatalf("tool lost after reload: %v %+v", err, tool)
	}
}

func TestFileStoreAtomicWriteLeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-store.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := store.PutAdapter(ctx, Adapter{Name: "a", Endpoint: "http://x", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "mcp-store.json" {
			t.Fatalf("unexpected leftover file: %s", e.Name())
		}
	}
}
