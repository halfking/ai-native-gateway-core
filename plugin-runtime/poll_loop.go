package pluginruntime

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// CatalogLister is the maintain-catalog surface PollLoop needs.
// *MaintainCatalogClient satisfies it.
type CatalogLister interface {
	Catalog(ctx context.Context) ([]CatalogEntry, error)
	FindRelease(entries []CatalogEntry, pluginID, version string) (CatalogRelease, CatalogArtifact, error)
}

// PluginUpgrader is the install surface PollLoop needs.
// *Installer satisfies it.
type PluginUpgrader interface {
	Install(ctx context.Context, pluginID, version string) error
}

// Compile-time checks that the concrete types satisfy the interfaces.
var (
	_ CatalogLister  = (*MaintainCatalogClient)(nil)
	_ PluginUpgrader = (*Installer)(nil)
)

// PollLoopConfig configures a PollLoop.
type PollLoopConfig struct {
	Interval          time.Duration            // tick period; must be > 0
	Client            CatalogLister            // queries maintain catalog
	Installer         PluginUpgrader           // installs new versions
	InstalledVersions func() map[string]string // returns {pluginID: version} of running plugins
	GatewayVersion    string                   // for versionCompatible check (used by FindRelease)
}

// PollLoop periodically polls the maintain plugin catalog and auto-installs
// newer versions of already-installed plugins. It does NOT auto-install
// plugins that are not yet present locally (the operator installs new plugin
// types explicitly via POST /api/v1/plugins/install; the loop then keeps
// them up to date). Mirrors HealthLoop's Start/Stop lifecycle.
type PollLoop struct {
	cfg    PollLoopConfig
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPollLoop constructs a PollLoop. Interval must be > 0; the caller is
// responsible for not constructing a loop when polling is disabled.
func NewPollLoop(cfg PollLoopConfig) *PollLoop {
	return &PollLoop{cfg: cfg}
}

// Start launches the background loop. It runs one tick immediately, then on
// each Interval. Calling Start more than once is not supported.
func (p *PollLoop) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.cfg.Interval)
		defer ticker.Stop()
		p.tick(ctx) // run once immediately
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.tick(ctx)
			}
		}
	}()
}

// Stop cancels the loop and waits for the in-flight tick to finish.
// Safe to call multiple times.
func (p *PollLoop) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.wg.Wait()
}

// tick queries the catalog and installs newer versions of installed plugins.
// One plugin's install failure does not block others. Catalog errors are
// logged and retried next tick; the loop never panics.
func (p *PollLoop) tick(ctx context.Context) {
	entries, err := p.cfg.Client.Catalog(ctx)
	if err != nil {
		slog.Warn("plugin poll: catalog fetch failed", "error", err)
		return
	}
	installed := p.cfg.InstalledVersions()
	for _, entry := range entries {
		currentVersion, isInstalled := installed[entry.PluginID]
		if !isInstalled {
			continue // only upgrade plugins already installed locally
		}
		if entry.LatestVersion == "" || entry.LatestVersion == currentVersion {
			continue // no newer version available
		}
		// FindRelease applies versionCompatible against the client's gateway
		// version (for the real MaintainCatalogClient) and platform/arch
		// filtering. An error means the release isn't selectable for this
		// gateway, so we skip it.
		if _, _, err := p.cfg.Client.FindRelease(entries, entry.PluginID, entry.LatestVersion); err != nil {
			slog.Debug("plugin poll: release not selectable, skipping",
				"plugin", entry.PluginID,
				"version", entry.LatestVersion,
				"error", err)
			continue
		}
		slog.Info("plugin poll: upgrading",
			"plugin", entry.PluginID,
			"from", currentVersion,
			"to", entry.LatestVersion)
		if err := p.cfg.Installer.Install(ctx, entry.PluginID, entry.LatestVersion); err != nil {
			slog.Error("plugin poll: install failed",
				"plugin", entry.PluginID,
				"version", entry.LatestVersion,
				"error", err)
			// continue to next plugin — one failure shouldn't block others
		}
	}
}
