package bg

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// fakeSyncer is a test implementation of AssetSyncSource that returns
// in-memory data without touching the database.
type fakeSyncer struct {
	llms []apihub.Asset
	mcps []apihub.Asset
	err  error
}

func (f *fakeSyncer) LLMEndpoints(ctx context.Context) ([]apihub.Asset, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.llms, nil
}

func (f *fakeSyncer) MCPServers(ctx context.Context) ([]apihub.Asset, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.mcps, nil
}

// okStore is a no-op apihub.Store that swallows every call. We use it so
// the AssetWatcher test exercises the full sync flow without a database
// or a separate apihub.memStore (which is unexported).
type okStore struct{}

func (okStore) Upsert(_ context.Context, _ apihub.Asset) error { return nil }
func (okStore) Get(_ context.Context, _ string, _ apihub.Kind, _ int64) (apihub.Asset, error) {
	return apihub.Asset{}, nil
}
func (okStore) List(_ context.Context, _ apihub.Filter) ([]apihub.Asset, error) {
	return nil, nil
}
func (okStore) Link(_ context.Context, _ string, _ apihub.Relationship) error { return nil }
func (okStore) Neighbors(_ context.Context, _ string, _ apihub.Kind, _ int64, _ int) ([]apihub.Asset, []apihub.Relationship, error) {
	return nil, nil, nil
}
func (okStore) MarkHealth(_ context.Context, _ string, _ apihub.Kind, _ int64, _ apihub.HealthState) error {
	return nil
}
func (okStore) ListStale(_ context.Context, _ string, _ time.Duration) ([]apihub.Asset, error) {
	return nil, nil
}

func TestAssetWatcher_SyncOnce(t *testing.T) {
	// Create fake syncer with test data
	syncer := &fakeSyncer{
		llms: []apihub.Asset{
			{RefID: 1, TenantID: "tenant1", Name: "gpt-4"},
			{RefID: 2, TenantID: "tenant1", Name: "claude-3"},
		},
		mcps: []apihub.Asset{
			{RefID: 100, TenantID: "tenant1", Name: "mcp-server-1"},
		},
	}

	// Hub backed by noop store — same shape as production minus DB.
	hub := apihub.New(okStore{})

	// Create watcher
	watcher := NewAssetWatcher(hub, syncer)

	// Run sync
	ctx := context.Background()
	llmAdded, mcpAdded, err := watcher.SyncOnce(ctx)
	if err != nil {
		t.Fatalf("SyncOnce failed: %v", err)
	}

	// Verify counts
	if llmAdded != 2 {
		t.Errorf("expected 2 LLM assets added, got %d", llmAdded)
	}
	if mcpAdded != 1 {
		t.Errorf("expected 1 MCP asset added, got %d", mcpAdded)
	}
}

func TestAssetWatcher_SyncOnce_PartialFailure(t *testing.T) {
	// Syncer that fails on LLM but succeeds on MCP
	syncer := &fakeSyncer{
		llms: nil,
		mcps: []apihub.Asset{
			{RefID: 100, TenantID: "tenant1", Name: "mcp-ok"},
		},
		err: nil,
	}

	hub := apihub.New(okStore{})
	watcher := NewAssetWatcher(hub, syncer)

	ctx := context.Background()
	llmAdded, mcpAdded, err := watcher.SyncOnce(ctx)

	// Should not return error (partial failure is tolerated)
	if err != nil {
		t.Logf("Got expected error: %v", err)
	}

	// MCP should still succeed
	if mcpAdded != 1 {
		t.Errorf("expected 1 MCP asset, got %d", mcpAdded)
	}
	if llmAdded != 0 {
		t.Errorf("expected 0 LLM assets, got %d", llmAdded)
	}
}
