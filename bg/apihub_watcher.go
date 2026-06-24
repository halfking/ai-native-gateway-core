// Package bg — apihub watcher (主干 A A1-1 / A1-2).
//
// Periodically syncs existing resource tables (model_offers, api_keys,
// tool_registry.tools) into the unified assets table via apihub.Service.
// This is the bridge that keeps the Hub populated without requiring every
// new resource to manually call Service.Register.
//
// Pattern follows bg/audit_trimmer.go: Start/Stop/StopOnce lifecycle.
// The source-table reads are behind a Syncer interface so tests can inject
// an in-memory source (no DB needed). The default production Syncer reads
// from *pgxpool.Pool (see pgSyncer, added when relay wires this up).
package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// AssetSyncSource is the source-table reader the watcher polls. The default
// implementation (pgSyncer) reads from PostgreSQL; tests inject a fake.
//
// Each method returns the assets to upsert in the current sync cycle. The
// watcher then calls Service.Register for each. Returning an error logs a
// warning but does NOT abort the other source types — partial progress is
// better than none.
type AssetSyncSource interface {
	LLMEndpoints(ctx context.Context) ([]apihub.Asset, error)
	MCPServers(ctx context.Context) ([]apihub.Asset, error)
}

// AssetWatcher periodically syncs the source tables into the asset hub.
type AssetWatcher struct {
	hub      *apihub.Service
	src      AssetSyncSource
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewAssetWatcher constructs the watcher. Default tick is 60s; override via
// WithInterval. Call Start to launch.
func NewAssetWatcher(hub *apihub.Service, src AssetSyncSource) *AssetWatcher {
	return &AssetWatcher{
		hub:  hub,
		src:  src,
		tick: 60 * time.Second,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// WithInterval overrides the 60s default tick.
func (w *AssetWatcher) WithInterval(d time.Duration) *AssetWatcher {
	if d > 0 {
		w.tick = d
	}
	return w
}

// Start launches the background goroutine. Performs an initial sync so a
// fresh deploy doesn't wait a full tick before the Hub is populated.
func (w *AssetWatcher) Start(ctx context.Context) {
	go w.run(ctx)
	slog.Info("apihub watcher started", "interval", w.tick.String())
}

// Stop terminates the goroutine and waits for it to finish. Idempotent.
func (w *AssetWatcher) Stop() {
	if w.stop == nil || w.done == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// SyncOnce triggers an immediate sync (admin use). Returns per-source-type
// counts of how many assets were registered, and any error encountered.
// Errors from one source do not block the other.
func (w *AssetWatcher) SyncOnce(ctx context.Context) (llmAdded, mcpAdded int64, err error) {
	if w.hub == nil || w.src == nil {
		return 0, 0, nil
	}
	start := time.Now()

	// LLM endpoints
	if llms, e := w.src.LLMEndpoints(ctx); e != nil {
		slog.Warn("apihub watcher: LLMEndpoints source error", "error", e)
		err = e
	} else {
		for _, a := range llms {
			a.Kind = apihub.KindLLMEndpoint
			if regErr := w.hub.Register(ctx, a); regErr != nil {
				slog.Warn("apihub watcher: register LLM asset failed",
					"ref_id", a.RefID, "tenant", a.TenantID, "error", regErr)
				continue
			}
			llmAdded++
		}
	}

	// MCP servers
	if mcps, e := w.src.MCPServers(ctx); e != nil {
		slog.Warn("apihub watcher: MCPServers source error", "error", e)
		if err == nil {
			err = e
		}
	} else {
		for _, a := range mcps {
			a.Kind = apihub.KindMCPServer
			if regErr := w.hub.Register(ctx, a); regErr != nil {
				slog.Warn("apihub watcher: register MCP asset failed",
					"ref_id", a.RefID, "tenant", a.TenantID, "error", regErr)
				continue
			}
			mcpAdded++
		}
	}

	slog.Info("apihub watcher: sync complete",
		"llm_added", llmAdded, "mcp_added", mcpAdded,
		"duration_ms", time.Since(start).Milliseconds())
	return
}

func (w *AssetWatcher) run(ctx context.Context) {
	defer close(w.done)

	// Initial sync on startup so the Hub is populated immediately.
	//nolint:errcheck // best-effort; logged inside SyncOnce
	w.SyncOnce(ctx)

	tk := time.NewTicker(w.tick)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-tk.C:
			//nolint:errcheck // best-effort; logged inside SyncOnce
			w.SyncOnce(ctx)
		}
	}
}
