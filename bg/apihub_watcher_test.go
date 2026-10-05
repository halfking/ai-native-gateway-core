package bg

import (
	"context"
	"errors"
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
func (okStore) UpsertBatch(_ context.Context, assets []apihub.Asset) (int, error) {
	return len(assets), nil
}
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
func (okStore) ListTenants(_ context.Context) ([]string, error) {
	return []string{"default"}, nil
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

// countingStore 可注入 UpsertBatch 的返回值（确认落库行数 + 错误），
// 用于钉住 R48-E1 的计数契约：SyncOnce 的 llmAdded 必须是 store 确认的
// 行数，而不是源表条数；部分失败时计数与错误**并存**。
type countingStore struct {
	okStore
	batchN   int
	batchErr error
}

func (s countingStore) UpsertBatch(_ context.Context, assets []apihub.Asset) (int, error) {
	if s.batchN == 0 && s.batchErr == nil {
		return len(assets), nil
	}
	return s.batchN, s.batchErr
}

// TestAssetWatcher_SyncOnce_CountsStoreConfirmedRows —— 门A（计数语义）。
// 源表 3 条，其中 1 条空 tenant 会被 RegisterBatch 跳过 ⇒ llmAdded 必须
// 是 2（store 确认数），而不是源表条数 3。旧代码在这里返回 3（天然负控红）。
func TestAssetWatcher_SyncOnce_CountsStoreConfirmedRows(t *testing.T) {
	syncer := &fakeSyncer{
		llms: []apihub.Asset{
			{RefID: 1, TenantID: "tenant1", Name: "ok-1"},
			{RefID: 2, TenantID: "tenant1", Name: "ok-2"},
			{RefID: 3, TenantID: "", Name: "skipped-empty-tenant"},
		},
	}
	hub := apihub.New(okStore{})
	watcher := NewAssetWatcher(hub, syncer)

	llmAdded, _, err := watcher.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce failed: %v", err)
	}
	if llmAdded != 2 {
		t.Errorf("llmAdded = %d, want 2 (store-confirmed rows; the empty-tenant row "+
			"must be reported as skipped, not registered — R48-E1)", llmAdded)
	}
}

// TestAssetWatcher_SyncOnce_PartialFailure —— 门B（partial 计数与错误并存）。
// store 报告「写入 1 行 + 返回错误」⇒ SyncOnce 必须 llmAdded==1 且 err 非 nil；
// 不得在 err 非 nil 时回退成源表条数，也不得把计数吞成 0。
// （旧版此测试名为 PartialFailure 实际测的是空源——名实不符，本轮修正。）
func TestAssetWatcher_SyncOnce_PartialFailure(t *testing.T) {
	syncer := &fakeSyncer{
		llms: []apihub.Asset{
			{RefID: 1, TenantID: "tenant1", Name: "a"},
			{RefID: 2, TenantID: "tenant1", Name: "b"},
		},
		mcps: []apihub.Asset{
			{RefID: 100, TenantID: "tenant1", Name: "mcp-ok"},
		},
	}
	store := countingStore{batchN: 1, batchErr: errors.New("apihub: simulated chunk failure")}
	hub := apihub.New(store)
	watcher := NewAssetWatcher(hub, syncer)

	llmAdded, mcpAdded, err := watcher.SyncOnce(context.Background())
	if err == nil {
		t.Fatal("expected store error to propagate, got nil")
	}
	if llmAdded != 1 {
		t.Errorf("llmAdded = %d, want 1 (store-confirmed partial count, R48-E1)", llmAdded)
	}
	if mcpAdded != 1 {
		t.Errorf("mcpAdded = %d, want 1 (LLM failure must not block MCP)", mcpAdded)
	}
}

// TestAssetWatcher_SyncOnce_FullSuccessCountsAll —— 门C（防二值化回归）。
// 全部成功时 llmAdded 必须等于源表条数——防止后人把「成功数」改成 0/全量二值。
func TestAssetWatcher_SyncOnce_FullSuccessCountsAll(t *testing.T) {
	syncer := &fakeSyncer{
		llms: []apihub.Asset{
			{RefID: 1, TenantID: "tenant1", Name: "a"},
			{RefID: 2, TenantID: "tenant1", Name: "b"},
		},
	}
	hub := apihub.New(okStore{})
	watcher := NewAssetWatcher(hub, syncer)

	llmAdded, _, err := watcher.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce failed: %v", err)
	}
	if llmAdded != 2 {
		t.Errorf("llmAdded = %d, want 2 (full success must report every row)", llmAdded)
	}
}
