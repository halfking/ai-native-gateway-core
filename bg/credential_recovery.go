package bg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
	slog.Info("credential recovery task started", "interval", "60s")
}

func (r *CredentialRecovery) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *CredentialRecovery) run(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.recover(ctx)
		}
	}
}

func (r *CredentialRecovery) recover(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := r.db.Exec(timeoutCtx, `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_updated_at = now()
		WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed')
		  AND availability_recover_at IS NOT NULL
		  AND availability_recover_at <= now()
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
func stalePeriodicExhaustedCleanupSQL() string {
	return `
		UPDATE credentials
		SET quota_state         = 'ok',
		    quota_recover_at    = NULL,
		    state_reason_code   = NULL,
		    state_updated_at    = now()
		WHERE quota_state = 'periodic_exhausted'
		  AND health_status = 'healthy'
		  AND health_checked_at > now() - INTERVAL '2 hours'
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
		  AND cmb.unavailable_recover_at IS NOT NULL
		  AND cmb.unavailable_recover_at <= now()
		  AND (
		      cmb.unavailable_reason IN ('continuous_failure')
		      OR cmb.unavailable_reason LIKE 'probe_%'
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
		ORDER BY cmb.unavailable_recover_at ASC
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
