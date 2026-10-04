// Package bg — asset health probe (Phase 7).
//
// AssetHealthProbe periodically marks stale assets (last_seen_at older than
// 6h) as HealthDegraded, and assets that have disappeared from the source
// tables as HealthDown. The UI's stats cards surface the resulting
// health_state counts.
//
// 2026-07-23 (Phase 2.3 收敛进度): 本探针只读 assets 表，无需走 system-monitor
// 队列（无节点探测语义）。仅在 KEEP/FUTURE 审计列表里登记，不在切流范围内。
//
// FUTURE: 当系统监测 v3 引入资产级别监控（asset-level health）后，此 worker
// 应迁移为 system-monitor 的一个新 source 字段，原代码保留做 cross-check。
// [@platform] [trigger 2027-Q2]
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
	Go("asset_health_probe.run", func() { p.run(ctx) })
	slog.Info("asset health probe started",
		"stale_threshold", p.staleThreshold.String(),
		"tick", p.tick.String())
}

func (p *AssetHealthProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

// ProbeOnce runs one cycle for all tenants. Returns degraded/removed counts.
func (p *AssetHealthProbe) ProbeOnce(ctx context.Context) (degraded, removed int64, err error) {
	if p.hub == nil || p.syncer == nil {
		return 0, 0, nil
	}
	start := time.Now()

	// Get all tenants
	tenants, err := p.hub.ListTenants(ctx)
	if err != nil {
		slog.Warn("asset health: list tenants failed", "error", err)
		return 0, 0, err
	}
	if len(tenants) == 0 {
		return 0, 0, nil
	}

	// Process each tenant
	for _, tenant := range tenants {
		tenantCtx := apihub.WithTenant(ctx, tenant)
		d, r := p.probeOneTenant(tenantCtx, tenant)
		degraded += d
		removed += r
	}

	slog.Info("asset health probe: cycle complete",
		"tenants", len(tenants), "degraded", degraded, "removed", removed,
		"duration_ms", time.Since(start).Milliseconds())
	return
}

// probeOneTenant runs one cycle for a single tenant. Returns degraded/removed counts.
func (p *AssetHealthProbe) probeOneTenant(ctx context.Context, tenant string) (degraded, removed int64) {
	// Step 1: mark stale assets as degraded.
	stale, err := p.hub.ListStale(ctx, p.staleThreshold)
	if err != nil {
		slog.Warn("asset health: list stale failed", "tenant", tenant, "error", err)
	}
	// 2026-10-05（runbook §10.26）：下面这个 `continue` 原本只挡 HealthDown，
	// 于是**已经是 Degraded 的资产每轮都被重新标一次 Degraded**。实测每轮
	// 439 次全部是写同一个值 —— 纯空写，却照样产生 n_tup_upd 与堆脏页。
	// 门控放在 Go 侧而不是 markHealthSQL 的 `IS DISTINCT FROM` 守卫：
	// markHealthSQL 带 `RETURNING 1` 且调用方用 QueryRow().Scan()，
	// 加守卫后 ErrNoRows 会同时表示「行不存在」和「值本来就对」，
	// 而 Store.MarkHealth 把 ErrNoRows 一律映射成 ErrNotFound
	// —— 那会改掉 MarkHealth 的对外语义。调用方手里已有当前值
	// （listStaleSQL 选了 health_state），在这里判等号零成本。
	for _, a := range stale {
		// 已经是目标态（Degraded）或终态（Down）就不再写。
		// 两者都保持原语义：Down 的资产不会被降级回 Degraded。
		if a.HealthState == apihub.HealthDegraded || a.HealthState == apihub.HealthDown {
			continue
		}
		if e := p.hub.MarkHealth(ctx, a.Kind, a.RefID, apihub.HealthDegraded); e != nil {
			slog.Warn("asset health: mark degraded failed",
				"tenant", tenant, "ref_id", a.RefID, "error", e)
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

	// Fetch all assets with pagination (1000 per batch)
	const batchSize = 1000
	offset := 0
	for {
		batch, err := p.hub.List(ctx, apihub.Filter{Limit: batchSize})
		if err != nil {
			slog.Warn("asset health: list all failed", "tenant", tenant, "offset", offset, "error", err)
			break
		}
		if len(batch) == 0 {
			break
		}
		for _, a := range batch {
			key := string(a.Kind) + "|" + a.TenantID + "|" + itoa64(a.RefID)
			if !liveLookup[key] {
				// 2026-10-05（runbook §10.26）：实测每轮 396 次调用全部是
				// 把**已经是 Down** 的行再写一遍 Down。与上面 Step 1 同源。
				// 门控理由与取舍见 Step 1 的注释（不能放 SQL 侧，
				// 否则 ErrNoRows 的含义会从「行不存在」变成二义）。
				if a.HealthState == apihub.HealthDown {
					continue
				}
				if e := p.hub.MarkHealth(ctx, a.Kind, a.RefID, apihub.HealthDown); e != nil {
					continue
				}
				removed++
			}
		}
		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}
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
