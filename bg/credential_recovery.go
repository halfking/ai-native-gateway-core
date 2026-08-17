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
	cancel                   context.CancelFunc
	done                     chan struct{}
	tickMu                   sync.Mutex
	tickInterval             time.Duration
	// tickIntervalEverSet is false until SetTickInterval is called for the
	// first time, so tickIntervalLocked can distinguish "no override yet,
	// use the default" from "operator just disabled us with SetTickInterval(0)".
	tickIntervalEverSet bool
}

func NewCredentialRecovery(db *pgxpool.Pool) *CredentialRecovery {
	return &CredentialRecovery{db: db, done: make(chan struct{})}
}

// SetProbeSubmitter wires the (credID, model) -> probe enqueue hook.
// Safe to call multiple times; the latest non-nil setter wins.
func (r *CredentialRecovery) SetProbeSubmitter(fn func(credID int, model string)) {
	if fn == nil {
		return
	}
	r.probeSubmitter = fn
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

func (r *CredentialRecovery) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	slog.Info("credential recovery task started", "interval", r.tickIntervalLocked().String())
}

func (r *CredentialRecovery) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
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

	// 2026-08-07 P0 修复：availability 恢复必须先于 quota 恢复执行。
	// 原因：suspended 恢复的条件要求 quota_state 当前不是硬配额（见下方
	// suspended 守卫）。若 quota SQL 先跑把 periodic_exhausted 清成 'ok'，
	// availability 恢复就分不清"本次刚到期的 periodic"与"本来就 ok"，
	// 无法正确联动。先跑 availability（读到真实 quota_state），再跑 quota
	// （按 quota_recover_at 到期清除）才能各取所需。
	tag, err := r.db.Exec(timeoutCtx, `
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
	`)
	if err != nil {
		slog.Warn("credential availability recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("credential availability recovered", "count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_updated_at = now()
		WHERE quota_state = 'periodic_exhausted'
		  AND quota_recover_at IS NOT NULL
		  AND quota_recover_at <= now()
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("credential quota recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("credential quota recovered", "count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, stalePeriodicExhaustedCleanupSQL())
	if err != nil {
		slog.Warn("stale periodic_exhausted cleanup failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("stale periodic_exhausted cleared (credentials already healthy)",
			"count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET circuit_state = 'closed',
		    cooling_until = NULL,
		    consecutive_failures = 0,
		    state_updated_at = now()
		WHERE circuit_state = 'open'
		  AND (cooling_until IS NULL OR cooling_until <= now())
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("circuit breaker recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("circuit breakers closed", "count", tag.RowsAffected())
	}

	tag, err = r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET consecutive_failures = 0,
		    state_updated_at = now()
		WHERE consecutive_failures > 0
		  AND last_used_at < now() - INTERVAL '1 hour'
		  AND circuit_state = 'closed'
		  AND availability_state = 'ready'
		  AND lifecycle_status = 'active'
	`)
	if err != nil {
		slog.Warn("failure counter clear failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("stale failure counters cleared", "count", tag.RowsAffected())
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
	tag, err = r.db.Exec(timeoutCtx, `
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
	`)
	if err != nil {
		slog.Warn("health_status recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Warn("stale health_status reset to 'unknown' (re-probe will rerun shortly)",
			"count", tag.RowsAffected(),
		)
	}

	tag, err = r.db.Exec(timeoutCtx, mnfCoolingRecoverySQL(), mnfCoolingRecoveryMinutes())
	if err != nil {
		slog.Warn("mnf_cooling binding recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("mnf_cooling bindings recovered", "count", tag.RowsAffected())
		// Mirror the cmb recovery onto model_offers so /api/routing/resolve
		// ("test route") and the admin UI badges agree with the production
		// router. Without this, mnf_cooling restores the binding on the
		// cmb side but the offer still shows unavailable on model_offers
		// until manual admin intervention.
		if moTag, moErr := r.db.Exec(timeoutCtx, mnfCoolingRecoveryMirrorSQL(), mnfCoolingRecoveryMinutes()); moErr != nil {
			slog.Warn("mnf_cooling model_offers mirror recovery failed", "error", moErr)
		} else if moTag.RowsAffected() > 0 {
			slog.Info("mnf_cooling model_offers mirrored", "count", moTag.RowsAffected())
		}
	}

	// Reconcile the independent node_probe_state gate after the binding and
	// credential surfaces recover. A stale failed/backoff row otherwise keeps
	// v_routable_credential_models false even though the supplier is healthy.
	if tag, err := r.db.Exec(timeoutCtx, `
		UPDATE node_probe_state nps
		SET consecutive_failures = 0,
		    consecutive_successes = GREATEST(nps.consecutive_successes, 1),
		    next_retry_at = now() + interval '1 hour',
		    next_retry_seconds = 3600,
		    paused = FALSE,
		    in_flight_until = NULL,
		    last_direct_ok = TRUE,
		    last_gateway_ok = TRUE,
		    last_err_code = NULL,
		    last_err_detail = NULL,
		    updated_at = now()
		WHERE (
			nps.last_direct_ok IS DISTINCT FROM TRUE
			OR nps.paused = TRUE
			OR nps.next_retry_at > now()
		)
		AND EXISTS (
			SELECT 1
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			JOIN credentials c ON c.id = cmb.credential_id
			JOIN providers p ON p.id = c.provider_id
			WHERE cmb.credential_id = nps.credential_id
			  AND pm.raw_model_name = nps.raw_model_name
			  AND cmb.available = TRUE
			  AND COALESCE(c.status, 'active') = 'active'
			  AND COALESCE(c.lifecycle_status, 'active') = 'active'
			  AND COALESCE(c.manual_disabled, FALSE) = FALSE
			  AND COALESCE(p.enabled, TRUE) = TRUE
			  AND COALESCE(p.manual_disabled, FALSE) = FALSE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  )
	`); err != nil {
		slog.Warn("node probe state recovery failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("node probe states reconciled after credential recovery", "count", tag.RowsAffected())
		if _, notifyErr := r.db.Exec(timeoutCtx,
			"SELECT pg_notify('auto_route_refresh', 'node-probe-recovery')"); notifyErr != nil {
			slog.Warn("node probe recovery route refresh notify failed", "error", notifyErr)
		}
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
//	- cmb.available = FALSE + unavailable_reason = 'continuous_failure'.
//	  Other reasons use different code paths:
//	    - 'manual*' : operators chose those; never auto-restore.
//	    - 'probe_*' : already covered by node_probe.go's own re-arm ladder.
//	    - 'auto_*'   : written by domains/credential/writer.go for transient
//	                   per-model failures; out of scope for this branch.
//	- unavailable_recover_at IS NOT NULL AND > now() (i.e., still in cooldown).
//	- unavailable_at <= now() - 60 seconds. Don't re-probe a row that was
//	  just marked unavailable seconds ago — give the original failure burst
//	  a chance to settle. 60s matches the original 60s tick so the first
//	  re-check happens on the second tick after degradation.
//	- Same hard guards as the expired branch (manual, lifecycle, provider,
//	  admin_protected, availability_state, paused).
//	- Skip rows whose node_probe_state already has a future next_retry_at
//	  so we don't pile probes on top of an in-flight backoff ladder.
//	- ORDER BY oldest unavailable_at first so the most-stale rows (the
//	  ones most likely to have recovered upstream-side) get probed first.
//	- LIMIT 30/tick to bound fan-out.
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
