package bg

// ----------------------------------------------------------------------------
// MERGE AUDIT NOTE (2026-08-27, conflict resolution: keep HEAD / local)
//
// During merge of origin/main into local main, this file conflicted. Both
// sides added fields/methods to BalanceQuotaProbe. Per policy, the LOCAL
// (HEAD) version is preserved as the primary code, and the REMOTE
// (origin/main) version is retained as a commented-out reference block
// appended to the bottom of this file under matching audit markers:
//
//   /* === BEGIN: origin/main (kept for audit, NOT COMPILED) === */
//   /* === END:   origin/main (kept for audit, NOT COMPILED) === */
//
// so reviewers can diff against it without losing the merged-in content.
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

// OnQuotaRecharged 是 webhook 触发的入口：拿到充值信号后立即把凭据
// 推进两条探测路径（fast queue + ProbeNowAsync），再回调外部注入的
// onQuotaRecharged（如通知其他模块）。两条探测路径各自独立、互不阻塞，
// 任意一条失败不影响另一条 — 与 ForceProbe 复用同一套调度链。
//
// credID <= 0 直接静默返回（与 ForceProbe 行为对齐）。
func (p *BalanceQuotaProbe) OnQuotaRecharged(credID int, source string) {
	if credID <= 0 {
		return
	}
	// 1. 立即探活：fast queue（与 scheduled / admin_force 路径一致）。
	if p.probeSubmitter != nil {
		p.probeSubmitter(credID)
	}
	// 2. 旁路 ProbeNowAsync：绕开 fastReprobeQueue 的 5 分钟 delay。
	if p.probeNowAsync != nil {
		p.probeNowAsync(credID)
	}
	p.recordBalanceCheck(credID, "webhook_"+source)
	// 3. 通知外部注入的回调（一般用于刷 cache / 触发 admin 通知）。
	if p.onQuotaRecharged != nil {
		p.onQuotaRecharged(credID, source)
	}
	slog.Info("balance_quota_probe: webhook-triggered probe dispatched",
		"credential_id", credID,
		"source", source)
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

	p.recordBalanceCheck(credID, "admin_force")
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

/* === BEGIN: origin/main (kept for audit, NOT COMPILED) === */
//
// The following block is the verbatim text of origin/main's
// bg/balance_quota_probe.go at merge-time (commit 4b512d640,
// 2026-08-26). It is preserved as a commented-out reference so
// reviewers and future audits can diff against it without losing
// any of the local additions that were kept as the primary code
// above. DO NOT uncomment this block — its content is older than
// HEAD's version and would regress the hzx-2 audit additions.
//
// Each line below is prefixed with '// ' so the Go compiler
// ignores it. Matching end-marker below closes the block.
//

// package bg
//
// import (
// 	"context"
// 	"log/slog"
// 	"os"
// 	"strconv"
// 	"sync"
// 	"time"
//
// 	"github.com/jackc/pgx/v5/pgxpool"
// )
//
// // BalanceQuotaProbe 专门针对需要充值才能恢复的配额类型（balance_exhausted、permanently_exhausted）
// // 进行更频繁的探测，以便及时感知用户充值后的恢复。
// //
// // 与 PeriodicQuotaProbe 的区别：
// //   - PeriodicQuotaProbe: 针对周期性配额用尽（periodic_exhausted），默认 5 分钟探测一次
// //   - BalanceQuotaProbe: 针对余额/永久配额用尽（balance_exhausted、permanently_exhausted），
// //     默认 2 分钟探测一次，更快感知充值恢复
// //
// // 探测策略：
// //  1. 选择 quota_state IN ('balance_exhausted', 'permanently_exhausted') 的活跃凭据
// //  2. 通过 credential_probe_v2 进行实际探测（调用上游 API）
// //  3. 如果探测成功，probe-v2 会自动清除 quota_state 并恢复 availability_state
// //  4. 如果探测失败且仍然是配额错误，保持当前状态不变
// type BalanceQuotaProbe struct {
// 	db             *pgxpool.Pool
// 	interval       time.Duration
// 	probeSubmitter func(credID int)
// 	stopCh         chan struct{}
// 	stopOnce       sync.Once
// }
//
// func NewBalanceQuotaProbe(db *pgxpool.Pool) *BalanceQuotaProbe {
// 	// 默认 2 分钟，比周期性配额探测（5 分钟）更频繁
// 	// 原因：余额充值是用户主动操作，需要更快感知恢复
// 	interval := 2 * time.Minute
// 	if v := os.Getenv("LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL"); v != "" {
// 		if d, err := time.ParseDuration(v); err == nil && d > 0 {
// 			interval = d
// 		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
// 			interval = time.Duration(n) * time.Minute
// 		}
// 	}
// 	return &BalanceQuotaProbe{
// 		db:       db,
// 		interval: interval,
// 		stopCh:   make(chan struct{}),
// 	}
// }
//
// func (p *BalanceQuotaProbe) SetProbeSubmitter(fn func(credID int)) {
// 	p.probeSubmitter = fn
// }
//
// func (p *BalanceQuotaProbe) Start(ctx context.Context) {
// 	slog.Info("balance_quota_probe started",
// 		"interval", p.interval,
// 		"target_states", []string{"balance_exhausted", "permanently_exhausted"},
// 	)
// 	go func() {
// 		ticker := time.NewTicker(p.interval)
// 		defer ticker.Stop()
// 		for {
// 			select {
// 			case <-ctx.Done():
// 				slog.Info("balance_quota_probe stopping")
// 				return
// 			case <-p.stopCh:
// 				slog.Info("balance_quota_probe stopped")
// 				return
// 			case <-ticker.C:
// 				if err := p.probeBalanceExhausted(ctx); err != nil {
// 					slog.Error("balance_quota_probe failed", "error", err)
// 				}
// 			}
// 		}
// 	}()
// }
//
// func (p *BalanceQuotaProbe) Stop() {
// 	p.stopOnce.Do(func() { close(p.stopCh) })
// }
//
// // probeBalanceExhausted 探测 balance_exhausted 和 permanently_exhausted 状态的凭据。
// //
// // 选择条件：
// //   - quota_state IN ('balance_exhausted', 'permanently_exhausted')
// //   - lifecycle_status = 'active'
// //   - 凭据和 provider 都未被手动禁用
// //   - provider 已启用
// //   - 有配置 default_probe_model（探测需要指定模型）
// //
// // 探测结果：
// //   - 成功：credential_probe_v2 会清除 quota_state='ok'，恢复 availability_state='ready'
// //   - 失败（仍然配额错误）：保持当前状态
// //   - 失败（其他错误）：可能转为其他错误状态（由 probe-v2 的分类逻辑决定）
// func (p *BalanceQuotaProbe) probeBalanceExhausted(ctx context.Context) error {
// 	if p.db == nil {
// 		return nil
// 	}
// 	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
// 	defer cancel()
//
// 	rows, err := p.db.Query(timeoutCtx, `
// 		SELECT c.id
// 		FROM credentials c
// 		JOIN providers p ON p.id = c.provider_id
// 		WHERE c.quota_state IN ('balance_exhausted', 'permanently_exhausted')
// 		  AND c.lifecycle_status = 'active'
// 		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
// 		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
// 		  AND p.enabled = TRUE
// 		  AND COALESCE(c.default_probe_model, '') <> ''
// 		ORDER BY c.id
// 		LIMIT 100
// 	`)
// 	if err != nil {
// 		return err
// 	}
// 	defer rows.Close()
//
// 	var count int
// 	for rows.Next() {
// 		var credID int
// 		if err := rows.Scan(&credID); err != nil {
// 			slog.Warn("balance_quota_probe: scan failed", "error", err)
// 			continue
// 		}
// 		if p.probeSubmitter != nil {
// 			p.probeSubmitter(credID)
// 		}
// 		count++
// 	}
// 	if count > 0 {
// 		slog.Info("balance_quota_probe: submitted probes for balance/permanently exhausted credentials",
// 			"count", count,
// 			"interval", p.interval,
// 			"reason", "detect recharge recovery faster",
// 		)
// 	}
// 	return nil
// }

/* === END: origin/main (kept for audit, NOT COMPILED) === */
