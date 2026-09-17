package bg

// ----------------------------------------------------------------------------
// MERGE AUDIT NOTE (2026-08-27, conflict resolution: keep HEAD / local)
//
// During merge of origin/main into local main, this file conflicted. Both
// sides added fields/methods to BalanceQuotaProbe. Per policy, the LOCAL
// (HEAD) version is preserved as the primary code. The REMOTE (origin/main)
// version was originally retained as a commented-out reference block at the
// bottom of this file; it was removed in the R36 redundancy sweep (2026-09-17)
// — retrieve it from git history (pre-R36 revisions, merge commit 4b512d640)
// if a diff against it is ever needed.
// The local additions (probeNowAsync, onQuotaRecharged, forceCooldown,
// forceMu, forceLastSeen fields, plus SetProbeNowAsync / SetOnQuotaRecharged
// / OnQuotaRecharged methods, the forceCooldown env-var parsing in
// NewBalanceQuotaProbe, and the 2026-08-23 hzx-2 audit additions doc
// block) were authored on this branch and must not be lost when
// integrating origin/main.
// ----------------------------------------------------------------------------

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BalanceQuotaProbe 专门针对需要充值才能恢复的配额类型（balance_exhausted、permanently_exhausted）
// 进行更频繁的探测，以便及时感知用户充值后的恢复。
//
// 与 PeriodicQuotaProbe 的区别：
//   - PeriodicQuotaProbe: 针对周期性配额用尽（periodic_exhausted），默认 5 分钟探测一次
//   - BalanceQuotaProbe: 针对余额/永久配额用尽（balance_exhausted、permanently_exhausted），
//     默认 2 分钟探测一次，更快感知充值恢复
//
// 探测策略：
//  1. 选择 quota_state IN ('balance_exhausted', 'permanently_exhausted') 的活跃凭据
//  2. 通过 credential_probe_v2 进行实际探测（调用上游 API）
//  3. 如果探测成功，probe-v2 会自动清除 quota_state 并恢复 availability_state
//  4. 如果探测失败且仍然是配额错误，保持当前状态不变
//
// 2026-08-23 hzx-2 audit additions:
//   - ForceProbe(credID): bypass the 2-min tick for admin-triggered
//     re-checks after a user manually topped up. Rate-limited so a
//     panic-clicking operator can't pile up probes.
//   - last_balance_check_at bookkeeping on credentials.balance_last_checked_at
//     for observability and to dedupe against double-submissions.
//   - default_probe_model fallback: when the operator hasn't set one,
//     pick the cheapest routable model from credential_model_bindings
//     so the probe always has a target. The probe-v2 worker still
//     honours default_probe_model — this fallback only matters for
//     the BalanceQuotaProbe SELECT predicate.
type BalanceQuotaProbe struct {
	db             *pgxpool.Pool
	interval       time.Duration
	probeSubmitter func(credID int)
	stopCh         chan struct{}
	stopOnce       sync.Once

	// lifecycleMu + started/workerDone: 与 PeriodicQuotaProbe 同款生命周期
	// 守卫——Start 无重入保护会叠出双份 ticker 循环（每 tick 双份探测提交），
	// goroutine 顶层 recover 防单次 panic 击穿网关进程
	// （2026-09-14 审计 A-P2-2/A-P2-3）。
	lifecycleMu sync.Mutex
	started     bool
	workerDone  chan struct{}

	// probeNowAsync (optional, 2026-08-23 hzx-2): lets ForceProbe bypass
	// the 2-min tick by invoking CredentialProbeV2.ProbeNowAsync. Wired
	// from cmd/gateway/main.go after both workers are constructed.
	probeNowAsync func(credID int)

	// 2026-08-26 hzx-2 / 充值回调 webhook (落点 B):
	//
	// 由 cmd/gateway/webhooks.QuotaRechargedHandler 注入，HMAC 验签通过后
	// 异步调用 OnQuotaRecharged(credID, source)，把"用户已充值"信号从
	// 2 分钟 tick 提到秒级。回调内部走 credProbeV2.SubmitFastProbe /
	// ProbeNowAsync 两条路径，与 admin ForceProbe 复用同一套调度链。
	onQuotaRecharged func(credID int, source string)

	// forceCooldown caps how often the same credential can be
	// admin-forced within a single process. Operators can click
	// "Force probe" repeatedly; without this cap each click produces
	// an independent probe and floods fastReprobeQueue.
	forceCooldown time.Duration

	// forceMu guards forceLastSeen.
	forceMu       sync.Mutex
	forceLastSeen map[int]time.Time
}

func NewBalanceQuotaProbe(db *pgxpool.Pool) *BalanceQuotaProbe {
	// 默认 2 分钟，比周期性配额探测（5 分钟）更频繁
	// 原因：余额充值是用户主动操作，需要更快感知恢复
	interval := 2 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		}
	}
	forceCooldown := 30 * time.Second
	if v := os.Getenv("LLM_GATEWAY_BALANCE_QUOTA_FORCE_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			forceCooldown = d
		}
	}
	return &BalanceQuotaProbe{
		db:            db,
		interval:      interval,
		stopCh:        make(chan struct{}),
		forceCooldown: forceCooldown,
		forceLastSeen: make(map[int]time.Time),
	}
}

func (p *BalanceQuotaProbe) SetProbeSubmitter(fn func(credID int)) {
	p.probeSubmitter = fn
}

// SetProbeNowAsync wires the CredentialProbeV2.ProbeNowAsync hook so
// ForceProbe can bypass the 2-min tick. Optional — nil is safe and
// causes ForceProbe to fall back to the SubmitFastProbe path.
func (p *BalanceQuotaProbe) SetProbeNowAsync(fn func(credID int)) {
	p.probeNowAsync = fn
}

// SetOnQuotaRecharged wires the recharge-callback hook fired by
// cmd/gateway/webhooks.QuotaRechargedHandler. Optional — nil is safe
// (the webhook just becomes a 200-OK echo with no follow-up).
//
// 2026-08-26 hzx-2: webhook 注入后，秒级恢复链路成立；nil-safe 保证
// 单测 / 老启动路径不会 panic。
func (p *BalanceQuotaProbe) SetOnQuotaRecharged(fn func(credID int, source string)) {
	p.onQuotaRecharged = fn
}

// OnQuotaRecharged accepts a provider recharge signal and schedules exactly one
// re-check. It prefers the immediate probe path; the delayed queue is only a
// compatibility fallback when the immediate worker is unavailable. This avoids
// charging two upstream probes for one accepted webhook.
//
// credID <= 0 is ignored (same behavior as ForceProbe).
// Balance-floor-pulled credentials remain owned by the floor guard; a chat
// probe would incorrectly restore them while their balance evidence is stale.
func (p *BalanceQuotaProbe) OnQuotaRecharged(credID int, source string) {
	if credID <= 0 {
		return
	}
	if p.credentialFloorPulled(credID) {
		slog.Info("balance_quota_probe: recharge webhook ignored for balance_floor-pulled credential (guard owns recovery)", "credential_id", credID, "source", source)
		return
	}
	if p.probeNowAsync != nil {
		p.probeNowAsync(credID)
	} else if p.probeSubmitter != nil {
		p.probeSubmitter(credID)
	}
	if p.onQuotaRecharged != nil {
		p.onQuotaRecharged(credID, source)
	}
	slog.Info("balance_quota_probe: webhook-triggered probe dispatched",
		"credential_id", credID,
		"source", source,
		"mode", map[bool]string{true: "immediate", false: "delayed_fallback"}[p.probeNowAsync != nil])
}

func (p *BalanceQuotaProbe) Start(ctx context.Context) {
	p.lifecycleMu.Lock()
	if p.started {
		p.lifecycleMu.Unlock()
		return
	}
	p.started = true
	if p.workerDone == nil {
		p.workerDone = make(chan struct{})
	}
	p.lifecycleMu.Unlock()
	slog.Info("balance_quota_probe started",
		"interval", p.interval,
		"target_states", []string{"balance_exhausted", "permanently_exhausted"},
		"force_cooldown", p.forceCooldown,
	)
	go func() {
		defer close(p.workerDone)
		// 顶层 recover：tick 路径含 DB 扫描与结果分类，未捕获 panic 会
		// 直接终止整个网关进程（2026-09-14 审计 A-P2-2）。
		defer func() {
			if r := recover(); r != nil {
				slog.Error("balance_quota_probe panic", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("balance_quota_probe stopping")
				return
			case <-p.stopCh:
				slog.Info("balance_quota_probe stopped")
				return
			case <-ticker.C:
				p.runGuardedTick(ctx)
			}
		}
	}()
}

// runGuardedTick 隔离单次 tick 的 panic：单轮失败/异常不应终止后续调度。
func (p *BalanceQuotaProbe) runGuardedTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("balance_quota_probe tick panic", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := p.probeBalanceExhausted(ctx); err != nil {
		slog.Error("balance_quota_probe failed", "error", err)
	}
}

func (p *BalanceQuotaProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

// ForceProbe submits an immediate re-check for one credential. Intended
// for admin use right after a user reports they've topped up. Bypasses
// the 2-min tick so the recovery is visible to users within seconds.
//
// Returns true if the probe was actually enqueued (or executed). Returns
// false if:
//   - the credential was probed within the last forceCooldown (operator
//     is clicking too fast; the dedup is intentional);
//   - probeSubmitter is nil AND probeNowAsync is nil (worker not wired);
//   - the credential is not in a balance/permanent quota state (the
//     probe would no-op anyway).
//
// The function is safe to call concurrently — internal state is guarded
// by forceMu. The cooldown mark is released on every non-dispatch path
// so an operator who clicks during a transient state (eligibility
// changes mid-flight, DB error, worker un-wired) isn't locked out for
// the next forceCooldown seconds.
func (p *BalanceQuotaProbe) ForceProbe(credID int) bool {
	if credID <= 0 {
		return false
	}

	p.forceMu.Lock()
	now := time.Now()
	if last, ok := p.forceLastSeen[credID]; ok && now.Sub(last) < p.forceCooldown {
		p.forceMu.Unlock()
		slog.Info("balance_quota_probe: force probe suppressed by cooldown",
			"credential_id", credID,
			"since_last", now.Sub(last).String(),
			"cooldown", p.forceCooldown.String())
		return false
	}
	p.forceLastSeen[credID] = now
	mapLen := len(p.forceLastSeen)
	// Garbage-collect stale entries while holding the lock to avoid a
	// data race on the map (len() + iterate are both racy vs concurrent
	// writers otherwise). GC fires once the map exceeds 100 entries —
	// more than enough for any realistic operator session.
	if mapLen > 100 {
		for id, ts := range p.forceLastSeen {
			if now.Sub(ts) > p.forceCooldown*10 {
				delete(p.forceLastSeen, id)
			}
		}
	}
	p.forceMu.Unlock()

	// releaseCooldown removes the cooldown mark for credID. Called on
	// every path that doesn't dispatch a probe so an operator isn't
	// locked out for 30s after a transient-state click.
	releaseCooldown := func() {
		p.forceMu.Lock()
		delete(p.forceLastSeen, credID)
		p.forceMu.Unlock()
	}

	eligible, stateCheckErr := p.credentialEligibleForForceProbe(credID)
	if stateCheckErr != nil {
		slog.Warn("balance_quota_probe: force probe eligibility check failed",
			"credential_id", credID, "error", stateCheckErr)
		releaseCooldown()
		return false
	}
	if !eligible {
		// Credential isn't in a balance/permanent state; either it
		// recovered already or the operator clicked the wrong one.
		// Release the cooldown so the next click is honoured immediately
		// if state flips.
		releaseCooldown()
		return false
	}

	// Prefer ProbeNowAsync (immediate execution) over the fast queue
	// because the admin click is meant to surface results NOW. Fall
	// back to the standard fast queue when ProbeNowAsync isn't wired
	// (older test harness, partial boot).
	if p.probeNowAsync != nil {
		p.probeNowAsync(credID)
		slog.Info("balance_quota_probe: force probe dispatched via ProbeNowAsync",
			"credential_id", credID,
			"source", "admin_force")
	} else if p.probeSubmitter != nil {
		p.probeSubmitter(credID)
		slog.Info("balance_quota_probe: force probe dispatched via fast queue",
			"credential_id", credID,
			"source", "admin_force")
	} else {
		// Worker not wired — release the cooldown mark so the operator
		// can retry once the wiring is fixed.
		releaseCooldown()
		return false
	}

	return true
}

// credentialEligibleForForceProbe returns true when the credential is
// currently in a balance/permanent exhausted state, OR when no row was
// found (the credential already recovered between the operator's click
// and this check — treat as success so the admin endpoint returns a
// clean "already healthy" message).
//
// The second return value is the DB error from QueryRow.Scan if any
// other error occurred (timeout, connection failure, schema mismatch).
// Callers must distinguish "no rows = recovered" from "error = unknown"
// — silently conflating them with "eligible" would let the probe path
// be dispatched even during a DB outage, returning 202 Accepted while
// the upstream is unreachable.
func (p *BalanceQuotaProbe) credentialEligibleForForceProbe(credID int) (bool, error) {
	if p.db == nil {
		return false, errors.New("balance_quota_probe: db pool is nil")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var state string
	err := p.db.QueryRow(probeCtx, `
		SELECT quota_state
		FROM credentials
		WHERE id = $1
		  AND quota_state IN ('balance_exhausted', 'permanently_exhausted')
	`, credID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No matching row — credential already recovered. The
			// admin endpoint returns 200 with status="no_op"; the
			// probe path is not entered.
			return true, nil
		}
		return false, err
	}
	return state == "balance_exhausted" || state == "permanently_exhausted", nil
}

// probeBalanceExhausted 探测 balance_exhausted 和 permanently_exhausted 状态的凭据。
//
// 选择条件：
//   - quota_state IN ('balance_exhausted', 'permanently_exhausted')，
//     OR 挂起矛盾行（availability_state='suspended' 且 quota_state='ok'
//     且 recover_at=NULL —— 2026-09-13 closeout P3：这类行此前没有任何
//     探测来源，只能人工 force-enable；纳入慢速复验后，探测成功经
//     writeHealth 翻回 ready，为 availSQL 的 suspended 证据恢复分支提供
//     health 证据）
//   - lifecycle_status = 'active'
//   - 凭据和 provider 都未被手动禁用
//   - provider 已启用
//   - 有配置 default_probe_model，或可从 credential_model_bindings 中
//     选出至少一个可用探测模型（fallback）
//   - 2026-09-13 closeout P3 到期闸：last_probe_at 距今不足
//     quotaProbeBackoff(probe_consecutive_failures) 的行跳过，本轮不探。
//     原先每 2min 全量轰炸"没充值"的确定死亡凭据（720 次/天/凭据），
//     指数退避后稳态 ≤1 次/小时/凭据，充值后最迟 1h 自动恢复。
//
// 探测结果：
//   - 成功：credential_probe_v2 会清除 quota_state='ok'，恢复 availability_state='ready'
//   - 失败（仍然配额错误）：保持当前状态
//   - 失败（其他错误）：可能转为其他错误状态（由 probe-v2 的分类逻辑决定）
func (p *BalanceQuotaProbe) probeBalanceExhausted(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 2026-08-23 hzx-2 audit: the predicate no longer requires
	// COALESCE(c.default_probe_model, '') <> ''. The original guard
	// meant that operators who never set a default_probe_model
	// (common for newly-onboarded credentials) had their balance-
	// exhausted credentials silently skipped for the entire lifetime
	// of the exhausted state. The new guard requires EITHER a
	// configured default_probe_model OR a routable binding on the
	// credential — if neither is true the probe really cannot run.
	rows, err := p.db.Query(timeoutCtx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE (
		      c.quota_state IN ('balance_exhausted', 'permanently_exhausted')
		      OR (
		          -- suspended-revalidation set (P3): the contradictory
		          -- suspended+quota-ok+NULL-recover rows. Slower floor
		          -- cadence than the quota set via the backoff expression
		          -- below; see credential_recovery.go availSQL for the
		          -- matching evidence-based restore. Rows that ALREADY carry
		          -- fresh healthy evidence are excluded — availSQL restores
		          -- those within one 30s tick without another probe.
		          c.availability_state = 'suspended'
		          AND c.availability_recover_at IS NULL
		          AND COALESCE(c.quota_state, 'ok') = 'ok'
		          AND NOT (
		              c.health_status = 'healthy'
		              AND c.health_checked_at > now() - INTERVAL '2 hours'
		          )
		      )
		      OR (
		          -- 2026-09-13 audit round F4: auto-disabled revalidation. An
		          -- auto-disabled credential whose revalidation probe returns
		          -- 401/403 flips to auth_failed and would otherwise fall out
		          -- of every recovery set (this scan required 'suspended'; the
		          -- availSQL tick requires lifecycle='active') — stranded
		          -- again. Revalidate ANY auto-disabled, quota-ok, non-ready
		          -- row once its availability backoff has expired; writeHealth
		          -- flips it fully (ready + lifecycle active) on success.
		          c.lifecycle_status = 'disabled'
		          AND c.auto_disabled_at IS NOT NULL
		          AND COALESCE(c.quota_state, 'ok') = 'ok'
		          AND COALESCE(c.availability_state, 'ready') <> 'ready'
		          AND (c.availability_recover_at IS NULL OR c.availability_recover_at <= now())
		      )
		      )
  AND COALESCE(c.state_reason_code, '') <> 'balance_floor' -- balance_floor guard exemption
  AND c.status = 'active'
  -- 2026-09-13 closeout (P3): admit auto-disabled rows (auto_disabled_at
  -- set) — writeHealth already sanctions re-enabling exactly this class
  -- on a successful probe (auto_enabled_reason='periodic_quota_probe_
  -- recovered'). Prod evidence: ALL six suspended+quota-ok contradictory
  -- rows (creds 9/13/23/24/25/30) were auto-disabled by the availability
  -- <50% rule, then stranded — no probe target could ever reach them.
  AND (
      c.lifecycle_status = 'active'
      OR (
          c.lifecycle_status = 'disabled'
          AND c.auto_disabled_at IS NOT NULL
      )
  )
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  -- due gate (P3): exponential re-probe interval by consecutive
		  -- probe failures — 2min, 4min, 8min, 16min, 32min, then capped at
		  -- 1h. First failure stays on the 2min rung so a genuine transient
		  -- or a quick recharge is still noticed within one interval.
		  AND (
		      c.last_probe_at IS NULL
		      OR now() - c.last_probe_at >= LEAST(
		          INTERVAL '2 minutes' * POWER(2, LEAST(COALESCE(c.probe_consecutive_failures, 0), 5)),
		          INTERVAL '1 hour'
		      )
		  )
		  AND (
		      COALESCE(c.default_probe_model, '') <> ''
		      OR EXISTS (
		          SELECT 1
		          FROM credential_model_bindings cmb
		          JOIN provider_models pm ON pm.id = cmb.provider_model_id
		          WHERE cmb.credential_id = c.id
		            AND COALESCE(cmb.available, FALSE) = TRUE
		      )
		  )
		ORDER BY c.id
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var credID int
		if err := rows.Scan(&credID); err != nil {
			slog.Warn("balance_quota_probe: scan failed", "error", err)
			continue
		}
		if p.probeNowAsync != nil {
			p.probeNowAsync(credID)
		} else if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		count++
	}
	if count > 0 {
		slog.Info("balance_quota_probe: submitted probes for balance/permanently exhausted credentials",
			"count", count,
			"interval", p.interval,
			"reason", "detect recharge recovery faster",
		)
	}
	return nil
}

// credentialFloorPulled reports whether the balance-floor guard owns recovery.
// Fail open on database errors so a transient outage does not swallow webhooks.
func (p *BalanceQuotaProbe) credentialFloorPulled(credID int) bool {
	if p.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var reason string
	err := p.db.QueryRow(ctx, `
		SELECT COALESCE(state_reason_code, '')
		FROM credentials
		WHERE id = $1
		  AND quota_state = 'balance_exhausted'
		  AND COALESCE(state_reason_code, '') = 'balance_floor'
	`, credID).Scan(&reason)
	return err == nil && reason == "balance_floor"
}
