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
	"sync/atomic"
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
	// started records that Start() launched the run goroutine. Stop() must
	// not wait on `done` when it was never started — `done` is closed by
	// run() and would never be closed at all (2026-10-21 R71).
	started atomic.Bool
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
	// 2026-10-21 (R71): mark the worker as running before the goroutine
	// starts. Stop() waits on p.done, which is closed by run() — so a probe
	// that was constructed but never started must not be waited on.
	p.started.Store(true)
	Go("asset_health_probe.run", func() { p.run(ctx) })
	slog.Info("asset health probe started",
		"stale_threshold", p.staleThreshold.String(),
		"tick", p.tick.String())
}

func (p *AssetHealthProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	if !p.started.Load() {
		// Never started: run() never launched, so p.done is never closed.
		return
	}
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
	//
	// 2026-11-23（211 配置优化遗留 L4）：这里是**集合差**，交给 SQL 做。
	//
	// ★ 旧实现为什么被换掉：
	//
	//	① 它把整张 assets 表按 500/页拉进 Go 侧（apihub.Filter{Offset}），
	//	   再用内存 liveLookup 逐行比对、逐行 MarkHealth ——
	//	   这就是 AGENTS.md 已登记的「统计面禁止列表聚合」同族；
	//	② 它带一个 20 页 × 500 行 = 10000 行的上界，**超过上界的行静默不再被检查**；
	//	③ 它的上界告警判据是**反的**：`if offset+pageSize <= maxPages*pageSize`
	//	   在「正常跑完（提前 break）」时成立 ⇒ 每轮都喊「可能超过 10000 行」，
	//	   而真撞上界（offset 恰好等于 10000）时反而不喊。
	//	   ⇒ 211 上那条 WARN 从来不是「表太大」，而是**判据写反了**；
	//	   实测 default 租户只有 1420 行资产（远低于上界）。
	//
	// ★ 新实现：按 kind 把「还活着的 ref_id」交给 apihub.MarkMissingDown，
	//   由一条 UPDATE ... NOT (ref_id = ANY(...)) 完成差集 ⇒ 没有上界、
	//   没有 N+1 次写、没有 Go 侧全表扫描（2026-11-23 211 实测口径核对：
	//   default 租户 1420 行资产 / 1376 行仍活 / 44 行已 down，与 SQL 反连结果一致）。
	//
	// ★ 失败语义（重要）：源表查询**失败**时绝不能把「空集合」当成「真的空了」
	//   —— 那会把该租户的全部资产一次性打成 down。旧代码 `liveLLMs, _ := ...`
	//   吞掉了错误 ⇒ 查询失败 ⇒ liveLookup 为空 ⇒ 全表被标 down（静默灾）。
	//   现在：查询失败就**跳过该 kind**，不动任何行，并留下 WARN。
	liveLLMs, llmErr := p.syncer.LLMEndpoints(ctx)
	liveMCPs, mcpErr := p.syncer.MCPServers(ctx)
	if llmErr != nil {
		slog.Warn("asset health: list live llm endpoints failed (skip marking down)",
			"tenant", tenant, "error", llmErr)
	}
	if mcpErr != nil {
		slog.Warn("asset health: list live mcp servers failed (skip marking down)",
			"tenant", tenant, "error", mcpErr)
	}

	type liveSource struct {
		kind  apihub.Kind
		items []apihub.Asset
		err   error
	}
	for _, src := range []liveSource{
		{apihub.KindLLMEndpoint, liveLLMs, llmErr},
		{apihub.KindMCPServer, liveMCPs, mcpErr},
	} {
		if src.err != nil {
			continue // 源表查询失败 ⇒ 本 kind 不做任何标记（见上面的失败语义）
		}
		liveRefIDs := make([]int64, 0, len(src.items))
		for _, a := range src.items {
			if a.Kind == src.kind {
				liveRefIDs = append(liveRefIDs, a.RefID)
			}
		}
		n, err := p.hub.MarkMissingDown(ctx, src.kind, liveRefIDs)
		if err != nil {
			slog.Warn("asset health: mark missing down failed",
				"tenant", tenant, "kind", string(src.kind), "error", err)
			continue
		}
		if n > 0 {
			removed += n
			slog.Info("asset health: marked missing assets down",
				"tenant", tenant, "kind", string(src.kind),
				"live", len(liveRefIDs), "marked_down", n)
		}
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
