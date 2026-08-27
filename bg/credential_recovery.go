package bg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	met "github.com/kaixuan/llm-gateway-go/metrics" //nolint:depguard // routing credential observability (2026-08-23 hzx-2 audit)
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// credentialRecoveryDB is the minimal database contract CredentialRecovery
// needs. Both *pgxpool.Pool and pgxmock.PgxPoolIface satisfy it, which
// lets us test the recovery flow without a real PostgreSQL instance.
type credentialRecoveryDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Default tick for the CredentialRecovery loop. The previous hard-coded 60s
// was fine when continuous_failure carried a 15-minute cooldown: by the
// time the tick re-queried expired bindings, ~14 minutes had passed and the
// cooldown was about to elapse on its own. After 2026-08-17 we want the
// recovery loop to (a) check expired bindings sooner so cooldown finishes
// don't have to wait a full tick boundary, and (b) probe fresh-degraded
// bindings during the cooldown window. 30s hits both goals without flooding
// the database or upstream providers.
const defaultCredentialRecoveryInterval = 30 * time.Second

// disabledProbeInterval is the heart-beat used while the loop is in the
// "disabled" state (SetTickInterval(0)). We can't simply stop the ticker
// because then the loop couldn't notice when an operator sets the interval
// back to a positive value — so we use a long heart-beat (10 minutes) just
// to re-check the configured interval. The recover() body is skipped while
// disabled, so the loop is effectively idle.
const disabledProbeInterval = 10 * time.Minute

// SetTickInterval lets callers tune the recovery loop period after Start.
//
// Semantics:
//   - d < 0: ignored (defensive; env parsing may yield -1).
//   - d == 0: the loop enters a disabled state — recover() is not called
//     until a subsequent SetTickInterval(<positive>) re-arms it. Useful
//     for maintenance windows when ops want to silence the worker.
//   - 0 < d < 1s: clamped to 1s to keep the DB and upstream friendly.
//   - d >= 1s: used as-is.
//
// The change is picked up at the next ticker boundary.
func (r *CredentialRecovery) SetTickInterval(d time.Duration) {
	if d < 0 {
		return
	}
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	r.tickIntervalEverSet = true
	if d == 0 {
		r.tickInterval = 0 // disabled sentinel
		return
	}
	if d < time.Second {
		d = time.Second
	}
	r.tickInterval = d
}

// tickIntervalLocked returns the configured interval, or the default if no
// explicit value has ever been set (the sentinel -1 marks "never set"). A
// value of 0 means "disabled": run() must NOT call NewTicker(0), which
// would panic. See SetTickInterval for the disabled semantics.
func (r *CredentialRecovery) tickIntervalLocked() time.Duration {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	if r.tickInterval == 0 && !r.tickIntervalEverSet {
		return defaultCredentialRecoveryInterval
	}
	return r.tickInterval
}

type CredentialRecovery struct {
	db credentialRecoveryDB
	// lookbackDB is the transaction-capable handle used by the 36h lookback
	// scan's SELECT ... FOR UPDATE SKIP LOCKED claim (会话优化 v4 T5 /
	// R4.4). Production wires the same *pgxpool.Pool as db; tests inject a
	// pgxmock pool. Separated from the credentialRecoveryDB interface so the
	// legacy recover() tick keeps its narrow contract.
	lookbackDB lookbackBeginner
	// lookbackHot supplies scan-interval / lookback-window overrides from
	// settings_kv (hotconfig.Config satisfies it). nil → env/boot defaults.
	lookbackHot LookbackHotConfig
	// ursmRecoverSink writes evidence-backed recovery records into URSM v2
	// at Recover(30) priority — the integrator wires it to
	// (*ursmv2.Manager).ApplyProbeForTenantWithSource with
	// api.SourcePriorityRecover. nil → the URSM half of the scan is a no-op
	// (the dual-round probe submission still runs).
	ursmRecoverSink func(ctx context.Context, tenantID string, credID int, model string, success bool, latencyMs int) error
	// probeSubmitter hands (credential_id, raw_model_name) pairs to
	// NodeProbeWorker.Submit so the authoritative probe path can
	// flip cmb.available when it confirms the upstream is healthy
	// again. Wired from cmd/gateway/main.go AFTER NodeProbeWorker
	// is constructed; the 60s recovery tick is safe to call this
	// even when the worker isn't ready yet (Submit is idempotent and
	// the next probe cycle tolerates a 5s/30s/60s backoff).
	probeSubmitter func(credID int, model string)
	// invalidateCandidateCache flushes the per-credential candidate
	// snapshot so the next request re-plans with the recovered
	// binding visible without waiting for the 30s candCache TTL.
	// Wired from main.go via SetInvalidateCandidateCache.
	invalidateCandidateCache func(credID int)
	// onQuotaRecovered is the dispatcher-facing hook fired when a probe
	// path (cycleAll / fastProbe / probeQueueWorker) flips a credential
	// back to healthy after a quota-recovery flip. The signature carries
	// (credID, source) so the wired handler can route the metric label
	// and pick the right cache invalidator without consulting a global.
	// Wired from main.go via SetOnQuotaRecovered; nil → the probe paths
	// stay silent (the routing layer falls back to its candCache TTL).
	// 2026-08-26 quota-recovery-notify fix: closes the
	// "DB says ready but cache still excludes credential" gap.
	onQuotaRecovered func(credID int, source string)
	// probeSubmitterImmediate is the *synchronous* probe-submitter
	// fired by dispatchRecoveryHooks the moment a credential flips out
	// of a quota-blocked / availability-blocked state. Wired from
	// main.go via SetProbeSubmitterImmediate (typically bound to
	// credProbeV2.ProbeNowAsync). nil → only the delayed
	// fastReprobeQueue path runs, behaviour matches pre-P1-4.
	// 2026-08-26 P1-4 (落点 D).
	probeSubmitterImmediate func(credID int)
	cancel                  context.CancelFunc
	done                    chan struct{}
	// lookbackDone signals the 36h lookback scan loop exited (Stop waits on
	// both). Constructed together with done.
	lookbackDone     chan struct{}
	lifecycleMu      sync.Mutex
	started          bool
	stopped          bool
	tickMu           sync.Mutex
	tickInterval     time.Duration
	lookbackInterval time.Duration
	// tickIntervalEverSet is false until SetTickInterval is called for the
	// first time, so tickIntervalLocked can distinguish "no override yet,
	// use the default" from "operator just disabled us with SetTickInterval(0)".
	tickIntervalEverSet bool
}

func NewCredentialRecovery(db *pgxpool.Pool) *CredentialRecovery {
	return &CredentialRecovery{
		db:           db,
		lookbackDB:   db,
		done:         make(chan struct{}),
		lookbackDone: make(chan struct{}),
	}
}

// SetProbeSubmitter wires the (credID, model) -> probe enqueue hook.
// Safe to call multiple times; the latest non-nil setter wins.
func (r *CredentialRecovery) SetProbeSubmitter(fn func(credID int, model string)) {
	if fn == nil {
		return
	}
	r.probeSubmitter = fn
}

// SetProbeSubmitterImmediate wires a *second* probe-submitter hook that
// fires synchronously from the recovery tick the moment a credential flips
// out of a quota-blocked / availability-blocked state. Unlike
// SetProbeSubmitter (which goes through the delayed fastReprobeQueue with
// the 5-minute default — user principle "≥5 分钟探测一次" preserved; see
// .handoff/selfcheck-audit-2026-08-26.md §2.1), this hook bypasses the
// delay so the first chat request after a recovery sees a fresh probe
// rather than waiting for the next scheduled cycle.
//
// 2026-08-26 P1-4 (landing point D): the immediate-submit path closes
// the "recover() flips the DB but the next chat request still picks a
// fallback because no probe has re-validated the credential yet" gap
// for vendors whose probe is cheap (vendor accounts with
// health-check-style endpoints). Vendors whose probe is expensive
// (token-billing accounts that pay per request) should leave this hook
// unwired — the existing delayed queue is the safer default.
//
// Safe to call multiple times; the latest non-nil setter wins. nil →
// no-op (only the delayed path is used; behavior reverts to pre-P1-4).
func (r *CredentialRecovery) SetProbeSubmitterImmediate(fn func(credID int)) {
	if fn == nil {
		return
	}
	r.probeSubmitterImmediate = fn
}

// SetInvalidateCandidateCache wires the per-credential candidate
// cache invalidator so the next chat request re-plans with the
// recovered binding visible.
func (r *CredentialRecovery) SetInvalidateCandidateCache(fn func(credID int)) {
	if fn == nil {
		return
	}
	r.invalidateCandidateCache = fn
}

// SetOnQuotaRecovered wires the dispatcher-facing notification that the
// probe paths (cycleAll / fastProbe / probeQueueWorker) call once a
// credential's quota / availability state has been flipped back to healthy.
// The (credID, source) signature lets the integrator route the label +
// invalidator from a single closure. Safe to call multiple times; the
// latest non-nil setter wins. nil → no-op (the probe paths stay silent,
// the routing layer falls back to its candCache TTL).
//
// 2026-08-26 quota-recovery-notify fix: without this hook the probe path
// would write healthy into the DB but the routing layer's candidate
// cache would still exclude the credential until TTL elapses, so the
// first chat request after a recharge still picks a fallback node.
func (r *CredentialRecovery) SetOnQuotaRecovered(fn func(credID int, source string)) {
	if fn == nil {
		return
	}
	r.onQuotaRecovered = fn
}

// SetURSMRecoverSink wires the Recover(30) URSM v2 write used by the 36h
// lookback scan (会话优化 v4 T5 / R4.4). The integrator binds it to
// (*ursmv2.Manager).ApplyProbeForTenantWithSource with
// api.SourcePriorityRecover. Safe to call before Start; nil keeps the URSM
// half of the scan disabled.
func (r *CredentialRecovery) SetURSMRecoverSink(fn func(ctx context.Context, tenantID string, credID int, model string, success bool, latencyMs int) error) {
	if fn == nil {
		return
	}
	r.ursmRecoverSink = fn
}

// SetLookbackHotConfig wires the settings_kv reader for the scan interval
// (llmgw_recovery_lookback_interval_seconds) and lookback window hours
// (llmgw_recovery_lookback_window_hours). *hotconfig.Config satisfies the
// interface; nil keeps env/boot defaults.
func (r *CredentialRecovery) SetLookbackHotConfig(src LookbackHotConfig) {
	r.lookbackHot = src
}

func (r *CredentialRecovery) Start(ctx context.Context) {
	r.lifecycleMu.Lock()
	if r.started && !r.stopped {
		r.lifecycleMu.Unlock()
		return
	}
	if r.stopped {
		r.lifecycleMu.Unlock()
		return
	}
	if r.done == nil {
		r.done = make(chan struct{})
	}
	if r.lookbackDone == nil {
		r.lookbackDone = make(chan struct{})
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.started = true
	r.lifecycleMu.Unlock()
	go r.run(ctx)
	go r.runLookbackScan(ctx)
	slog.Info("credential recovery task started",
		"interval", r.tickIntervalLocked().String(),
		"lookback_scan_interval", r.lookbackScanIntervalLocked().String())
}

func (r *CredentialRecovery) Stop() {
	r.lifecycleMu.Lock()
	if !r.started || r.stopped {
		r.lifecycleMu.Unlock()
		return
	}
	r.stopped = true
	cancel := r.cancel
	r.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-r.done
	if r.lookbackDone != nil {
		<-r.lookbackDone
	}
}

func (r *CredentialRecovery) run(ctx context.Context) {
	defer close(r.done)

	// resolveInterval maps the configured value to the ticker period we
	// actually use. 0 (disabled) → disabledProbeInterval so the loop can
	// still notice a subsequent SetTickInterval(<positive>). Any other
	// value is used as-is.
	resolveInterval := func() (period time.Duration, enabled bool) {
		v := r.tickIntervalLocked()
		if v == 0 {
			return disabledProbeInterval, false
		}
		return v, true
	}

	period, enabled := resolveInterval()
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	slog.Info("credential_recovery: run loop armed",
		"period", period.String(), "enabled", enabled)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newPeriod, newEnabled := resolveInterval()
			if newPeriod != period {
				period = newPeriod
				ticker.Reset(period)
			}
			if !newEnabled {
				// Disabled: heart-beat only. Don't call recover().
				continue
			}
			r.recover(ctx)
		}
	}
}

func (r *CredentialRecovery) recover(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 2026-08-23 (hzx-2 audit): record tick duration and per-block recovery
	// outcomes so on-call engineers can see whether the 30s tick is actually
	// recovering nodes, hitting permanent-kind guards, or erroring out. The
	// tick duration gauge surfaces silent slowdowns (e.g. DB connection
	// pool exhaustion) that would otherwise only show up after a full
	// outage. The per-block counters expose the root cause directly.
	start := time.Now()
	defer func() {
		met.RoutingCredentialRecoveryTickDurationSeconds.Set(time.Since(start).Seconds())
	}()
	recordOutcome := func(sqlKind, outcome string) {
		met.RoutingCredentialRecoveryTotal.WithLabelValues(sqlKind, outcome).Inc()
	}

	// 2026-08-26 (quota-recovery-notify fix): every per-block UPDATE that
	// flips a credential back to healthy MUST publish the affected credential
	// IDs so the routing layer's candidate cache invalidates immediately
	// and the durable probe queue receives a fresh submission. Without this,
	// the 30s tick logs "recovered=N" but the next chat request still sees
	// the stale snapshot until candCache TTL or the next 5-min
	// PeriodicQuotaProbe tick.
	//
	// dispatchRecoveryHooks queries the same UPDATE statement with
	// RETURNING id, calls InvalidateCandidateCache + probeSubmitter per row,
	// and increments the per-action counter. It is nil-safe (Exec fallback
	// when both hooks are nil so RowsAffected semantics don't change) and
	// mirrors the defensive pattern of recoverExpiredBindings /
	// recoverFreshDegradedBindings / reconcileStaleNodeProbeStates.
	//
	// Returns (affectedRowCount, err) — same shape as r.db.Exec.
	dispatchRecoveryHooks := func(sqlKind, sqlText string, args ...any) (int, error) {
		if r.invalidateCandidateCache == nil && r.probeSubmitter == nil && r.probeSubmitterImmediate == nil {
			// All hooks nil: cheap Exec path so the outcome counter /
			// RowsAffected semantics don't change.
			tag, execErr := r.db.Exec(timeoutCtx, sqlText, args...)
			if execErr != nil {
				return 0, execErr
			}
			return int(tag.RowsAffected()), nil
		}
		rows, qErr := r.db.Query(timeoutCtx, sqlText, args...)
		if qErr != nil {
			return 0, qErr
		}
		defer rows.Close()

		seen := make(map[int]struct{})
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				slog.Warn("credential_recovery: RETURNING id scan failed",
					"sql_kind", sqlKind, "error", err)
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			if r.invalidateCandidateCache != nil {
				r.invalidateCandidateCache(id)
				met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "invalidate").Inc()
			}
			if r.probeSubmitter != nil {
				// Empty model → NodeProbeWorker.Submit picks the credential's
				// default_probe_model (its existing "let the worker decide"
				// sentinel — see main.go:3504 where expired-binding-recovery
				// uses the same shape).
				r.probeSubmitter(id, "")
				met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "probe_submit").Inc()
			}
		}
		if err := rows.Err(); err != nil {
			return len(seen), err
		}
		// 2026-08-26 P1-4 (landing point D): for recovery-flip actions
		// (quota_periodic_recover / availability_recover) bypass the
		// fastReprobeQueue's 30s delay (P1-2 default) by firing an immediate
		// probe per unique credential that just flipped. The local `seen`
		// map already dedupes RETURNING ids within this UPDATE block.
		// nil probeSubmitterImmediate is silently skipped — falls back
		// to the delayed probeSubmitter path. Observability bump lets
		// operators confirm the immediate path actually fires (otherwise
		// it would be invisible).
		if r.probeSubmitterImmediate != nil && len(seen) > 0 &&
			(sqlKind == "quota_periodic_recover" || sqlKind == "availability_recover") {
			for id := range seen {
				r.probeSubmitterImmediate(id)
			}
			met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "probe_immediate").Inc()
		}
		return len(seen), nil
	}

	// 2026-08-18 fix (glm-5.2 outage): 智谱 GLM Coding Plan 5 小时窗口的 429
	// 在 44f197505 之前被误标为 quota_permanent / permanently_exhausted（硬配额），
	// 且 quota_recover_at / availability_recover_at 均为 NULL —— 上游窗口每 5
	// 小时重置一次，但这两个"永久"状态没有任何自动恢复路径（上面的 periodic
	// 恢复只认 periodic_exhausted，suspended 恢复被硬配额守卫拦住）。存量行必须
	// 一次性重评：state_reason_detail 以 '[quota_periodic]' 开头即周期性证据，
	// 降级为 periodic_exhausted 并把两个 recover_at 置为立即可恢复，交由下方
	// 既有恢复块 + PeriodicQuotaProbe 探活纠偏。新增 429 不会再进入这条路径
	// （writer 已按 5h 窗口分类）。
	tag, err := r.db.Exec(timeoutCtx, misclassifiedPeriodicQuotaReclassSQL())
	if err != nil {
		slog.Warn("misclassified periodic quota reclass failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("misclassified periodic quota reclassified (permanently_exhausted → periodic_exhausted)",
			"count", tag.RowsAffected())
	}

	// 2026-08-07 P0 修复：availability 恢复必须先于 quota 恢复执行。
	// 原因：suspended 恢复的条件要求 quota_state 当前不是硬配额（见下方
	// suspended 守卫）。若 quota SQL 先跑把 periodic_exhausted 清成 'ok'，
	// availability 恢复就分不清"本次刚到期的 periodic"与"本来就 ok"，
	// 无法正确联动。先跑 availability（读到真实 quota_state），再跑 quota
	// （按 quota_recover_at 到期清除）才能各取所需。
	availSQL := `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_updated_at = now()
		WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed','suspended')
		  AND (
		      -- 2026-07-27 fix: auth_failed never sets availability_recover_at
		      -- (credential_probe_v2.go sets it to NULL), so we allow auth_failed
		      -- to recover even when availability_recover_at IS NULL. The next
		      -- successful node_probe clears auth_failed via updateCredentialHealth.
		      availability_state = 'auth_failed'
		      OR (
		          availability_recover_at IS NOT NULL
		          AND availability_recover_at <= now()
		      )
		  )
		  -- 2026-08-07 P0 死锁修复：'suspended' 之前不在上面的 IN 列表里，
		  -- 导致 quota_periodic 写入的 suspended 永远无法自动恢复。
		  --
		  -- 实测证据（154 生产）：cred 22 (zhipu-roocode-v2) 的
		  -- availability_state='suspended' / availability_recover_at=2026-08-10，
		  -- 而 quota_state 已被 stale-cleanup 清成 'ok' —— 状态自相矛盾，
		  -- 且没有任何自动路径能把它翻回 ready，只能靠 admin 人工 force_enable。
		  -- 这就是 zhima-max/zhipu 反复"可用↔不可用"翻转的根因：人工救回后
		  -- 又被下一次 quota 命中打回 suspended，循环往复。
		  --
		  -- suspended 的恢复必须比其它状态更严格，因为它同时被
		  -- quota_balance / quota_permanent / auth_revoked 使用：
		  --   1. 必须有到期的 availability_recover_at（由上面的 OR 分支保证）。
		  --      auth_revoked / quota_balance 写的是 NULL，因此不会被误救。
		  --   2. 硬配额（余额/永久用尽）仍未解除时不放行 —— 那类凭据只能由
		  --      balance_quota_probe 探活成功后经 writeHealth 翻回。
		  AND (
		      availability_state <> 'suspended'
		      OR COALESCE(quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  )
		  AND lifecycle_status = 'active'
		  -- 2026-06-22 defect (4): do NOT auto-restore a credential to 'ready'
		  -- while any of its (credential, model) bindings is still
		  -- model_probe_state='broken_confirmed'. The recovery ticker used to
		  -- flip availability_state back to 'ready' every 60s unconditionally,
		  -- which re-admitted the credential into the candidate pool even
		  -- though the per-model probe had proven the model was gone — producing
		  -- the fail -> unreachable(120s) -> ready -> re-select -> fail loop seen
		  -- on cred-11/minimax-m3. The probe worker only re-marks a binding
		  -- healthy_confirmed via a manual nudge (TriggerManual), so guarding
		  -- here cannot strand a credential that has actually recovered.
		  AND NOT EXISTS (
		      SELECT 1
		      FROM model_probe_state mps
		      -- 2026-07-16: dropped dead "OR pm.standardized_name = mps.raw_model_name"
		      -- branch. model_probe_state.raw_model_name stores the upstream
		      -- vendor form (e.g. "z-ai/glm-5.2"); pm.standardized_name is
		      -- lowercase+unprefixed (e.g. "glm-5.2") and can never match it,
		      -- so the OR was dead code. Same defect removed from
		      -- credentialhealth/checker.go (9e7eb23f1) and provider/client.go.
		      JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
		      JOIN credential_model_bindings cmb
		           ON cmb.credential_id = mps.credential_id
		          AND cmb.provider_model_id = pm.id
		      WHERE mps.credential_id = credentials.id
		        AND mps.state = 'broken_confirmed'
		        AND cmb.available = FALSE
		  )
		  RETURNING id
	`
	availRecovered, availErr := dispatchRecoveryHooks("availability_recover", availSQL)
	switch {
	case availErr != nil:
		slog.Warn("credential availability recovery failed", "error", availErr)
		recordOutcome("availability_recover", "error")
	case availRecovered > 0:
		slog.Info("credential availability recovered", "count", availRecovered)
		recordOutcome("availability_recover", "recovered")
	default:
		recordOutcome("availability_recover", "no_row")
	}

	quotaRecovered, quotaErr := dispatchRecoveryHooks("quota_periodic_recover", `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_updated_at = now()
		WHERE quota_state = 'periodic_exhausted'
		  AND quota_recover_at IS NOT NULL
		  AND quota_recover_at <= now()
		  AND lifecycle_status = 'active'
		RETURNING id
	`)
	switch {
	case quotaErr != nil:
		slog.Warn("credential quota recovery failed", "error", quotaErr)
		recordOutcome("quota_periodic_recover", "error")
	case quotaRecovered > 0:
		slog.Info("credential quota recovered", "count", quotaRecovered)
		recordOutcome("quota_periodic_recover", "recovered")
	default:
		// 2026-08-23 (hzx-2 audit): "no_row" likely means quota_recover_at
		// was NULL (permanent kind was written). Operators reading the
		// counter trend can spot a permanent-kind buildup that needs
		// admin force_enable — exactly the failure mode that produced the
		// hzx-2 outage.
		recordOutcome("quota_periodic_recover", "no_row")
	}

	staleRecovered, staleErr := dispatchRecoveryHooks("stale_periodic_exhausted_cleanup", stalePeriodicExhaustedCleanupSQL()+"\n\t\tRETURNING id")
	switch {
	case staleErr != nil:
		slog.Warn("stale periodic_exhausted cleanup failed", "error", staleErr)
		recordOutcome("stale_periodic_exhausted_cleanup", "error")
	case staleRecovered > 0:
		slog.Info("stale periodic_exhausted cleared (credentials already healthy)",
			"count", staleRecovered)
		recordOutcome("stale_periodic_exhausted_cleanup", "recovered")
	default:
		recordOutcome("stale_periodic_exhausted_cleanup", "no_row")
	}

	circuitRecovered, circuitErr := dispatchRecoveryHooks("circuit_close", `
		UPDATE credentials
		SET circuit_state = 'closed',
		    cooling_until = NULL,
		    consecutive_failures = 0,
		    state_updated_at = now()
		WHERE circuit_state = 'open'
		  AND (cooling_until IS NULL OR cooling_until <= now())
		  AND lifecycle_status = 'active'
		RETURNING id
	`)
	switch {
	case circuitErr != nil:
		slog.Warn("circuit breaker recovery failed", "error", circuitErr)
		recordOutcome("circuit_close", "error")
	case circuitRecovered > 0:
		slog.Info("circuit breakers closed", "count", circuitRecovered)
		recordOutcome("circuit_close", "recovered")
	default:
		recordOutcome("circuit_close", "no_row")
	}

	fcRecovered, fcErr := dispatchRecoveryHooks("consecutive_failures_clear", `
		UPDATE credentials
		SET consecutive_failures = 0,
		    state_updated_at = now()
		WHERE consecutive_failures > 0
		  AND last_used_at < now() - INTERVAL '1 hour'
		  AND circuit_state = 'closed'
		  AND availability_state = 'ready'
		  AND lifecycle_status = 'active'
		RETURNING id
	`)
	switch {
	case fcErr != nil:
		slog.Warn("failure counter clear failed", "error", fcErr)
		recordOutcome("consecutive_failures_clear", "error")
	case fcRecovered > 0:
		slog.Info("stale failure counters cleared", "count", fcRecovered)
		recordOutcome("consecutive_failures_clear", "recovered")
	default:
		recordOutcome("consecutive_failures_clear", "no_row")
	}

	// Reset stale health_status="unreachable" / "auth_failed" / "error" rows.
	//
	// Root cause (2026-06-12): v_routable_credential_models.is_routable
	// requires health_status IN ('healthy', 'unknown'). A single cycler/probe-v2
	// failure marks a credential as 'unreachable', which sets is_routable=FALSE.
	// Without this recovery branch, every credential flagged unreachable
	// stays unroutable until the next probe runs (up to 90 minutes). During
	// that window, all providers share the same root failure cause, and
	// users see a "every provider fails at the same time" outage.
	//
	// Recovery rule: re-probe a credential as soon as its health_status is
	// not 'healthy'/'unknown' for more than 2 minutes. The next cycler
	// (every hour) or probe-v2 (next :30 mark) will overwrite health_status
	// with a fresh result, and a successful probe restores routability.
	healthRecovered, healthErr := dispatchRecoveryHooks("health_status_reset", `
		UPDATE credentials
		SET health_status = 'unknown',
		    health_error = NULL,
		    health_source = 'probe',
		    health_checked_at = NOW(),
		    state_updated_at = NOW()
		WHERE health_status NOT IN ('healthy', 'unknown')
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND (health_checked_at IS NULL OR health_checked_at < NOW() - INTERVAL '2 minutes')
		  AND COALESCE(availability_state, 'ready') NOT IN ('suspended', 'auth_failed')
		RETURNING id
	`)
	if healthErr != nil {
		slog.Warn("health_status recovery failed", "error", healthErr)
	} else if healthRecovered > 0 {
		slog.Warn("stale health_status reset to 'unknown' (re-probe will rerun shortly)",
			"count", healthRecovered,
		)
	}

	// mnf_cooling_recover writes to credential_model_bindings, not
	// credentials — so RETURNING cmb.credential_id (which the dispatchRecoveryHooks
	// helper scans into the generic `id` variable; the cache invalidator only
	// needs the credential_id).
	//
	// We also fire the dispatch for the mirror UPDATE because it changes the
	// model_offers state the admin /api/routing/resolve panel reads directly.
	mnfRecovered, mnfErr := dispatchRecoveryHooks("mnf_cooling_recover",
		mnfCoolingRecoverySQL()+"\n\t\tRETURNING cmb.credential_id",
		mnfCoolingRecoveryMinutes())
	switch {
	case mnfErr != nil:
		slog.Warn("mnf_cooling binding recovery failed", "error", mnfErr)
		recordOutcome("mnf_cooling_recover", "error")
	case mnfRecovered > 0:
		slog.Info("mnf_cooling bindings recovered", "count", mnfRecovered)
		recordOutcome("mnf_cooling_recover", "recovered")
	default:
		recordOutcome("mnf_cooling_recover", "no_row")
	}
	// Mirror the cmb recovery onto model_offers so /api/routing/resolve
	// ("test route") and the admin UI badges agree with the production
	// router. Without this, mnf_cooling restores the binding on the
	// cmb side but the offer still shows unavailable on model_offers
	// until manual admin intervention.
	moTag, moErr := r.db.Exec(timeoutCtx, mnfCoolingRecoveryMirrorSQL(), mnfCoolingRecoveryMinutes())
	if moErr != nil {
		slog.Warn("mnf_cooling model_offers mirror recovery failed", "error", moErr)
	} else if moTag.RowsAffected() > 0 {
		slog.Info("mnf_cooling model_offers mirrored", "count", moTag.RowsAffected())
	}

	// The node_probe_state row is intentionally left for the probe worker. A
	// recovery tick only repairs credential-level metadata and submits durable
	// probe work below; it must not claim direct/gateway success without an
	// upstream observation because authoritative URSM treats its own node key as
	// the runtime source of truth.
	//
	// 2026-08-18 P0 fix (commit 8ebaee0b2): the previous SQL UPDATE block here
	// wrote `last_direct_ok=TRUE`, `last_gateway_ok=TRUE` and pushed
	// `next_retry_at` one hour into the future whenever the credential /
	// provider / cmb surfaces all looked healthy. That UPDATE was the root
	// cause of the 126-line "DB says healthy, URSM tenant key missing, actual
	// request fails" cohort observed across glm-5.2 / glm-5.1 / gpt-5.5 /
	// kimi-k2.6 / doubao on 154 — without a real probe run the row had no
	// fresh evidence, so the routing filter happily picked a credential whose
	// last real attempt had been a failure and whose cool_until_ms had
	// silently elapsed without a follow-up probe. Pushing next_retry_at 1h
	// into the future also starved the node_probe backoff ladder of any
	// re-attempt before the next 30s tick.
	//
	// Replacement contract (kept intact from upstream HEAD's no-fake-success
	// position + this commit's durable-enqueue addition):
	//   * `node_probe_state.last_direct_ok/last_gateway_ok` and
	//     `node_probe_state.next_retry_at` are NEVER written from this loop.
	//     Only the unified probe path (NodeProbeWorker.Submit →
	//     ProbeQueue → ProbeService.Run → mirrorNodeProbeState) may touch
	//     them, and only after a real direct + gateway round both succeed.
	//   * reconcileStaleNodeProbeStates below is read-only SELECT. For each
	//     (cred, model) whose node_probe_state is in failed/unknown state
	//     but whose underlying cmb/credential/provider surfaces look
	//     healthy, it hands the pair to NodeProbeWorker.Submit (via the
	//     same probeSubmitter hook the expired-binding and fresh-degraded
	//     branches use). The queue's `ON CONFLICT DO NOTHING` dedup_key
	//     collapses concurrent submissions so two recover() goroutines
	//     (this process, a peer instance, or the 36h lookback scan in the
	//     same tick) cannot double-enqueue.
	//   * The URSM v2 source-priority contract is preserved: the probe
	//     path calls `updateURSMv2ProbeState` at Probe(20) priority via
	//     Manager.ApplyProbe (NOT Recover(30) — that priority belongs to
	//     scanLookbackRecoveries where the SQL predicate proved an in-window
	//     logged success). apply_probe.lua's `source_priority <= 10`
	//     guard in record_request.lua is therefore never preempted by a
	//     recovery write because we never write at Recover priority from
	//     this branch; the only Recover writes are the gated ones in the
	//     36h lookback scan (evidence-backed success=true).
	if err := r.reconcileStaleNodeProbeStates(timeoutCtx); err != nil {
		slog.Warn("node probe state stale reconciliation failed", "error", err)
	}

	// 2026-07-24 P0 fix: re-probe (cred, model) bindings whose cmb.available
	// was flipped to FALSE by continuous_failure (credentialhealth/checker.go)
	// or by a probe_* reason (bg/node_probe.go updateBindingAvailability) and
	// whose unavailable_recover_at has already elapsed. Without this branch,
	// those bindings stay excluded from v_routable_credential_models
	// indefinitely once business traffic routes around them through other
	// working credentials — Submit is only fired by real request failures,
	// so a once-failed-and-now-OK provider stays invisible until an
	// operator manually clears and re-fetches the model list. Symptom:
	//   /api/routing/resolve returns 0 (or stale) nodes; real chat
	//   requests return "no available nodes". Clearing & re-fetching
	//   resets cmb.available=TRUE via modelcatalog.UpsertCredentialModel.
	//
	// We must NOT write cmb.available=TRUE directly here: a blind restore
	// would re-admit credentials that are still broken. The probe path
	// (NodeProbeWorker.runOne success branch) is the authoritative writer
	// of cmb.available — it only flips when direct + gateway probe rounds
	// both succeed. Submit re-arms node_probe_state with next_retry_at=+5s
	// for paused rows or for rows whose ladder has elapsed; mid-cycle
	// rows are left untouched so the backoff ladder (5s/30s/60s/5m/1h/...)
	// continues to advance.
	if err := r.recoverExpiredBindings(timeoutCtx); err != nil {
		slog.Warn("expired-binding probe recovery failed", "error", err)
	}

	// 2026-08-17 P0 fix: actively probe (cred, model) bindings that were
	// just marked unavailable via the continuous_failure path even though
	// their cooldown hasn't elapsed yet. Previously the recovery loop only
	// did anything once unavailable_recover_at <= now(), which meant a
	// binding degraded by an upstream blip sat in cooldown for the entire
	// 15-minute degradedCooldown with no way to know the upstream was back.
	//
	// Symptom (154 production): ZhiMa / SenseNova / NVIDIA NIM providers
	// would flap on transient upstream 5xx, get cmb.available flipped to
	// FALSE by credentialhealth/checker.go (15-minute cooldown), and then
	// stay invisible to routing until the cooldown elapsed and the
	// recovery tick happened to land on a row whose probe succeeded —
	// total downtime ≈ cooldown + probe-cycle delay.
	//
	// This branch hands in-cooldown (cred, model) pairs to the same
	// NodeProbeWorker.Submit path that the expired branch uses; the only
	// difference is the eligibility SQL. The probe path is still the
	// authoritative writer of cmb.available (runOne success branch), so
	// we never blindly restore a binding.
	if err := r.recoverFreshDegradedBindings(timeoutCtx); err != nil {
		slog.Warn("fresh-degraded binding probe recovery failed", "error", err)
	}
}

// stalePeriodicExhaustedCleanupSQL returns the SQL that clears credentials
// stuck in quota_state='periodic_exhausted' even though a recent probe
// confirmed they are healthy.
//
// 2026-07-06 P0 fix: 清除已经健康但卡在 periodic_exhausted 的凭据。
//
// 根因（双重缺陷）：
//  1. probe-v2 对 402 错误 (classifyProbeFailure) 设置 quota_state='periodic_exhausted'
//     但不设置 quota_recover_at，导致上面的恢复 SQL（WHERE quota_recover_at IS NOT NULL）
//     永远不触发。
//  2. 成功探测时 writeHealth 使用 COALESCE($8, quota_state) 保持旧值不变，
//     导致即使探测成功也无法清除 periodic_exhausted。
//
// 运行时路径 (domains/credential/writer.go WriteOnError) 对 KindQuotaPeriodic 会设置
// quota_recover_at，但 probe-v2 路径不会。两条路径都可能把凭据推进 periodic_exhausted，
// 所以这里不能用 quota_recover_at 判断是否恢复，而应以"健康探测成功"为准。
//
// 恢复条件：health_status='healthy' 且最近 2 小时内探测过 → 凭据已恢复但 quota_state 未清除。
// 使用 2 小时窗口是因为 probe-v2 探测在每小时 :30 分运行 (nextHalfHour)，
// 两次探测间隔最大 90 分钟，1 小时窗口在边界情况下可能漏判。
// 同时重置 quota_recover_at = NULL 和 state_reason_code = NULL，避免残留数据影响下次状态判断。
// 2026-08-07 P0 补强：同时清除 quota_periodic 连带写入的
// availability_state='suspended' / availability_recover_at。
//
// 缺陷证据（154 生产，cred 22 zhipu-roocode-v2）：本 SQL 把 quota_state
// 清成 'ok' 后，availability_state 仍是 'suspended'、availability_recover_at
// 仍指向 2026-08-10。因为 domains/credential/writer.go 的 KindQuotaPeriodic
// 分支会同时写这两个 surface，而这里只回滚了其中一个，凭据于是卡在
// "quota 已恢复但 availability 仍挂起" 的矛盾态 —— 三维可用性
// (provider + credential + model) 判定里 v_routable_credential_models
// 要求 availability_state='ready'，所以它依旧不可路由，且没有任何
// 自动路径能救（那条 60s availability 恢复 SQL 原先不认 'suspended'）。
//
// 既然判定依据是"探活已确认健康"，就必须把该次 quota 事件写入的
// 全部 surface 一并回滚，保持 quota_state 与 availability_state 同进同退。
//
// 2026-08-08 P0 修复（quota_recover_at 守卫）：防止误杀"业务模型仍
// 配额耗尽"的 periodic 凭据。
//
// 缺陷证据（154 生产，cred 35 zhima-max）：业务流量对
// gpt-5.6-sol / claude-opus-5 持续命中 upstream 429
// `{"error":"usage limit exceeded","window_type":"five_hour"}`，
// writer.go 正确写入 periodic_exhausted + suspended + quota_recover_at
// （=inferQuotaRecoverAt，指向 5 小时后）。但 periodic_quota_probe
// 每 5 分钟用 default_probe_model（claude-fable-5）探测成功 → 把
// health_status 写成 'healthy'。claude-fable-5 探测成功只能证明"该
// 凭据能用低消耗 probe 模型请求"，并不代表业务模型（gpt-5.6-sol）的
// 5 小时窗口配额已恢复 —— 上游按模型/凭据计费，probe 模型不消耗
// 业务模型的配额池。60s recovery ticker 随后看到
// `periodic_exhausted AND health_status='healthy'`，无条件把
// quota_state 清回 'ok' + availability_state 清回 'ready'，路由立刻
// 重新选中该凭据，下一次业务请求再次 429 → 死循环：
//
//	429 → periodic_exhausted+suspended → probe(claude-fable-5) 成功
//	     → health healthy → stale-cleanup 清回 ok+ready
//	     → 路由重选 → 再 429 → ...
//
// 修复：仅当 quota_recover_at 已到期（或从未设置，即 probe-v2 402
// 路径）才清除。writer 路径设置了 recover_at（5 小时后），到期前
// 保持 suspended，避免循环；到期后由 60s ticker 的 quota 恢复 SQL
// （quota_recover_at <= now()）统一翻回 ok。probe-v2 对 402 写入
// periodic_exhausted 时不设 quota_recover_at（本文件注释根因 1），
// 因此 `quota_recover_at IS NULL` 分支保留原有"探活健康即恢复"的
// 兜底语义，两条路径互不干扰。
// misclassifiedPeriodicQuotaReclassSQL downgrades 存量 misclassified
// permanent-quota rows (pre-44f197505 zhipu 5h-window 429s) to periodic so
// the existing periodic/quota recovery blocks can actually fire. Guards:
// only quota_state='permanently_exhausted'; only rows whose
// state_reason_detail carries the periodic signature; recover_at columns
// use COALESCE so an already-scheduled recovery time is preserved.
func misclassifiedPeriodicQuotaReclassSQL() string {
	return `
		UPDATE credentials
		SET quota_state = 'periodic_exhausted',
		    quota_recover_at = COALESCE(quota_recover_at, now()),
		    availability_recover_at = COALESCE(availability_recover_at, now()),
		    state_updated_at = now()
		WHERE quota_state = 'permanently_exhausted'
		  AND COALESCE(state_reason_detail, '') LIKE '[quota_periodic]%'`
}

func stalePeriodicExhaustedCleanupSQL() string {
	return `
		UPDATE credentials
		SET quota_state         = 'ok',
		    quota_recover_at    = NULL,
		    state_reason_code   = NULL,
		    availability_state      = CASE
		        WHEN availability_state = 'suspended' THEN 'ready'
		        ELSE availability_state
		    END,
		    availability_recover_at = CASE
		        WHEN availability_state = 'suspended' THEN NULL
		        ELSE availability_recover_at
		    END,
		    state_updated_at    = now()
		WHERE quota_state = 'periodic_exhausted'
		  AND health_status = 'healthy'
		  AND health_checked_at > now() - INTERVAL '2 hours'
		  -- 2026-08-08 P0 守卫：writer 路径写了 quota_recover_at
		  -- （=inferQuotaRecoverAt，5 小时窗口），到期前不清除，避免
		  -- probe(probe_model) 健康被当成业务模型配额恢复 → 死循环。
		  AND (quota_recover_at IS NULL OR quota_recover_at <= now())
		  AND lifecycle_status = 'active'
	`
}

func mnfCoolingRecoveryMinutes() int {
	if v := os.Getenv("LLM_GATEWAY_MNF_COOL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func mnfCoolingRecoverySQL() string {
	return `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    updated_at = now()
		FROM credentials c, providers p
		WHERE cmb.credential_id = c.id
		  AND c.provider_id = p.id
		  AND cmb.available = FALSE
		  AND cmb.unavailable_reason = 'mnf_cooling'
		  AND cmb.unavailable_at IS NOT NULL
		  AND cmb.unavailable_at <= NOW() - make_interval(mins => $1)
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`
}

// mnfCoolingRecoveryMirrorSQL clears model_offers for the same (cred,
// model) pairs that mnfCoolingRecoverySQL just restored on the cmb side.
// /api/routing/resolve ("test route") and the admin UI read
// model_offers.available directly, so without this mirror the cmb row
// is restored but the offer still shows as unavailable.
func mnfCoolingRecoveryMirrorSQL() string {
	return `
		UPDATE model_offers mo
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    updated_at = now()
		FROM credential_model_bindings cmb,
		     provider_models pm,
		     credentials c,
		     providers p
		WHERE cmb.provider_model_id = pm.id
		  AND pm.raw_model_name = mo.raw_model_name
		  AND cmb.credential_id = mo.credential_id
		  AND cmb.credential_id = c.id
		  AND c.provider_id = p.id
		  AND mo.available = FALSE
		  AND mo.unavailable_reason = 'mnf_cooling'
		  AND mo.unavailable_at IS NOT NULL
		  AND mo.unavailable_at <= NOW() - make_interval(mins => $1)
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(mo.admin_protected, FALSE) = FALSE
	`
}

// expiredCmbRecoverySQL selects (credential_id, raw_model_name) pairs
// whose credential_model_bindings row is currently FALSE with a past
// unavailable_recover_at, AND whose unavailable_reason is one of the
// transient failure reasons that we want to re-verify before flipping
// available=TRUE.
//
// Picked reasons:
//   - 'continuous_failure'  — written by credentialhealth/checker.go:markDegraded
//     when a 1h sliding-window failure ratio crosses
//     the configured threshold (default 80%).
//   - 'probe_*'             — written by bg/node_probe.go:updateBindingAvailability
//     when the two-round probe (direct + gateway)
//     fails; errCode is appended (e.g. 'probe_http_503',
//     'probe_network_error', 'probe_network_timeout').
//   - 'auto_*'              — written by domains/credential/writer.go for
//     transient per-model failures (timeout, rate limit, upstream down, etc.).
//
// Hard guards (mirroring the credential_recovery.recover() siblings):
//   - NOT LIKE 'manual%'             — operators chose manual; never auto-flip.
//   - admin_protected = FALSE        — same.
//   - credential is active / lifecycle=active / not manual_disabled.
//   - availability_state = 'ready'   — don't re-probe a cred that's itself
//     in cooling/rate_limited/auth_failed.
//   - provider enabled / not manual_disabled.
//   - Skip when node_probe_state.paused = TRUE (operator paused).
//   - Skip when node_probe_state.next_retry_at > now() (ladder mid-cycle).
//
// The query is SELECT-only — the caller hands each row to
// NodeProbeWorker.Submit, which writes cmb.available=TRUE via runOne's
// success branch. We never write cmb.available=TRUE from this SQL.
func expiredCmbRecoverySQL() string {
	return `
		SELECT cmb.credential_id, pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c      ON c.id = cmb.credential_id
		JOIN providers p        ON p.id = c.provider_id
		WHERE cmb.available = FALSE
		  -- 2026-07-27 fix: model_probe_broken never sets unavailable_recover_at
		  -- (model_probe.go sets it to NULL), so we allow it to recover without
		  -- a scheduled time. For other reasons, require the recovery time to have elapsed.
		  AND (
		      cmb.unavailable_reason = 'model_probe_broken'
		      OR (
		          cmb.unavailable_recover_at IS NOT NULL
		          AND cmb.unavailable_recover_at <= now()
		          AND (
		              cmb.unavailable_reason IN ('continuous_failure')
		              OR cmb.unavailable_reason LIKE 'probe!_%' ESCAPE '!'
		              OR cmb.unavailable_reason LIKE 'auto!_%' ESCAPE '!'
		          )
		      )
		  )
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND c.availability_state = 'ready'
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND NOT EXISTS (
		      SELECT 1 FROM node_probe_state nps
		      WHERE nps.credential_id  = cmb.credential_id
		        AND nps.raw_model_name = pm.raw_model_name
		        AND nps.paused         = TRUE
		  )
		ORDER BY cmb.unavailable_recover_at ASC NULLS LAST
		LIMIT 50
	`
}

// recoverExpiredBindings hands expired cmb.available=FALSE rows to the
// NodeProbeWorker so the authoritative probe path can decide whether to
// flip them back. See expiredCmbRecoverySQL for the eligibility contract.
//
// The submitter is wired from cmd/gateway/main.go after NodeProbeWorker
// is constructed; if it is nil (e.g. legacy self-check mode) the
// function is a no-op and returns nil so the 60s tick continues
// without flagging a transient wiring gap as an error.
//
// Returns the first DB error verbatim so callers can decide whether
// to halt or skip — the surrounding recover() logs and continues.
func (r *CredentialRecovery) recoverExpiredBindings(ctx context.Context) error {
	if r.probeSubmitter == nil {
		return nil
	}
	rows, err := r.db.Query(ctx, expiredCmbRecoverySQL())
	if err != nil {
		return fmt.Errorf("query expired cmb bindings: %w", err)
	}
	defer rows.Close()

	type pair struct {
		credID int
		model  string
	}
	var (
		seen       []pair
		invalidSet = make(map[int]struct{})
	)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.credID, &p.model); err != nil {
			slog.Warn("expired-binding recovery scan failed", "error", err)
			continue
		}
		seen = append(seen, p)
		invalidSet[p.credID] = struct{}{}
		r.probeSubmitter(p.credID, p.model)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate expired cmb bindings: %w", err)
	}
	if len(seen) == 0 {
		return nil
	}
	if r.invalidateCandidateCache != nil {
		for credID := range invalidSet {
			r.invalidateCandidateCache(credID)
		}
	}
	slog.Info("expired-binding probe recovery queued",
		"pairs", len(seen),
		"unique_credentials", len(invalidSet),
	)
	return nil
}

// freshDegradedCmbSQL returns (credential_id, raw_model_name) pairs that
// were just marked unavailable via the continuous_failure path but whose
// unavailable_recover_at has NOT yet elapsed. The companion to
// expiredCmbRecoverySQL: while the expired path waits for cooldown to
// elapse, this one hands still-cooldown rows to NodeProbeWorker so a
// recovered ZhiMa / SenseNova / NVIDIA NIM upstream is detected within
// seconds instead of minutes.
//
// Eligibility contract:
//
//   - cmb.available = FALSE + unavailable_reason = 'continuous_failure'.
//     Other reasons use different code paths:
//   - 'manual*' : operators chose those; never auto-restore.
//   - 'probe_*' : already covered by node_probe.go's own re-arm ladder.
//   - 'auto_*'   : written by domains/credential/writer.go for transient
//     per-model failures; out of scope for this branch.
//   - unavailable_recover_at IS NOT NULL AND > now() (i.e., still in cooldown).
//   - unavailable_at <= now() - 60 seconds. Don't re-probe a row that was
//     just marked unavailable seconds ago — give the original failure burst
//     a chance to settle. 60s matches the original 60s tick so the first
//     re-check happens on the second tick after degradation.
//   - Same hard guards as the expired branch (manual, lifecycle, provider,
//     admin_protected, availability_state, paused).
//   - Skip rows whose node_probe_state already has a future next_retry_at
//     so we don't pile probes on top of an in-flight backoff ladder.
//   - ORDER BY oldest unavailable_at first so the most-stale rows (the
//     ones most likely to have recovered upstream-side) get probed first.
//   - LIMIT 30/tick to bound fan-out.
func freshDegradedCmbSQL() string {
	return `
		SELECT cmb.credential_id, pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c      ON c.id = cmb.credential_id
		JOIN providers p        ON p.id = c.provider_id
		WHERE cmb.available = FALSE
		  AND cmb.unavailable_reason = 'continuous_failure'
		  AND cmb.unavailable_at IS NOT NULL
		  AND cmb.unavailable_at <= now() - INTERVAL '60 seconds'
		  AND cmb.unavailable_recover_at IS NOT NULL
		  AND cmb.unavailable_recover_at > now()
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND c.availability_state = 'ready'
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND NOT EXISTS (
		      SELECT 1 FROM node_probe_state nps
		      WHERE nps.credential_id  = cmb.credential_id
		        AND nps.raw_model_name = pm.raw_model_name
		        AND (nps.paused = TRUE OR nps.next_retry_at > now())
		  )
		ORDER BY cmb.unavailable_at ASC
		LIMIT 30
	`
}

// recoverFreshDegradedBindings hands in-cooldown continuous_failure rows
// to the NodeProbeWorker so the upstream can be re-tested before the
// degradedCooldown elapses. Same wiring contract as
// recoverExpiredBindings: SELECT-only, hands pairs to the Submit hook,
// no direct write to cmb.available. Safe to call with a nil probeSubmitter.
func (r *CredentialRecovery) recoverFreshDegradedBindings(ctx context.Context) error {
	if r.probeSubmitter == nil {
		return nil
	}
	rows, err := r.db.Query(ctx, freshDegradedCmbSQL())
	if err != nil {
		return fmt.Errorf("query fresh-degraded cmb bindings: %w", err)
	}
	defer rows.Close()

	type pair struct {
		credID int
		model  string
	}
	var (
		seen       []pair
		invalidSet = make(map[int]struct{})
	)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.credID, &p.model); err != nil {
			slog.Warn("fresh-degraded recovery scan failed", "error", err)
			continue
		}
		seen = append(seen, p)
		invalidSet[p.credID] = struct{}{}
		// parentReqID labels the probe tile in the live stream so operators
		// can see at a glance that this probe was triggered by the new
		// fresh-degraded self-check, not by a real upstream failure.
		r.probeSubmitter(p.credID, p.model)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate fresh-degraded cmb bindings: %w", err)
	}
	if len(seen) == 0 {
		return nil
	}
	if r.invalidateCandidateCache != nil {
		for credID := range invalidSet {
			r.invalidateCandidateCache(credID)
		}
	}
	slog.Info("credential_recovery: fresh-degraded (in-cooldown) probe queued",
		"pairs", len(seen),
		"unique_credentials", len(invalidSet),
		"reason", "self_check_during_cooldown",
	)
	return nil
}

// =============================================================================
// 2026-08-18 P0 fix: replacement for the previous "fake-success" UPDATE.
//
// reconcileStaleNodeProbeStates replaces the legacy SQL UPDATE that
// unconditionally wrote
//
//	last_direct_ok = TRUE, last_gateway_ok = TRUE,
//	next_retry_at  = now() + interval '1 hour'
//
// on any node_probe_state row whose credential / cmb / provider surfaces
// all looked healthy, regardless of whether a real direct + gateway probe
// run had ever succeeded for that pair. The legacy SQL produced the
// 126-row "DB says healthy, URSM tenant key missing, real request fails"
// cohort that was the trigger for the 2026-08-18 global routing audit.
//
// This function is read-only on node_probe_state / cmb / credentials /
// providers / model_probe_state. For each (cred, model) whose
// node_probe_state shows a stale failed / backoff / paused row whose
// surfaces are all healthy it hands the pair to NodeProbeWorker.Submit via
// probeSubmitter (same hook the expired-binding and fresh-degraded
// branches use). The probe worker routes the submission through the
// durable credential_probe_queue, whose ON CONFLICT DO NOTHING on
// dedup_key prevents double-enqueue from concurrent recover() goroutines
// (this process, peer instances, or the 36h lookback scan running in the
// same tick).
//
// Eligibility (mirrors the existing recoverExpiredBindings /
// recoverFreshDegradedBindings guards so this branch composes with them):
//
//   - cmb.available = TRUE (the binding has been admitted by the probe
//     or the catalog path; otherwise the credential_recovery availability
//     UPDATE above is responsible for the row, not this branch).
//   - node_probe_state is in a stale failed/backoff/paused state —
//     i.e. at least one of last_direct_ok/last_gateway_ok/paused/
//     next_retry_at indicates the pair needs re-verification. We use
//     `last_direct_ok IS DISTINCT FROM TRUE OR paused OR next_retry_at
//     IS NULL OR next_retry_at > now()` so a row that was probed
//     successfully AND whose ladder has elapsed still gets a fresh
//     round (the ladder continues naturally — Submit's arming branch
//     only re-arms paused or expired rows).
//   - credential / lifecycle / provider / manual guards identical to
//     recoverExpiredBindings.
//   - availability_state = 'ready' (do not enqueue a probe for a
//     credential whose own state machine is in cooling / rate_limited /
//     auth_failed — those are owned by the availability UPDATE above).
//   - cmb.unavailable_reason NOT LIKE 'manual%' AND admin_protected =
//     FALSE (operators chose manual; never auto-restore).
//
// IMPORTANT: this branch NEVER writes node_probe_state columns. It only
// invokes the existing probeSubmitter, which goes through ProbeQueue →
// ProbeService.Run → mirrorNodeProbeState. That path is the single
// authoritative writer of last_direct_ok / last_gateway_ok /
// next_retry_at, and only after BOTH probe rounds succeed. The
// pg_notify('auto_route_refresh', ...) emitted from the previous fake-
// success block is no longer needed because Submit's success branch
// already calls notifyAutoRouteRefresh (probe_service.go:160).
func reconcileStaleNodeProbeStateSQL() string {
	return `
		SELECT nps.credential_id, pm.raw_model_name
		FROM node_probe_state nps
		JOIN provider_models pm ON pm.raw_model_name = nps.raw_model_name
		JOIN credential_model_bindings cmb
		     ON cmb.credential_id = nps.credential_id
		    AND cmb.provider_model_id = pm.id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers   p ON p.id = c.provider_id
		WHERE cmb.available = TRUE
			  AND COALESCE(nps.paused, FALSE) = FALSE
			  AND (
			      nps.last_direct_ok  IS DISTINCT FROM TRUE
			      OR nps.last_gateway_ok IS DISTINCT FROM TRUE
			      OR nps.next_retry_at IS NULL
			      OR nps.next_retry_at > now()
			  )

		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND c.availability_state = 'ready'
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		ORDER BY nps.updated_at ASC NULLS FIRST
		LIMIT 50
	`
}

// reconcileStaleNodeProbeStates replaces the legacy "fake-success" SQL
// UPDATE. It is read-only on node_probe_state and only invokes the
// unified probe path (probeSubmitter). Safe against a nil probeSubmitter:
// the function is a no-op so a freshly-constructed CredentialRecovery
// (before NodeProbeWorker is wired) does not flag a wiring gap as an
// error. The probe queue's ON CONFLICT DO NOTHING on dedup_key
// ("node_probe:<credID>:<model>") means two concurrent recover()
// goroutines — this instance + a peer instance + the 36h lookback scan
// running on the same tick — collapse into a single enqueue per pair.
func (r *CredentialRecovery) reconcileStaleNodeProbeStates(ctx context.Context) error {
	if r.probeSubmitter == nil {
		return nil
	}
	rows, err := r.db.Query(ctx, reconcileStaleNodeProbeStateSQL())
	if err != nil {
		return fmt.Errorf("query stale node_probe_state rows: %w", err)
	}
	defer rows.Close()

	type pair struct {
		credID int
		model  string
	}
	var (
		seen       []pair
		invalidSet = make(map[int]struct{})
	)
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.credID, &p.model); err != nil {
			slog.Warn("stale node_probe_state scan failed", "error", err)
			continue
		}
		seen = append(seen, p)
		invalidSet[p.credID] = struct{}{}
		r.probeSubmitter(p.credID, p.model)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stale node_probe_state rows: %w", err)
	}
	if len(seen) == 0 {
		return nil
	}
	if r.invalidateCandidateCache != nil {
		for credID := range invalidSet {
			r.invalidateCandidateCache(credID)
		}
	}
	slog.Info("credential_recovery: stale node_probe_state rows handed to probe queue",
		"pairs", len(seen),
		"unique_credentials", len(invalidSet),
		"reason", "stale_node_probe_state_reverify",
	)
	return nil
}

// =============================================================================
// 36h 成功回看恢复扫描 (会话优化 v4 T5 / FR-4 R4.4 / UT-CR-09)
//
// A slow, independent scan loop next to the 30s recover() tick. It selects
// (credential, model) bindings that are currently degraded/offline
// (cmb.available = FALSE with unavailable_recover_at elapsed or unset) but
// have AT LEAST ONE successful request within the lookback window (default
// 36h, configurable) in request_logs_hot or request_logs. Logged success is
// real traffic evidence that the upstream works, so for each candidate:
//
//  1. CLAIM the node_probe_state row cross-instance with the same
//     PostgreSQL `SELECT ... FOR UPDATE SKIP LOCKED` + `in_flight_until`
//     lease pattern node_probe.go's pickDueAtomically uses
//     (node_probe.go:1140-1209 — PG row locks, NOT Redis).
//  2. WRITE the URSM v2 runtime layer back to available at Recover(30)
//     priority via the ursmRecoverSink hook (integrator wires it to
//     Manager.ApplyProbeForTenantWithSource with api.SourcePriorityRecover,
//     the priority apply_probe.lua was parameterized for in T5). The write
//     is success=true ONLY — it is backed by the logged success the SQL
//     predicate demanded. The scanner never writes failure evidence at
//     Recover priority, so a failed probe can never masquerade as recovery
//     (“自检失败绝不直接回 active” is preserved by construction).
//  3. TRIGGER the dual-round (direct + gateway) probe through the existing
//     NodeProbeWorker.Submit hook (probeSubmitter), the same entry the
//     expired-binding and fresh-degraded branches above use. runOne's
//     success branch remains the authoritative writer of cmb.available —
//     this scan NEVER flips the persistent binding directly.
//
// PROBE-REUSE BOUNDARY: NodeProbeWorker.runOne / probeDirect / probeGateway
// are private methods coupled to *pgxpool.Pool, the secret keyring, and the
// proxy-aware HTTP clients, so CredentialRecovery cannot call them directly.
// Submit (via probeSubmitter) IS the existing dual-round probe entry this
// package already reuses; a "minimal direct probe" is NOT reimplemented here
// because decrypting credentials.secret_ciphertext requires the keyring,
// which CredentialRecovery deliberately does not hold. If Submit is not
// wired, the scan still performs the URSM Recover write (step 2) and skips
// step 3.
//
// 36h-window semantics (UT-CR-09): a candidate with success inside the
// window enters the scan; outside the window / no recent success → the SQL
// predicate excludes it and nothing is triggered.
// =============================================================================

const (
	// defaultLookbackScanInterval is the slow scan cadence (spec §11 T5:
	// 默认 15min, hot-configurable via llmgw_recovery_lookback_interval_seconds).
	defaultLookbackScanInterval = 15 * time.Minute
	// defaultLookbackWindowHours is the success-lookback window (36h,
	// hot-configurable via llmgw_recovery_lookback_window_hours).
	defaultLookbackWindowHours = 36
	// lookbackBatchLimit bounds per-scan fan-out so a large outage cannot
	// stampede upstream providers.
	lookbackBatchLimit = 20
	// lookbackClaimLease is the in_flight_until lease the claim transaction
	// stamps. Deliberately SHORT (unlike pickDueAtomically's 5-minute
	// execution lease): this scanner hands the actual probe to
	// NodeProbeWorker.Submit, whose arming branch clears in_flight_until, so
	// a long lease would only delay the worker's own pick. The lease exists
	// to dedup concurrent SCANNERS across instances.
	lookbackClaimLease = 30 * time.Second
)

// Hot-config keys for the lookback scan (settings_kv, platform scope).
const (
	HotKeyRecoveryLookbackIntervalSeconds = "llmgw_recovery_lookback_interval_seconds"
	HotKeyRecoveryLookbackWindowHours     = "llmgw_recovery_lookback_window_hours"
)

// LookbackHotConfig is the settings_kv read surface the scan needs.
// *hotconfig.Config satisfies it; the interface stays local so bg does not
// import hotconfig (mirrors requestjourney.IntConfigSource).
type LookbackHotConfig interface {
	GetInt(key string, defaultValue int) int
}

// lookbackTx is the transaction subset the claim needs; pgx.Tx satisfies it.
type lookbackTx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// lookbackBeginner is satisfied by *pgxpool.Pool (production) and pgxmock
// pools (tests).
type lookbackBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Metrics for the lookback scan (promauto, same registry as bg/metrics.go).
var (
	recoveryLookbackScans = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_recovery_lookback_scans_total",
		Help: "Number of 36h success-lookback recovery scan ticks executed.",
	})
	recoveryLookbackTriggers = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_recovery_lookback_triggers_total",
		Help: "Candidate outcomes per lookback scan: triggered (claimed+recovery write), lease_skipped, claim_error, recover_write_failed.",
	}, []string{"outcome"})
)

// lookbackScanIntervalLocked resolves the scan period: direct field
// override → hotconfig override → env override → default (15min). An
// EXPLICIT zero from hotconfig or env disables the scan (same sentinel
// semantics as SetTickInterval(0) on the 30s loop); positive values below
// 1s clamp to 1s.
func (r *CredentialRecovery) lookbackScanIntervalLocked() time.Duration {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	if r.lookbackInterval != 0 {
		return clampLookbackInterval(r.lookbackInterval)
	}
	if r.lookbackHot != nil {
		// -1 = key absent; 0 = explicit operator disable.
		if n := r.lookbackHot.GetInt(HotKeyRecoveryLookbackIntervalSeconds, -1); n >= 0 {
			return clampLookbackInterval(time.Duration(n) * time.Second)
		}
	}
	if raw := os.Getenv("LLM_GATEWAY_RECOVERY_LOOKBACK_INTERVAL_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return clampLookbackInterval(time.Duration(n) * time.Second)
		}
	}
	return defaultLookbackScanInterval
}

// clampLookbackInterval keeps the disabled sentinel (0) intact and lifts
// too-small positive periods to 1s.
func clampLookbackInterval(v time.Duration) time.Duration {
	if v != 0 && v < time.Second {
		return time.Second
	}
	return v
}

// lookbackWindowHoursLocked resolves the success-lookback window (hours):
// hotconfig → env → 36h default. Non-positive values fall back to the
// default so a bad settings row cannot disable the window predicate.
func (r *CredentialRecovery) lookbackWindowHoursLocked() int {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	if r.lookbackHot != nil {
		if n := r.lookbackHot.GetInt(HotKeyRecoveryLookbackWindowHours, 0); n > 0 {
			return n
		}
	}
	if raw := os.Getenv("LLM_GATEWAY_RECOVERY_LOOKBACK_WINDOW_HOURS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultLookbackWindowHours
}

// runLookbackScan is the slow scan loop. Mirrors run()'s disabled-sentinel
// handling: interval 0 (hotconfig or env) → long heart-beat, scan body
// skipped, so a later positive override re-arms the loop.
func (r *CredentialRecovery) runLookbackScan(ctx context.Context) {
	if r.lookbackDone != nil {
		defer close(r.lookbackDone)
	}
	resolve := func() (period time.Duration, enabled bool) {
		// An explicit 0 (hotconfig/env) is the disabled sentinel: the loop
		// keeps a long heart-beat so a later positive override re-arms it,
		// mirroring run()'s SetTickInterval(0) handling.
		v := r.lookbackScanIntervalLocked()
		if v <= 0 {
			return disabledProbeInterval, false
		}
		return v, true
	}
	period, enabled := resolve()
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	slog.Info("credential_recovery: 36h lookback scan armed",
		"period", period.String(), "enabled", enabled,
		"window_hours", r.lookbackWindowHoursLocked())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newPeriod, newEnabled := resolve()
			if newPeriod != period {
				period = newPeriod
				ticker.Reset(period)
			}
			if !newEnabled {
				continue
			}
			r.scanLookbackRecoveries(ctx)
		}
	}
}

// lookbackCandidateSQL selects (credential_id, raw_model_name, tenant_id)
// triples that are degraded/offline but demonstrably served successful
// traffic inside the lookback window (UT-CR-09). SELECT-only — cmb.available
// is never written here; the authoritative flip is node_probe runOne's
// success branch.
//
// Guards mirror the sibling recovery queries (expiredCmbRecoverySQL):
// manual* reasons and admin_protected rows are never auto-recovered;
// credential/provider must be active and not manually disabled;
// availability_state='ready' (a credential in cooling/auth_failed is the
// 30s tick's business, not this scan's).
//
// The 36h-success predicate searches request_logs_hot first (hot rows,
// ~7d retention) and falls back to request_logs (archived/promoted rows).
// The success-model join mirrors passive_probe_listener.go:
// COALESCE(outbound_model, client_model) = raw_model_name.
func lookbackCandidateSQL() string {
	return `
		SELECT cmb.credential_id, pm.raw_model_name, COALESCE(c.tenant_id, '')
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c      ON c.id = cmb.credential_id
		JOIN providers p        ON p.id = c.provider_id
		WHERE cmb.available = FALSE
		  -- degraded/offline with cooldown elapsed (or never scheduled):
		  -- unavailable_recover_at in the future means the cooldown still
		  -- owns the row; the fresh-degraded branch of the 30s tick covers it.
		  AND (cmb.unavailable_recover_at IS NULL OR cmb.unavailable_recover_at <= now())
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND c.availability_state = 'ready'
		  AND COALESCE(p.enabled, TRUE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  -- UT-CR-09: only nodes with a SUCCESS inside the lookback window
		  -- are candidates. Outside the window / no success → excluded,
		  -- nothing triggers.
		  AND (
		      EXISTS (
		          SELECT 1 FROM request_logs_hot rl
		          WHERE rl.credential_id = cmb.credential_id
		            AND COALESCE(rl.outbound_model, rl.client_model) = pm.raw_model_name
		            AND rl.success = TRUE
		            AND rl.ts > now() - make_interval(hours => $1)
		      )
		      OR EXISTS (
		          SELECT 1 FROM request_logs rl
		          WHERE rl.credential_id = cmb.credential_id
		            AND COALESCE(rl.outbound_model, rl.client_model) = pm.raw_model_name
		            AND rl.success = TRUE
		            AND rl.ts > now() - make_interval(hours => $1)
		      )
		  )
		ORDER BY cmb.unavailable_at ASC NULLS LAST
		LIMIT $2
	`
}

// claimLookbackCandidate leases the (cred, model) row cross-instance,
// mirroring node_probe.go pickDueAtomically (BEGIN → SELECT ... FOR UPDATE
// SKIP LOCKED → UPDATE in_flight_until → COMMIT). Differences from the
// reference, both deliberate:
//
//   - the SELECT targets ONE row (the scan already chose the candidate),
//     so "SKIP LOCKED" resolves contention between instances scanning the
//     same candidate at the same moment;
//   - a candidate with NO node_probe_state row yet (degraded purely by
//     continuous_failure, never probed) is INSERTed inside the same tx with
//     next_retry_at = now()+5s so the worker can pick it up — mirroring
//     Submit's arming — instead of being silently dropped.
//
// paused rows are never claimed (operator intent), matching
// expiredCmbRecoverySQL's paused guard.
func (r *CredentialRecovery) claimLookbackCandidate(ctx context.Context, credID int, model string) (bool, error) {
	if r.lookbackDB == nil {
		return false, fmt.Errorf("lookback claim: no transaction-capable db wired")
	}
	tx, err := r.lookbackDB.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("lookback claim begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	leaseSeconds := int(lookbackClaimLease.Seconds())

	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM node_probe_state
		WHERE credential_id = $1
		  AND raw_model_name = $2
		  AND paused = FALSE
		  AND (in_flight_until IS NULL OR in_flight_until <= now())
		FOR UPDATE SKIP LOCKED
	`, credID, model).Scan(&one)
	switch {
	case err == nil:
		// Row exists and is lease-free: stamp the lease inside the same
		// transaction (the pickDueAtomically shape).
		if _, err := tx.Exec(ctx, `
			UPDATE node_probe_state
			   SET in_flight_until = now() + make_interval(secs => $1),
			       updated_at = now()
			 WHERE credential_id = $2 AND raw_model_name = $3
		`, leaseSeconds, credID, model); err != nil {
			return false, fmt.Errorf("lookback claim lease: %w", err)
		}
	case err.Error() == "no rows in result set":
		// Absent, paused, or currently leased elsewhere. Only the ABSENT
		// case proceeds: INSERT ON CONFLICT DO NOTHING loses to an existing
		// row (paused or leased), which reports claim=false via RowsAffected.
		tag, insErr := tx.Exec(ctx, `
			INSERT INTO node_probe_state
			    (credential_id, raw_model_name, next_retry_at, next_retry_seconds,
			     paused, in_flight_until, consecutive_failures, last_err_code)
			VALUES ($1, $2, now() + interval '5 seconds', 5, FALSE,
			        now() + make_interval(secs => $3), 0, NULL)
			ON CONFLICT (credential_id, raw_model_name) DO NOTHING
		`, credID, model, leaseSeconds)
		if insErr != nil {
			return false, fmt.Errorf("lookback claim insert: %w", insErr)
		}
		if tag.RowsAffected() == 0 {
			// Row existed (paused or leased by another instance/worker).
			return false, nil
		}
	default:
		return false, fmt.Errorf("lookback claim select: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("lookback claim commit: %w", err)
	}
	return true, nil
}

// scanLookbackRecoveries runs one scan pass. Safe against nil hooks: with
// neither the recover sink nor the probe submitter wired it is a no-op (the
// SQL is not even issued, mirroring recoverFreshDegradedBindings).
func (r *CredentialRecovery) scanLookbackRecoveries(ctx context.Context) {
	if r.ursmRecoverSink == nil && r.probeSubmitter == nil {
		return
	}
	scanCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	windowHours := r.lookbackWindowHoursLocked()
	recoveryLookbackScans.Inc()

	rows, err := r.db.Query(scanCtx, lookbackCandidateSQL(), windowHours, lookbackBatchLimit)
	if err != nil {
		recoveryLookbackTriggers.WithLabelValues("scan_error").Inc()
		slog.Warn("credential_recovery: lookback candidate query failed", "error", err)
		return
	}
	type candidate struct {
		credID   int
		model    string
		tenantID string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.credID, &c.model, &c.tenantID); err != nil {
			slog.Warn("credential_recovery: lookback scan row failed", "error", err)
			continue
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("credential_recovery: lookback scan iteration failed", "error", err)
	}
	rows.Close()
	if len(candidates) == 0 {
		return
	}

	triggered := 0
	for _, c := range candidates {
		claimed, err := r.claimLookbackCandidate(scanCtx, c.credID, c.model)
		if err != nil {
			recoveryLookbackTriggers.WithLabelValues("claim_error").Inc()
			slog.Warn("credential_recovery: lookback claim failed",
				"credential_id", c.credID, "model", c.model, "error", err)
			continue
		}
		if !claimed {
			recoveryLookbackTriggers.WithLabelValues("lease_skipped").Inc()
			continue
		}
		// Evidence-backed Recover(30) write into URSM v2. success=true is
		// justified by the SQL predicate (a logged success inside the
		// window); the scanner NEVER writes failure at this priority.
		if r.ursmRecoverSink != nil {
			writeCtx, writeCancel := context.WithTimeout(scanCtx, 3*time.Second)
			if err := r.ursmRecoverSink(writeCtx, c.tenantID, c.credID, c.model, true, 0); err != nil {
				recoveryLookbackTriggers.WithLabelValues("recover_write_failed").Inc()
				slog.Warn("credential_recovery: URSM recover write failed",
					"credential_id", c.credID, "model", c.model, "error", err)
			}
			writeCancel()
		}
		// Dual-round probe through the existing NodeProbeWorker entry. The
		// probe path owns cmb.available and all failure/backoff reporting.
		if r.probeSubmitter != nil {
			r.probeSubmitter(c.credID, c.model)
		}
		if r.invalidateCandidateCache != nil {
			r.invalidateCandidateCache(c.credID)
		}
		recoveryLookbackTriggers.WithLabelValues("triggered").Inc()
		triggered++
	}
	if triggered > 0 {
		slog.Info("credential_recovery: 36h lookback scan triggered recoveries",
			"window_hours", windowHours,
			"candidates", len(candidates),
			"triggered", triggered,
		)
	}
}
