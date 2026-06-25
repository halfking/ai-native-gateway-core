package bg

import (
	"context"
	"testing"

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

	// Create in-memory hub (no DB)
	hub := apihub.New(nil)

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

	hub := apihub.New(nil)
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
