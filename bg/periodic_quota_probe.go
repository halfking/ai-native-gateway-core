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

// PeriodicQuotaProbe handles periodic_exhausted credential self-checks.
//
// 2026-08-23 hzx-2 audit: extend the original 5-min window-end-only path
// with a pre-probe phase and a recover_at deviation guard so periodic
// quotas don't dead-lock when the writer's recover_at hint misses the
// actual upstream window boundary.
//
// 2026-08-31 hzx-2 round-4 audit:
//   - Add the default_probe_model fallback already present in
//     BalanceQuotaProbe (credential_model_bindings availability). Without
//     it, credentials whose operator never set default_probe_model were
//     silently skipped by every layer (pre-probe, post-expiry, ProbeNow).
//   - Round-6 correction: the deviation guard's threshold stays at
//     max(recoverAtMaxLinger, 5h) (NOT tightened) and its clamp target
//     is the next 5h grid boundary — an earlier draft that clamped to
//     now()+30min re-introduced the 2026-08-08 probe-success death loop
//     for correctly classified 5h credentials. See
//     recoverAtDeviationGuard for the full rationale.
//
// Layered strategy:
//
//  1. PRE-PROBE  — credentials whose quota_recover_at is within
//     LLM_GATEWAY_PERIODIC_QUOTA_PRE_PROBE_WINDOW (default 60s) of now.
//     Probing 60s before the boundary lets the gateway resume within
//     one tick of the upstream window rolling over, instead of waiting
//     for the next 5-min cycle to see the expired row. The probe-v2
//     fast queue dedup key ensures multiple pre-probes collapse to one.
//
//  2. POST-EXPIRY — credentials whose quota_recover_at has passed (the
//     legacy path). The 30s credential_recovery tick flips quota_state
//     from periodic_exhausted → ok; PeriodicQuotaProbe then enqueues
//     a fast probe so the binding is verified end-to-end.
//
//  3. RECOVER_AT DEVIATION GUARD — fallback to next UTC midnight
//     (domains/credential/writer.go:inferQuotaRecoverAt) is wrong for
//     5h windows when the writer's regex hint misses. When
//     quota_recover_at - now() exceeds max(recoverAtMaxLinger, 5h), we
//     clamp it back to the next 5h grid boundary (UTC+8 00/05/10/15/20)
//     so the pre-probe path fires within the configured window at the
//     row's TRUE reset time — no early clear (death-loop safe), no extra
//     5h strand. Weekly/monthly-worded rows are excluded from clamping.
//
// Cost: pre-probe + clamp both run on the existing 5-min tick. DB load
// is bounded by the existing 100-row LIMIT on each SELECT. Upstream
// probe traffic is bounded by fastReprobeQueue's 64-slot dedup window.
type PeriodicQuotaProbe struct {
	db             *pgxpool.Pool
	interval       time.Duration
	probeSubmitter func(credID int)
	stopCh         chan struct{}
	stopOnce       sync.Once
	lifecycleMu    sync.Mutex
	started        bool
	stopped        bool
	workerDone     chan struct{}

	// preProbeWindow is how close to quota_recover_at we trigger the
	// pre-probe. Defaults to 60s. Override via env for tests / tuning.
	preProbeWindow time.Duration

	// recoverAtMaxLinger is the safety net for misclassified
	// quota_recover_at values: anything more than this many minutes
	// past "the obvious next 5h boundary" gets clamped to that
	// boundary so the pre-probe + post-expiry paths have a real
	// deadline to act on. Defaults to 30 minutes.
	recoverAtMaxLinger time.Duration
}

func NewPeriodicQuotaProbe(db *pgxpool.Pool) *PeriodicQuotaProbe {
	interval := 5 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_PERIODIC_QUOTA_PROBE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		}
	}
	preProbeWindow := 60 * time.Second
	if v := os.Getenv("LLM_GATEWAY_PERIODIC_QUOTA_PRE_PROBE_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			preProbeWindow = d
		}
	}
	recoverAtMaxLinger := 30 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_PERIODIC_QUOTA_RECOVER_AT_MAX_LINGER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			recoverAtMaxLinger = d
		}
	}
	return &PeriodicQuotaProbe{
		db:                 db,
		interval:           interval,
		stopCh:             make(chan struct{}),
		preProbeWindow:     preProbeWindow,
		recoverAtMaxLinger: recoverAtMaxLinger,
		workerDone:         make(chan struct{}),
	}
}
func (p *PeriodicQuotaProbe) SetProbeSubmitter(fn func(credID int)) {
	p.probeSubmitter = fn
}

func (p *PeriodicQuotaProbe) Start(ctx context.Context) {
	p.lifecycleMu.Lock()
	if p.started || p.stopped {
		p.lifecycleMu.Unlock()
		return
	}
	if p.stopCh == nil {
		p.stopCh = make(chan struct{})
	}
	if p.workerDone == nil {
		p.workerDone = make(chan struct{})
	}
	p.started = true
	p.lifecycleMu.Unlock()
	slog.Info("periodic_quota_probe started",
		"interval", p.interval,
		"pre_probe_window", p.preProbeWindow,
		"recover_at_max_linger", p.recoverAtMaxLinger)
	go func() {
		defer close(p.workerDone)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("periodic_quota_probe stopping")
				return
			case <-p.stopCh:
				slog.Info("periodic_quota_probe stopped")
				return
			case <-ticker.C:
				if err := p.tick(ctx); err != nil {
					slog.Error("periodic_quota_probe failed", "error", err)
				}
			}
		}
	}()
}

func (p *PeriodicQuotaProbe) Stop() {
	p.lifecycleMu.Lock()
	if !p.started || p.stopped {
		p.lifecycleMu.Unlock()
		return
	}
	p.stopped = true
	p.lifecycleMu.Unlock()
	p.stopOnce.Do(func() { close(p.stopCh) })
	<-p.workerDone
}

// tick runs the three layered checks: deviation guard, pre-probe, then
// post-expiry. Order matters — the deviation guard fixes recover_at
// values that the pre-probe path would otherwise ignore.
func (p *PeriodicQuotaProbe) tick(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := p.recoverAtDeviationGuard(timeoutCtx); err != nil {
		slog.Warn("periodic_quota_probe: recover_at deviation guard failed", "error", err)
	}
	preCount, preErr := p.probePreExhausted(timeoutCtx)
	if preErr != nil {
		slog.Warn("periodic_quota_probe: pre-probe failed", "error", preErr)
	}
	postCount, postErr := p.probePeriodicExhausted(timeoutCtx)
	if postErr != nil {
		return postErr
	}
	if preCount > 0 || postCount > 0 {
		slog.Info("periodic_quota_probe: submitted probes for periodic_exhausted credentials",
			"pre_probe_count", preCount,
			"post_expiry_count", postCount,
			"interval", p.interval)
	}
	return nil
}

// probePeriodicExhausted is the legacy path: probe credentials whose
// quota_recover_at has already elapsed. The credential_recovery 30s
// tick has already cleared quota_state from periodic_exhausted → ok by
// this point, so this scan picks up the residual rows that survived
// stale-periodic cleanup (e.g. when health_status wasn't ready in time).
//
// 2026-08-31 hzx-2 round-4: the default_probe_model guard was relaxed
// to mirror BalanceQuotaProbe's binding-availability fallback. Without
// it, operators who never set default_probe_model (common for newly
// onboarded credentials like hzx-2) saw their periodic_exhausted
// credentials silently skipped for the entire lifetime of the exhausted
// state. The probe-v2 worker still honours default_probe_model — this
// fallback only affects the SELECT predicate that decides whether the
// credential is probeable in the first place.
func (p *PeriodicQuotaProbe) probePeriodicExhausted(ctx context.Context) (int, error) {
	if p.db == nil {
		return 0, nil
	}
	rows, err := p.db.Query(ctx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.quota_state = 'periodic_exhausted'
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND (c.quota_recover_at IS NULL OR c.quota_recover_at <= now())
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
		LIMIT 100
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var credID int
		if err := rows.Scan(&credID); err != nil {
			slog.Warn("periodic_quota_probe: scan failed", "error", err)
			continue
		}
		if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		count++
	}
	return count, nil
}

// probePreExhausted proactively probes credentials whose quota_recover_at
// is about to elapse. Without this path, a 5h-window credential whose
// recover_at was just missed by the writer would have to wait the full
// 5min cycle after expiration to be probed — visible to operators as a
// "stuck on 5h boundary" delay.
//
// We deliberately exclude rows already covered by the post-expiry path
// (`quota_recover_at <= now()`) to keep both queries idempotent against
// the fastReprobeQueue dedup window.
//
// 2026-08-31 hzx-2 round-4: same default_probe_model fallback as the
// post-expiry path.
func (p *PeriodicQuotaProbe) probePreExhausted(ctx context.Context) (int, error) {
	if p.db == nil {
		return 0, nil
	}
	rows, err := p.db.Query(ctx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.quota_state = 'periodic_exhausted'
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND c.quota_recover_at IS NOT NULL
		  AND c.quota_recover_at > now()
		  AND c.quota_recover_at <= now() + make_interval(secs => $1)
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
		LIMIT 100
	`, periodSecondsParam(p.preProbeWindow))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var credID int
		if err := rows.Scan(&credID); err != nil {
			slog.Warn("periodic_quota_probe: pre-probe scan failed", "error", err)
			continue
		}
		if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		count++
	}
	return count, nil
}

// recoverAtDeviationGuard fixes quota_recover_at values that drifted
// past the obvious next 5h boundary. The root cause is in
// domains/credential/writer.go:inferQuotaRecoverAt — when the upstream
// error body matches none of the timestamp / 5h / week / month
// heuristics, the function falls back to "next UTC midnight". For a
// 5h-window credential this can land 0-24h past the actual reset,
// which means PeriodicQuotaProbe's pre-probe window never triggers
// and the post-expiry path waits the full residual interval.
//
// 2026-08-31 hzx-2 round-4 audit + round-6 correction:
//
//   - Threshold (deviant detection) is `quota_recover_at > now() +
//     max(recoverAtMaxLinger, 5h)`. The 5h floor is load-bearing: a
//     CORRECTLY classified 5h-window row has recover_at = the next
//     grid boundary, which is up to 5h away. A tighter threshold would
//     clamp those rows too, and clamping a correctly-classified row
//     short re-introduces the 2026-08-08 death loop
//     (credential_recovery.go stalePeriodicExhaustedCleanupSQL guard):
//     probe(probe_model) succeeds because the probe model does not
//     consume the business model's quota → quota_state cleared early →
//     routing re-admits → business 429 → writer re-suspends → loop.
//     An earlier version of this fix used max(preProbeWindow,
//     recoverAtMaxLinger)=30min as BOTH threshold and target — that
//     clamped every healthy 5h credential into a ~35-minute loop and
//     is exactly why the threshold must stay ≥ the full window.
//
//   - Target (clamp value) is the next 5h grid boundary in UTC+8
//     (00/05/10/15/20 北京时间), computed in Go and passed as $2 — NOT
//     now()+X. The legacy `now() + INTERVAL '5 hours'` target added a
//     full extra window of delay to a misclassified row whose actual
//     reset sits at the grid boundary. Aligning to the grid means a
//     misclassified 5h row recovers at its true boundary: no early
//     clear (loop-safe) and no extra 5h strand (the hzx-2 complaint).
//
//   - Weekly/monthly rows are EXCLUDED from clamping via the
//     state_reason_detail token filter. Their legitimately long
//     recover_at (next Monday / next month 1st) always exceeds the 5h
//     threshold, so without the exclusion the guard would compress a
//     7-day window into a 5h loop — a pre-existing defect in the
//     legacy guard that this correction also closes.
func (p *PeriodicQuotaProbe) recoverAtDeviationGuard(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	// Threshold: anything with more than one full 5h window of runway
	// is deviant. Operator-tunable upward via recoverAtMaxLinger; the
	// 5h floor is non-negotiable for the death-loop reason above.
	lingerSeconds := int(p.recoverAtMaxLinger.Seconds())
	if fiveHourSeconds := int((5 * time.Hour).Seconds()); lingerSeconds < fiveHourSeconds {
		lingerSeconds = fiveHourSeconds
	}
	// Target: next 5h grid boundary in UTC+8. Always ≤ now+5h, so every
	// deviant row (recover_at > now+5h) strictly improves (shrinks).
	boundary := nextFiveHourBoundaryCST(time.Now())

	tag, err := p.db.Exec(ctx, `
		UPDATE credentials c
		SET quota_recover_at = $2,
		    state_updated_at = now()
		FROM providers p
		WHERE p.id = c.provider_id
		  AND c.quota_state = 'periodic_exhausted'
		  AND c.quota_recover_at IS NOT NULL
		  AND c.quota_recover_at > now() + make_interval(secs => $1)
		  -- Never extend: only shrink a deadline that sits past the
		  -- boundary (defensive; the threshold above already implies
		  -- recover_at > $2 for every matched row).
		  AND c.quota_recover_at > $2
		  -- Exclude legitimately long windows so the guard cannot
		  -- compress a weekly/monthly reset into a 5h re-probe loop.
		  -- Mirrors the tokens domains/credential/writer.go uses to
		  -- classify those windows in the first place.
		  AND COALESCE(lower(c.state_reason_detail), '') NOT LIKE '%week%'
		  AND COALESCE(lower(c.state_reason_detail), '') NOT LIKE '%month%'
		  AND COALESCE(c.state_reason_detail, '') NOT LIKE '%周%'
		  AND COALESCE(c.state_reason_detail, '') NOT LIKE '%月%'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
	`, lingerSeconds, boundary)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Info("periodic_quota_probe: clamped deviant quota_recover_at values",
			"count", tag.RowsAffected(),
			"linger_seconds", lingerSeconds,
			"clamped_to", boundary.Format(time.RFC3339),
			"reason", "next_5h_boundary_grid_cap")
	}
	return nil
}

// nextFiveHourBoundaryCST returns the first 5-hour mark (00/05/10/15/20
// in UTC+8) strictly after now. Mirrors
// domains/credential/writer.go:nextFiveHourBoundary (unexported there,
// so duplicated here with this pointer instead of exporting a domain
// symbol from bg). Keep the two in sync — the deviation guard must aim
// at the same grid the writer classifies against.
func nextFiveHourBoundaryCST(now time.Time) time.Time {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	nextHour := 5 * (local.Hour()/5 + 1)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cst)
	if nextHour >= 24 {
		nextHour -= 24
		day = day.AddDate(0, 0, 1)
	}
	return day.Add(time.Duration(nextHour) * time.Hour)
}

// periodSecondsParam renders a duration as the integer-seconds
// literal accepted by the pre-probe SELECT. Centralised so the
// formatting rule lives in one place; the cast to interval happens
// inside SQL (`$1::interval`).
func periodSecondsParam(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds()))
}
