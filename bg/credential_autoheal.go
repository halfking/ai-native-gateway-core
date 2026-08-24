// Package bg — credential_autoheal.go
//
// CredentialAutoHealWorker 主动探测长期 Disabled 的凭据，成功即恢复状态。
//
// 2026-08-23 (hzx-2 audit): 之前的状态机自恢复缺陷是：被动恢复路径只清
// in-memory副本（credentialfpslot/node_state.go 的 recoverIfCooldownExpired），
// 而 Redis 上的 state.disabled 需要真实成功请求触发 cooldown_expired + success
// 分支才会清。这导致"reset 后又被写坏"+"无真实请求→永久 lock"的死锁。
//
// 本 worker 周期性扫描 disabled=true 且 elapsed 超过 grace 窗口的 (cred, model)
// 对，把它们 Submit 给 NodeProbeWorker。如果上游真的恢复了，runOne 成功
// 分支会自然清 cmb.available=true + cmb.unavailable_reason=NULL；如果还没
// 恢复，runOne 失败分支只更新 next_retry_at 不动 cooldown，**不会**加重状态。
//
// 与 credential_selfcheck.go 的区别：
//   - credential_selfcheck 每天一个凭据做真请求探测（HTTP round）；
//   - credential_autoheal 每 5min 批量扫描所有到期的 disabled (cred, model)
//     对，复用 NodeProbeWorker 的现成直连+网关两轮探测，**不**自己发请求。
//
// 接口：
//   - Start(parent) / Stop() 生命周期对齐 credential_selfcheck
//   - OneShot(ctx, credentialID) 同步触发，立即入队一批 (cred, model)
//     由 admin /api/routing/credentials/{id}/reset-state 调用
//   - tickInterval 默认 5min，可由 SetTickInterval 调整
package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	met "github.com/kaixuan/llm-gateway-go/metrics" //nolint:depguard // routing credential observability
)

// autoHealCycleInterval is the wake-up cadence. Each tick submits a batch
// of (cred, model) pairs that have been Disabled long enough that the
// upstream had time to recover. The window is 5 minutes — same as the
// fpslot cooldown — so this worker does NOT race with the natural cooldown
// recovery path (which already covers those pairs).
const autoHealCycleInterval = 5 * time.Minute

// autoHealMinDisabledDuration is the minimum time the binding must have
// been available=FALSE before we re-probe. Setting this >= nodeDisabledCooldownSec
// (5min) avoids duplicate probes right after a fresh failure.
const autoHealMinDisabledDuration = 5 * time.Minute

// autoHealMaxDisabledDuration is the safety upper bound. Bindings that
// have been unavailable for longer than this are considered "stale" and
// skipped — they're abandoned credentials we should not auto-heal.
const autoHealMaxDisabledDuration = 7 * 24 * time.Hour

// autoHealBatchSize caps how many pairs are submitted per tick to avoid
// thundering-herd against the probe worker.
const autoHealBatchSize = 16

// dueAutoHealSQL returns the (credential_id, tenant_id, raw_model_name)
// triples that are eligible for proactive self-healing:
//
//   - cmb.available = FALSE (binding-level disable)
//   - unavailable_reason in ('continuous_failure', 'auto_cooling', 'probe_*')
//     (skip permanent kinds: manual_*, auth_failed)
//   - last_used_at within last 7 days (don't auto-heal abandoned creds)
//   - unavailable_at elapsed >= 5min (cooldown expired)
//   - node_probe_state not paused and next_retry_at <= now() (probe worker
//     already in mid-cycle)
func dueAutoHealSQL() string {
	return `
		SELECT cmb.credential_id, c.tenant_id, pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE cmb.available = FALSE
		  AND cmb.unavailable_reason NOT LIKE 'manual%'
		  AND cmb.unavailable_reason NOT IN ('auth_failed', 'quota_permanent',
		                                    'balance_exhausted', 'permanently_exhausted')
		  AND cmb.unavailable_at IS NOT NULL
		  AND cmb.unavailable_at <= now() - $2::interval
		  AND cmb.unavailable_at >= now() - $3::interval
		  AND COALESCE(c.last_used_at, now() - interval '1 hour') >= now() - $4::interval
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND NOT EXISTS (
		      SELECT 1 FROM node_probe_state nps
		      WHERE nps.credential_id = cmb.credential_id
		        AND nps.raw_model_name = pm.raw_model_name
		        AND (nps.paused = TRUE OR nps.next_retry_at > now())
		  )
		ORDER BY cmb.unavailable_at ASC
		LIMIT $1
	`
}

// probeSubmitter is the subset of NodeProbeWorker the autoheal worker
// depends on. Defined as a function value so wiring in main.go is a
// one-liner closure (mirrors the admin.probeSubmitter pattern).
type probeSubmitter func(credentialID int, rawModel, tenantID, parentReqID string)

// CredentialAutoHealWorker 周期性扫描并提交自愈探测。
type CredentialAutoHealWorker struct {
	db             *pgxpool.Pool
	submit         probeSubmitter
	tickInterval   time.Duration
	minDisabledDur time.Duration
	maxDisabledDur time.Duration
	batchSize      int

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewCredentialAutoHealWorker 构造 worker。submit 可以为 nil（测试场景），
// 此时 worker 仅扫描 + metric，不真正提交探测。
func NewCredentialAutoHealWorker(db *pgxpool.Pool, submit probeSubmitter) *CredentialAutoHealWorker {
	if db == nil {
		// Caller intentionally disabled; surface a no-op so Start() is safe
		// but no SQL ever runs.
		return &CredentialAutoHealWorker{}
	}
	return &CredentialAutoHealWorker{
		db:             db,
		submit:         submit,
		tickInterval:   autoHealCycleInterval,
		minDisabledDur: autoHealMinDisabledDuration,
		maxDisabledDur: autoHealMaxDisabledDuration,
		batchSize:      autoHealBatchSize,
		stopCh:         make(chan struct{}),
	}
}

// SetTickInterval adjusts the wake-up cadence. Useful for tests + ops
// tuning (e.g. reduce to 1min during a known outage).
func (w *CredentialAutoHealWorker) SetTickInterval(d time.Duration) {
	if d > 0 {
		w.tickInterval = d
	}
}

// Start launches the loop goroutine. Idempotent: second and subsequent
// calls are no-ops, so accidental double wiring from main.go won't fan out
// two probe loops on the same worker.
func (w *CredentialAutoHealWorker) Start(parent context.Context) {
	if w == nil || w.db == nil {
		slog.Info("credential_autoheal_worker disabled (nil db)")
		return
	}
	var alreadyStarted bool
	w.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		w.cancel = cancel
		w.wg.Add(1)
		go w.loop(ctx)
		slog.Info("credential_autoheal_worker started",
			"tick_interval", w.tickInterval,
			"min_disabled_duration", w.minDisabledDur,
			"max_disabled_duration", w.maxDisabledDur,
			"batch_size", w.batchSize,
		)
	})
	_ = alreadyStarted
}

// Stop signals termination and waits for the loop to exit. Safe to call
// on the zero-value worker returned when db == nil (no-op), and on a
// worker that was never Start'd.
func (w *CredentialAutoHealWorker) Stop() {
	if w == nil || w.stopCh == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
}

// OneShot 立即跑一次 cycle，不等 tick。admin reset-state 端点用它给操作员
// 一个"立即触发"的入口 — 不用等 5 分钟。
//
// 与周期 SQL 的差异：保留 `node_probe_state` 守卫，避免与仍在 mid-cycle
// 的 probe worker 形成重复提交；其它过滤条件一致。
func (w *CredentialAutoHealWorker) OneShot(ctx context.Context, credentialID int) int {
	if w == nil || w.db == nil {
		return 0
	}
	sql := `
		SELECT cmb.credential_id, c.tenant_id, pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE cmb.available = FALSE
		  AND cmb.unavailable_reason NOT LIKE 'manual%'
		  AND cmb.unavailable_reason NOT IN ('auth_failed', 'quota_permanent',
		                                    'balance_exhausted', 'permanently_exhausted')
		  AND cmb.unavailable_at <= now() - $2::interval
		  AND cmb.unavailable_at >= now() - $3::interval
		  AND COALESCE(c.last_used_at, now() - interval '1 hour') >= now() - $4::interval
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND cmb.credential_id = $5
		  AND NOT EXISTS (
		      SELECT 1 FROM node_probe_state nps
		      WHERE nps.credential_id = cmb.credential_id
		        AND nps.raw_model_name = pm.raw_model_name
		        AND (nps.paused = TRUE OR nps.next_retry_at > now())
		  )
		LIMIT $1
	`
	return w.runCycle(ctx, sql, []any{
		w.batchSize,
		w.minDisabledDur.String(),
		w.maxDisabledDur.String(),
		w.maxDisabledDur.String(), // also used as last_used_at window
		credentialID,
	})
}

func (w *CredentialAutoHealWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			n := w.runCycle(ctx, dueAutoHealSQL(), []any{
				w.batchSize,
				w.minDisabledDur.String(),
				w.maxDisabledDur.String(),
				w.maxDisabledDur.String(),
			})
			if n > 0 {
				slog.Info("credential_autoheal_worker: submitted self-heal probes",
					"count", n)
			}
		}
	}
}

func (w *CredentialAutoHealWorker) runCycle(ctx context.Context, sql string, args []any) int {
	if w.submit == nil {
		// Wiring is nil (older builds / tests). Count as skipped and exit.
		met.RoutingAutoHealSubmitTotal.WithLabelValues("skipped_no_due").Inc()
		return 0
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := w.db.Query(queryCtx, sql, args...)
	if err != nil {
		slog.Warn("credential_autoheal_worker: due-pair query failed",
			"error", err)
		met.RoutingAutoHealSubmitTotal.WithLabelValues("error").Inc()
		return 0
	}
	defer rows.Close()
	submitted := 0
	for rows.Next() {
		var credID int
		var tenantID string
		var rawModel string
		if err := rows.Scan(&credID, &tenantID, &rawModel); err != nil {
			slog.Warn("credential_autoheal_worker: scan failed", "error", err)
			continue
		}
		// Defensive fallback: a NULL tenant_id (legacy rows) must not
		// collapse into the shared "default" namespace, otherwise probes
		// for two distinct tenants would collide on Redis key + URSM v2.
		if tenantID == "" {
			tenantID = "default"
			slog.Warn("credential_autoheal_worker: tenant_id missing, defaulting",
				"credential_id", credID, "raw_model", rawModel)
		}
		w.submit(credID, rawModel, tenantID, "autoheal")
		submitted++
		met.RoutingAutoHealSubmitTotal.WithLabelValues("submitted").Inc()
	}
	if err := rows.Err(); err != nil {
		slog.Warn("credential_autoheal_worker: rows iteration failed",
			"error", err)
	}
	return submitted
}
