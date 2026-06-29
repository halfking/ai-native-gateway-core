// Package bg — asset health probe (Phase 7).
//
// AssetHealthProbe periodically marks stale assets (last_seen_at older than
// 6h) as HealthDegraded, and assets that have disappeared from the source
// tables as HealthDown. The UI's stats cards surface the resulting
// health_state counts.
package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// AssetHealthProbe runs an hourly cycle that downgrades stale/missing assets.
type AssetHealthProbe struct {
	hub            *apihub.Service
	syncer         AssetSyncSource
	staleThreshold time.Duration
	tick           time.Duration
	stop           chan struct{}
	done           chan struct{}
	stopOnce       sync.Once
}

// NewAssetHealthProbe constructs the probe with defaults: stale=6h, tick=1h.
func NewAssetHealthProbe(hub *apihub.Service, syncer AssetSyncSource) *AssetHealthProbe {
	return &AssetHealthProbe{
		hub:            hub,
		syncer:         syncer,
		staleThreshold: 6 * time.Hour,
		tick:           1 * time.Hour,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
}

func (p *AssetHealthProbe) WithStaleThreshold(d time.Duration) *AssetHealthProbe {
	if d > 0 {
		p.staleThreshold = d
	}
	return p
}

func (p *AssetHealthProbe) WithTick(d time.Duration) *AssetHealthProbe {
	if d > 0 {
		p.tick = d
	}
	return p
}

func (p *AssetHealthProbe) Start(ctx context.Context) {
	go p.run(ctx)
	slog.Info("asset health probe started",
		"stale_threshold", p.staleThreshold.String(),
		"tick", p.tick.String())
}

func (p *AssetHealthProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

// ProbeOnce runs one cycle. Returns degraded/removed counts.
func (p *AssetHealthProbe) ProbeOnce(ctx context.Context) (degraded, removed int64, err error) {
	if p.hub == nil || p.syncer == nil {
		return 0, 0, nil
	}
	start := time.Now()

	// Step 1: mark stale assets as degraded.
	stale, err := p.hub.ListStale(ctx, p.staleThreshold)
	if err != nil {
		slog.Warn("asset health: list stale failed", "error", err)
	}
	for _, a := range stale {
		if a.HealthState == apihub.HealthDown {
			continue
		}
		if e := p.hub.MarkHealth(ctx, a.Kind, a.RefID, apihub.HealthDegraded); e != nil {
			slog.Warn("asset health: mark degraded failed",
				"ref_id", a.RefID, "error", e)
			continue
		}
		degraded++
	}

	// Step 2: mark assets missing from source as down.
	liveLLMs, _ := p.syncer.LLMEndpoints(ctx)
	liveMCPs, _ := p.syncer.MCPServers(ctx)
	liveLookup := make(map[string]bool, len(liveLLMs)+len(liveMCPs))
	for _, a := range liveLLMs {
		liveLookup[string(a.Kind)+"|"+a.TenantID+"|"+itoa64(a.RefID)] = true
	}
	for _, a := range liveMCPs {
		liveLookup[string(a.Kind)+"|"+a.TenantID+"|"+itoa64(a.RefID)] = true
	}

	allAssets, err := p.hub.List(ctx, apihub.Filter{Limit: 1000})
	if err != nil {
		slog.Warn("asset health: list all failed", "error", err)
	}
	for _, a := range allAssets {
		key := string(a.Kind) + "|" + a.TenantID + "|" + itoa64(a.RefID)
		if !liveLookup[key] {
			if e := p.hub.MarkHealth(ctx, a.Kind, a.RefID, apihub.HealthDown); e != nil {
				continue
			}
			removed++
		}
	}

	slog.Info("asset health probe: cycle complete",
		"degraded", degraded, "removed", removed,
		"duration_ms", time.Since(start).Milliseconds())
	return
}

func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func (p *AssetHealthProbe) run(ctx context.Context) {
	defer close(p.done)

	//nolint:errcheck // best-effort
	p.ProbeOnce(ctx)

	tk := time.NewTicker(p.tick)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-tk.C:
			//nolint:errcheck // best-effort
			p.ProbeOnce(ctx)
		}
	}
}
