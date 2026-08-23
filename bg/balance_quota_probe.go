package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

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

	// probeNowAsync (optional, 2026-08-23 hzx-2): lets ForceProbe bypass
	// the 2-min tick by invoking CredentialProbeV2.ProbeNowAsync. Wired
	// from cmd/gateway/main.go after both workers are constructed.
	probeNowAsync func(credID int)

	// forceCooldown caps how often the same credential can be
	// admin-forced within a single process. Operators can click
	// "Force probe" repeatedly; without this cap each click produces
	// an independent probe and floods fastReprobeQueue.
	forceCooldown time.Duration

	// forceMu guards forceLastSeen.
	forceMu      sync.Mutex
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

func (p *BalanceQuotaProbe) Start(ctx context.Context) {
	slog.Info("balance_quota_probe started",
		"interval", p.interval,
		"target_states", []string{"balance_exhausted", "permanently_exhausted"},
		"force_cooldown", p.forceCooldown,
	)
	go func() {
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
				if err := p.probeBalanceExhausted(ctx); err != nil {
					slog.Error("balance_quota_probe failed", "error", err)
				}
			}
		}
	}()
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
// by forceMu.
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
	p.forceMu.Unlock()

	// Garbage-collect stale entries so the map doesn't grow unbounded
	// across a long-running gateway. 100 entries is more than enough
	// for any realistic operator session.
	if len(p.forceLastSeen) > 100 {
		p.gcForceHistory(now)
	}

	if !p.credentialEligibleForForceProbe(credID) {
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
		p.forceMu.Lock()
		delete(p.forceLastSeen, credID)
		p.forceMu.Unlock()
		return false
	}

	p.recordBalanceCheck(credID, "admin_force")
	return true
}

func (p *BalanceQuotaProbe) gcForceHistory(now time.Time) {
	p.forceMu.Lock()
	defer p.forceMu.Unlock()
	for id, ts := range p.forceLastSeen {
		if now.Sub(ts) > p.forceCooldown*10 {
			delete(p.forceLastSeen, id)
		}
	}
}

// credentialEligibleForForceProbe returns true when the credential is
// currently in a balance/permanent exhausted state. The check is best-
// effort — between the SELECT and the probe submission the state may
// have flipped (e.g. another instance's tick already recovered it), in
// which case the probe just no-ops inside credential_probe_v2.
func (p *BalanceQuotaProbe) credentialEligibleForForceProbe(credID int) bool {
	if p.db == nil {
		return false
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
		// no rows → state already recovered; treat as success so the
		// admin endpoint returns a clean "already healthy" message
		// rather than 503.
		return true
	}
	return state == "balance_exhausted" || state == "permanently_exhausted"
}

// probeBalanceExhausted 探测 balance_exhausted 和 permanently_exhausted 状态的凭据。
//
// 选择条件：
//   - quota_state IN ('balance_exhausted', 'permanently_exhausted')
//   - lifecycle_status = 'active'
//   - 凭据和 provider 都未被手动禁用
//   - provider 已启用
//   - 有配置 default_probe_model，或可从 credential_model_bindings 中
//     选出至少一个可用探测模型（fallback）
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
		WHERE c.quota_state IN ('balance_exhausted', 'permanently_exhausted')
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
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
		if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		p.recordBalanceCheck(credID, "scheduled")
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

// recordBalanceCheck stamps credentials.balance_last_checked_at so
// dashboards can show "last balance check" latency. Errors are logged
// but never block the probe — the timestamp is observability, not
// correctness.
func (p *BalanceQuotaProbe) recordBalanceCheck(credID int, source string) {
	if p.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tag, err := p.db.Exec(ctx, `
		UPDATE credentials
		SET balance_last_checked_at = now(),
		    state_updated_at        = now()
		WHERE id = $1
	`, credID)
	if err != nil {
		slog.Debug("balance_quota_probe: balance_last_checked_at update failed",
			"credential_id", credID, "source", source, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		slog.Debug("balance_quota_probe: balance_last_checked_at update affected 0 rows",
			"credential_id", credID, "source", source)
	}
}
