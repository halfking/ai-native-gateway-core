// Package bg — model_probe.go
//
// ModelProbeRunner uses a CONSENSUS strategy with exponential backoff to
// flip credential×model bindings back to routable.
//
// State machine (per credential × model, stored in model_probe_state):
//
//	unknown  ─┐
//	          │  first failure observed by the worker → 'recovering'
//	recovering  ◀──┘
//	   │  consecutive successes → +1; backoff resets to 1m
//	   │  consecutive failures  → reset successes, +1 failure, longer backoff
//	   ↓
//	healthy_confirmed    (3 consecutive ok)  ← state flips here
//	   │  any subsequent failure → back to 'recovering'
//	   ↓
//	broken_confirmed     (3 consecutive fail) ← stops probing
//
// Backoff schedule (Go-based exponential backoff via probe_backoff.go):
//
//	consecutive_failures = 0 → 2h (healthy watchdog)
//	consecutive_failures = 1 → 5m (base)
//	consecutive_failures = 2 → 10m (base × 2)
//	consecutive_failures = 3 → 20m (base × 4)
//	consecutive_failures = 4 → 40m (base × 8)
//	consecutive_failures ≥ 5 → 80m (base × 16, capped at 2h)
//
// Hot-reloadable via probe.backoff_* settings.
//
// CRITICAL invariant: a model that's manually disabled NEVER gets
// auto-recovered.  The runner re-checks c.manual_disabled on every
// iteration and the SQL WHERE clause filters out manual bindings.
//
// Spec: 2026-06-18-model-probe-rounds (v2: consensus + backoff)
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// RequiredConsensus is the number of consecutive successes (or
	// failures) needed before state flips.  Tuned by ops; do not
	// lower without re-reading spec §v2.
	RequiredConsensus = 3

	// MaxBatchPerCycle caps the number of probes per tick so a flood
	// of failures can't hammer the upstream all at once.
	MaxBatchPerCycle = 20

	// 2026-07-13 fix: 探测周期从 10s 改为 5min。
	//
	// 之前 ProbeInterval = 10s 导致：
	//   - 154 上观察到每分钟 21 次 scheduler 触发（正常应该是 5min 1 次）
	//   - model_probe_backoff_v2 最小值 10s（1次失败后），
	//     与 10s tick 完美重叠 → 失败后立即被重新探测
	//   - applyPassiveBoosts 把 next_retry_at 推到 30s，
	//     但 cycle 10s 一次 → 仍立即触发
	//
	// 新值 5min 与 healthy_confirmed watchdog（2h）兼容：
	//   - 正常 healthy 凭据 2h 才探测一次
	//   - 失败时按 backoff 10s/30s/1m/2m/5m 间隔探测
	//   - cycle 5min 一次不会与 1m/2m/5m backoff 冲突
	ProbeInterval = 5 * time.Minute
)

// ErrCredentialManuallyDisabled is returned by TriggerManual when the target
// binding exists but its credential or provider is manually disabled, or the
// credential is no longer active. Callers should treat this as an expected
// guard outcome rather than an unexpected SQL error.
//
// 2026-08-13 audit: TriggerManual must short-circuit at SQL time so a disabled
// target is never decrypted or probed upstream.
var ErrCredentialManuallyDisabled = errors.New("manual probe target is disabled")

// ModelProbeRunner is the v2 (consensus + backoff) implementation.
type ModelProbeRunner struct {
	db      *pgxpool.Pool
	encKey  []byte
	keyring *secret.Keyring
	cache   *ModelAvailabilityCache
	cancel  context.CancelFunc
	done    chan struct{}
	// featuredCancel is set by StartFeaturedOnly so Stop can cancel the
	// standalone 常用模型 deep-ping cycle independently of the consensus loop.
	// atomic.Pointer so Stop can safely read it during/after Start (audit #9).
	featuredCancel atomic.Pointer[context.CancelFunc]
	// closeOnce ensures `done` is closed exactly once whether the closer is the
	// consensus loop (run) or the featured-only cycle (StartFeaturedOnly).
	closeOnce sync.Once
	// manualProbeQueue holds async manual probe requests submitted via
	// SubmitManualProbe. Worker goroutine consumes tasks and executes
	// TriggerManual synchronously in background (2026-08-14).
	manualProbeQueue chan manualProbeTask
	manualProbeWG    sync.WaitGroup
}

type manualProbeTask struct {
	CredentialID int
	RawModel     string
}

func NewModelProbeRunner(db *pgxpool.Pool, encKey []byte) *ModelProbeRunner {
	return &ModelProbeRunner{
		db:               db,
		encKey:           encKey,
		done:             make(chan struct{}),
		manualProbeQueue: make(chan manualProbeTask, 64),
	}
}

func (r *ModelProbeRunner) SetKeyring(kr *secret.Keyring) { r.keyring = kr }

func (r *ModelProbeRunner) SetAvailabilityCache(cache *ModelAvailabilityCache) {
	r.cache = cache
}

func (r *ModelProbeRunner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	// Layer 4: featured model deep ping every 30 minutes (v5, 2026-06-20)
	go r.featuredCycleLoop(ctx)
	// Layer 5: manual probe worker (2026-08-14)
	go r.manualProbeWorker(ctx)
	slog.Info("model probe runner v2 (consensus+backoff) started",
		"interval", ProbeInterval,
		"required_consensus", RequiredConsensus,
		"max_batch", MaxBatchPerCycle,
	)
}

// StartFeaturedOnly launches ONLY the 常用模型 deep-ping cycle (no consensus
// loop). Used when LLM_GATEWAY_USE_NEW_PROBE_MODE=true (the default): the new
// CredentialSelfcheckWorker + NodeProbeWorker own consensus/error probes, but
// neither runs a frequent deep ping for HEALTHY 常用 models — so the featured
// cycle is the "强化自检" lever (需求: 常用模型加强自检). It only probes models
// matched by globalIsFeaturedModel and writes model_probe_runs (read-only w.r.t.
// state unless the consensus cycle also runs), so it is safe to run standalone.
func (r *ModelProbeRunner) StartFeaturedOnly(ctx context.Context) {
	fctx, cancel := context.WithCancel(ctx)
	r.featuredCancel.Store(&cancel)
	go func() {
		defer r.closeOnce.Do(func() { close(r.done) })
		r.featuredCycleLoop(fctx)
	}()
	// Audit fix #2: in new mode the consensus cycle is OFF, so the
	// nonfeatured watchdog multiplier (applyResult) never runs. Add a
	// lightweight watchdog loop that only EXTENDS next_retry_at for healthy
	// non-featured bindings — no HTTP probe, just timestamp arithmetic. This
	// keeps "其它模型降频" honest in the default new mode.
	go r.nonfeaturedWatchdogLoop(fctx)
	slog.Info("model probe featured-only cycle (常用模型 deep ping) + nonfeatured watchdog started")
}

// nonfeaturedWatchdogLoop extends next_retry_at on healthy_confirmed bindings
// whose raw_model is NOT 常用, multiplying the existing interval by
// probe.nonfeatured_watchdog_multiplier (default 4). 30-min cadence keeps it
// cheap; only touches model_probe_state, never calls out to providers.
func (r *ModelProbeRunner) nonfeaturedWatchdogLoop(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("nonfeatured watchdog panic", "recover", rec)
		}
	}()
	// Initial 1-min stagger so it runs after the first featured tick.
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
	}
	r.nonfeaturedWatchdogTick(ctx)
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.nonfeaturedWatchdogTick(ctx)
		}
	}
}

func (r *ModelProbeRunner) nonfeaturedWatchdogTick(ctx context.Context) {
	mult := settings.GetPlatformInt("probe.nonfeatured_watchdog_multiplier", 4)
	if mult <= 1 {
		return
	}
	windowHours := settings.GetPlatformInt("probe.featured_usage_window_hours", 168)
	topN := settings.GetPlatformInt("probe.featured_usage_top_n", 20)
	if topN <= 0 {
		topN = 0 // only static featured applies when kill-switch is on
	}
	// Tenant for routing_policy lookup (same as ModelTier.refresh, audit #H).
	tenant := settings.GetPlatformString("probe.featured_tenant", "default")

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Derive the target next_retry_at: NOW() + mult × LoadProbeBackoffConfig().NextDelay(0).
	// NextDelay(0) is the healthy-confirmed baseline (MaxDelay ≈ 2h). The operation
	// is IDEMPOTENT: we only extend rows whose next_retry_at is LESS than the target,
	// preventing unbounded drift on repeated 30-min ticks (audit #H additive issue).
	// model_probe_state has no provider_model_id; identity is (credential_id, raw_model_name).
	// We filter non-featured models by comparing raw_model_name directly against the
	// static + usage sets (audit BLOCKER join fix).
	baseSecs := int(LoadProbeBackoffConfig().NextDelay(0).Seconds())
	if baseSecs <= 0 {
		baseSecs = 7200 // fallback: 2h
	}
	targetSecs := mult * baseSecs

	tag, err := r.db.Exec(ctx, `
			WITH static AS (
			    SELECT lower(unnest(COALESCE(
			        (SELECT featured_models FROM routing_policy WHERE tenant_id = $1 LIMIT 1),
			        ARRAY[]::TEXT[]
			    ))) AS model
			), usage AS (
			    SELECT lower(raw_model) AS raw_model FROM (
			        SELECT COALESCE(rl.outbound_model, rl.client_model) AS raw_model,
			               count(*) AS calls
			        FROM request_logs_hot rl
			        WHERE rl.success
			          AND rl.ts > now() - make_interval(hours => $2)
			          AND COALESCE(rl.outbound_model, rl.client_model) <> ''
			        GROUP BY raw_model
			    ) t
			    ORDER BY calls DESC LIMIT $3
			)
			UPDATE model_probe_state mps
			SET next_retry_at = now() + ($4 * interval '1 second')
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			JOIN credentials c ON c.id = cmb.credential_id
			JOIN providers p ON p.id = c.provider_id
			WHERE mps.credential_id = cmb.credential_id
			  AND mps.raw_model_name = pm.raw_model_name
			  AND mps.state = 'healthy_confirmed'
			  AND (mps.next_retry_at IS NULL OR mps.next_retry_at < now() + ($4 * interval '1 second'))
			  AND COALESCE(c.status, 'active') = 'active'
			  AND COALESCE(c.lifecycle_status, 'active') = 'active'
			  AND COALESCE(c.manual_disabled, FALSE) = FALSE
			  AND COALESCE(p.enabled, FALSE) = TRUE
			  AND COALESCE(p.manual_disabled, FALSE) = FALSE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND lower(mps.raw_model_name) NOT IN (SELECT model FROM static)
			  AND lower(mps.raw_model_name) NOT IN (SELECT raw_model FROM usage)
		`, tenant, windowHours, topN, targetSecs)
	if err != nil {
		slog.Warn("nonfeatured watchdog tick failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("nonfeatured watchdog extended next_retry_at",
			"rows", n, "mult", mult, "target_hours", targetSecs/3600)
	}
}

// SubmitManualProbe submits a manual probe task to the async queue.
// Returns immediately with nil on success, or error if queue is full.
// The probe executes in background via manualProbeWorker.
func (r *ModelProbeRunner) SubmitManualProbe(credID int, model string) error {
	select {
	case r.manualProbeQueue <- manualProbeTask{CredentialID: credID, RawModel: model}:
		slog.Debug("manual probe submitted to async queue",
			"credential_id", credID,
			"model", model)
		return nil
	default:
		return fmt.Errorf("manual probe queue full")
	}
}

// manualProbeWorker consumes manual probe tasks from the queue and executes
// TriggerManual in background goroutines. Each task runs independently with
// timeout; failures are logged but don't block the queue.
func (r *ModelProbeRunner) manualProbeWorker(ctx context.Context) {
	for {
		select {
		case task := <-r.manualProbeQueue:
			r.manualProbeWG.Add(1)
			go func(t manualProbeTask) {
				defer r.manualProbeWG.Done()
				probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				if err := r.TriggerManual(probeCtx, t.CredentialID, t.RawModel); err != nil {
					slog.Warn("async manual probe failed",
						"credential_id", t.CredentialID,
						"model", t.RawModel,
						"error", err)
				}
			}(task)
		case <-ctx.Done():
			slog.Info("manual probe worker shutting down, waiting for in-flight probes")
			r.manualProbeWG.Wait()
			return
		}
	}
}

func (r *ModelProbeRunner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	if fc := r.featuredCancel.Load(); fc != nil && *fc != nil {
		(*fc)()
	}
	<-r.done
}

func (r *ModelProbeRunner) run(ctx context.Context) {
	defer r.closeOnce.Do(func() { close(r.done) })

	ticker := time.NewTicker(ProbeInterval)
	defer ticker.Stop()

	r.cycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// queued is the in-memory snapshot of a (credential, model) row that's
// due for a probe this cycle.
type queued struct {
	t       probeTarget
	state   string
	succCnt int
	failCnt int
}

// cycle runs one probe pass.  Picks up to MaxBatchPerCycle bindings
// whose next_retry_at has elapsed, runs a probe, updates the consensus
// state and writes a model_probe_runs row.
//
// 2026-06-23: switched to v_adaptive_probe_targets view. The view carries
// the (consecutive_failures, age, recent_passive_failures) triple, so the
// ORDER BY clause can prioritize the most-urgent targets:
//
//  1. bindings with recent passive failures in the last 5 min (transient
//     spike that the period probe missed);
//  2. bindings whose consecutive_failures is high (close to broken);
//  3. bindings that have been waiting the longest.
//
// This is the fix for the minimax-m3 06-23 incident where 27
// 'no_candidates' errors during a request spike took 5+ minutes to
// recover because the runner was waiting on its 5-min backoff.
func (r *ModelProbeRunner) cycle(ctx context.Context) {
	// v6 fix (2026-06-20): increased from 3min to 10min to accommodate
	// multiple probe retries (4 attempts × 55s backoff = ~70s per probe).
	// A batch of 10 models could take 10+ minutes with retries.
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// 2026-06-22 defect (1): idempotent reconciliation. ensure every
	// (credential, model) pair whose model_probe_state is 'broken_confirmed'
	// has its credential_model_bindings.available=FALSE. The event-driven
	// propagation in applyResult only fires on a fresh state transition, so
	// pairs that reached broken_confirmed before the P4 code (2026-06-19)
	// landed — or whose binding was flipped back to available by a non-P4
	// path — can drift back to available=TRUE and re-enter the candidate
	// pool. This re-applies the invariant each cycle without relying on a
	// state-change event. Cheap: one UPDATE, guarded by NOT LIKE 'manual%'
	// so admin-set manual reasons are never overwritten.
	r.reconcileBrokenConfirmedBindings(timeoutCtx)

	// 2026-06-29 fix: 反向reconcile — 当 model_probe_state 变为 healthy_confirmed
	// 时恢复 binding.available=TRUE。解决"常用模型报无可用凭据"误报问题。
	r.reconcileHealthyConfirmedBindings(timeoutCtx)

	// 2026-07-04 fix: 把 model_probe_state 表中残留的旧字面量
	// ('available' / 'healthy' / 'unavailable' / 'failing') 一次性
	// 映射到当前状态机。这是 migrations/329 的运行期补充——防止
	// 旧 init 路径再次写入 "available" 等不在 reader 白名单中的
	// 状态值，导致路由侧报 "无可用凭据"。
	r.reconcileLegacyModelProbeStates(timeoutCtx)

	// 2026-06-23: passive-failure boost — apply model's recent failure
	// signals to the schedule BEFORE selecting targets. If a binding has
	// had 3+ failures in the last 5 minutes (e.g. the minimax-m3 spike),
	// pull its next_retry_at forward to 30 seconds. This is the
	// "don't wait for the next backoff tick" mechanism.
	r.applyPassiveBoosts(timeoutCtx)

	rows, err := r.db.Query(timeoutCtx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(mc.modality, 'text'),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(mps.state, 'unknown'), COALESCE(mps.consecutive_successes, 0),
		       COALESCE(mps.consecutive_failures, 0)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN v_routable_credential_models v
		       ON v.credential_id = cmb.credential_id
		      AND v.raw_model_name = pm.raw_model_name
		LEFT JOIN model_probe_state mps
		       ON mps.credential_id = cmb.credential_id
		      AND mps.raw_model_name = pm.raw_model_name
		WHERE COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND (
		      COALESCE(v.is_routable, FALSE) = FALSE
		      OR mps.state = 'recovering'
		  )
		  AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed'
			  AND (mps.next_retry_at IS NULL OR mps.next_retry_at <= NOW())

		ORDER BY
		  -- 2026-06-23: probe the most-urgent targets first.
		  -- 1. The oldest failures (largest age_secs) — they've been
		  --    waiting the longest.
		  -- 2. Then by consecutive_failures desc — closer to broken.
		  -- 3. Then by id for stable ordering.
		  COALESCE(mps.last_attempt_at, NOW() - INTERVAL '1 hour') ASC,
		  COALESCE(mps.consecutive_failures, 0) DESC,
		  cmb.id
		LIMIT $1
	`, MaxBatchPerCycle)
	if err != nil {
		slog.Warn("model probe v2: target query failed", "error", err)
		return
	}
	defer rows.Close()

	var due []queued
	seen := make(map[string]struct{}, MaxBatchPerCycle) // 2026-07-14: per-cycle dedup
	for rows.Next() {
		var q queued
		var ciphertext []byte
		if err := rows.Scan(
			&q.t.CredentialID, &q.t.RawModel, &q.t.OutboundModel, &q.t.Modality,
			&q.t.BaseURL, &q.t.Protocol,
			&ciphertext, &q.t.ManualDisabled,
			&q.state, &q.succCnt, &q.failCnt,
		); err != nil {
			continue
		}
		// 2026-07-14 audit fix: dedupe by (credential_id, raw_model) within
		// a single cycle. The SQL query is not strictly unique because the
		// ORDER BY can produce ties (e.g. when multiple bindings share
		// consecutive_failures=0 and the same last_attempt_at). Without
		// this, one cycle may probe the same (cred, model) twice in a
		// 5min window — defeating the backoff schedule and confusing
		// operators watching the live stream.
		dedupKey := fmt.Sprintf("%d|%s", q.t.CredentialID, q.t.RawModel)
		if _, ok := seen[dedupKey]; ok {
			slog.Debug("model probe v2: skipping duplicate target within cycle",
				"credential_id", q.t.CredentialID,
				"raw_model", q.t.RawModel)
			continue
		}
		seen[dedupKey] = struct{}{}

		apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
		if decErr != nil {
			// Decrypt failure counts as a hard auth failure; record
			// the run + apply the consensus rule.
			_, _, ns, nf, nst := r.computeConsensus("auth", probeCategoryProviderError, q.state, "decrypt_error", q.succCnt, q.failCnt)
			r.recordRun(timeoutCtx, q.t, "auth", nil, "decrypt_error", decErr.Error(), 0, "unchanged", false, "scheduler")
			r.applyResult(timeoutCtx, q.t, "auth", nil, "decrypt_error", decErr.Error(), 0,
				"unchanged", false, "scheduler", ns, nf, nst)
			continue
		}
		q.t.APIKey = apiKey
		due = append(due, q)
	}
	if len(due) == 0 {
		return
	}

	tested := 0
	recovered := 0
	confirmedBroken := 0
	for _, q := range due {
		// Last-second manual_disable recheck.  SQL filters these out,
		// but between query and probe the operator could have flipped
		// the flag — we never want a passing probe to auto-recover a
		// manually-disabled binding.
		if q.t.ManualDisabled {
			r.recordRun(timeoutCtx, q.t, "skipped", nil, "manual_disabled",
				"credential manually disabled; probe skipped", 0,
				"unchanged", false, "scheduler")
			continue
		}

		status, category, httpStatus, errCode, errMsg, latency := r.probeModel(timeoutCtx, q.t)
		r.verifyTargetModality(timeoutCtx, q.t, status, "scheduler")

		stateChange, applied, newSucc, newFail, newState := r.computeConsensus(
			status, category, q.state, errCode, q.succCnt, q.failCnt,
		)

		r.recordRun(timeoutCtx, q.t, status, &httpStatus, errCode, errMsg, latency,
			stateChange, applied, "scheduler")
		r.applyResult(timeoutCtx, q.t, status, &httpStatus, errCode, errMsg, latency,
			stateChange, applied, "scheduler", newSucc, newFail, newState)

		tested++
		switch stateChange {
		case "recovered":
			recovered++
		case "broke":
			confirmedBroken++
		}
	}

	slog.Info("model probe v2: cycle complete",
		"tested", tested,
		"recovered", recovered,
		"broken_confirmed", confirmedBroken,
		"required_consensus", RequiredConsensus,
	)
}

// featuredCycleLoop runs Layer 4 deep probe for 常用模型 (featured ∪ usage top-N).
// Cadence is configurable via probe.featured_cycle_seconds (default 900=15min;
// was hardcoded 30min). 常用模型自检更频繁，其它模型不走此深探测周期。
func (r *ModelProbeRunner) featuredCycleLoop(ctx context.Context) {
	interval := time.Duration(settings.GetPlatformInt("probe.featured_cycle_seconds", 900)) * time.Second
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Stagger initial run by 2 minutes so it does not collide with the
	// 30s CredentialSelfcheckWorker tick. Audit fix #5: cancellation-aware so
	// Stop() does not block for 2 minutes on shutdown.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	r.featuredCycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.featuredCycle(ctx)
		}
	}
}

// featuredCycle does a chat ping for each binding whose raw_model_name
// appears in routing_policy.featured_models (the Layer 4 "hot model" list).
// It does NOT update model_probe_state — the result is recorded as a
// model_probe_runs row for visibility and the probe outcome goes through
// the same consensus state machine on the next L1+L2 cycle.
func (r *ModelProbeRunner) featuredCycle(ctx context.Context) {
	timeout, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rows, err := r.db.Query(timeout, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(mc.modality, 'text'),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(pm.standardized_name, '')
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		LIMIT $1
	`, MaxBatchPerCycle*4 /* bound scan; Go-side globalIsFeaturedModel further filters */)
	if err != nil {
		slog.Warn("featured cycle: query failed", "error", err)
		return
	}
	defer rows.Close()

	var tested int
	for rows.Next() {
		var t probeTarget
		var ciphertext []byte
		var standardized string
		if err := rows.Scan(
			&t.CredentialID, &t.RawModel, &t.OutboundModel, &t.Modality,
			&t.BaseURL, &t.Protocol, &ciphertext, &t.ManualDisabled, &standardized,
		); err != nil {
			continue
		}
		if t.ManualDisabled {
			continue
		}
		// 2026-08-13: "常用模型" filter (static featured ∪ usage Top-N). Only
		// 常用 models get the deep chat-ping; non-featured rows are skipped here
		// (they stay on the cheaper consensus models-list cycle).
		if !globalIsFeaturedModel(t.RawModel, standardized) {
			continue
		}
		apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
		if decErr != nil {
			continue
		}
		t.APIKey = apiKey
		desc := providercap.Resolve(t.Protocol, "")
		mode := ProbeModeChatPing
		if desc.Protocol == "anthropic-messages" {
			mode = ProbeModeMessages
		}
		result := probeWithRetry(timeout, desc, t, mode)
		// Record the probe result as a model_probe_runs row for visibility.
		var httpStatus *int
		if result.httpStatus > 0 {
			httpStatus = &result.httpStatus
		}
		r.recordRun(timeout, t, result.status, httpStatus, result.errCode, result.errMsg,
			result.latencyMs, "unchanged", false, "scheduler")
		tested++
		time.Sleep(2 * time.Second) // rate limit: 2s between probes
	}
	slog.Info("featured cycle (Layer 4) complete",
		"tested", tested,
	)
}

// computeConsensus returns the new (state_change, applied, succ, fail, newState)
// tuple for one probe result, applying the consensus rule.
//
// Branch is on probeCategory, not raw status, so that http_4xx/http_5xx
// outcomes are routed correctly: model_unavailable counts as a failure
// while provider_error does not.
//
// Consensus rules (per RequiredConsensus = 3):
//   - ok: succ++; fail = 0
//     if succ >= 3 → newState = 'healthy_confirmed', state_change = 'recovered'
//     else          newState = 'recovering',         state_change = 'unchanged'
//   - model_unavailable: fail++; succ = 0
//     if fail >= 3 → newState = 'broken_confirmed',  state_change = 'broke'
//     else          newState = 'recovering',        state_change = 'unchanged'
//   - provider_error (auth/network/5xx/rate_limit): succ = 0, fail unchanged
//     so provider issues never cause a model to be marked broken_confirmed.
//   - skipped: no state change, but endpoint_id_required resets counters.
func (r *ModelProbeRunner) computeConsensus(
	status string, category probeCategory, prevState, errCode string, prevSucc, prevFail int,
) (stateChange string, applied bool, newSucc, newFail int, newState string) {
	newSucc = prevSucc
	newFail = prevFail
	newState = prevState
	stateChange = "unchanged"
	applied = true

	switch category {
	case probeCategoryOK:
		newSucc = prevSucc + 1
		newFail = 0
		// If already healthy_confirmed, a watchdog success keeps us
		// there but does NOT re-fire the 'recovered' event — that
		// would spam the state_change log on every 2h watchdog tick.
		if prevState == "healthy_confirmed" {
			newState = "healthy_confirmed"
			stateChange = "unchanged"
			break
		}
		newState = "recovering"
		if newSucc >= RequiredConsensus {
			newState = "healthy_confirmed"
			stateChange = "recovered"
		}
	case probeCategoryModelUnavailable:
		// Genuine model problems (404 model_not_found, 400, 422, etc.) count as failures.
		newFail = prevFail + 1
		newSucc = 0
		newState = "recovering"
		if newFail >= RequiredConsensus {
			newState = "broken_confirmed"
			stateChange = "broke"
		}
	case probeCategorySkipped:
		if errCode == "endpoint_id_required" {
			// Reset counters so broken_confirmed bindings re-enter the queue
			// once outbound_model_name is set.
			newSucc = 0
			newFail = 0
			newState = "recovering"
			applied = true
		} else {
			// manual_disabled, suspended, endpoint_unresolved, etc.
			applied = false
			stateChange = "unchanged"
		}
	default:
		// probeCategoryProviderError: do NOT advance fail counter. Provider-side
		// issues (auth, network, http_5xx, rate_limit) do not prove the model
		// is unavailable.
		newSucc = 0
		newState = "recovering"
	}
	return
}

// applyResult upserts model_probe_state with the consensus outcome.
// next_retry_at is computed by the SQL function model_probe_backoff(N)
// for 'recovering' state, with longer intervals for the
// healthy_confirmed watchdog and broken_confirmed stop.
//
// Receives the pre-computed consensus result so we don't recompute it
// (the caller already has it).  This avoids the DRY trap of two
// computeConsensus calls that must agree.
//
// reconcileBrokenConfirmedBindings (defect 1, 2026-06-22) re-applies the
// broken_confirmed → binding available=FALSE invariant for every pair whose
// model_probe_state is broken_confirmed. Idempotent; safe to run every cycle.
// Guards: never overwrite a manual unavailable_reason, and only touches
// bindings that are currently available=TRUE (the common drift case). Called
// once at the top of cycle() so the candidate pool converges even without a
// fresh state-change event.
func (r *ModelProbeRunner) reconcileBrokenConfirmedBindings(ctx context.Context) {
	tag, err := r.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available          = FALSE,
		    unavailable_reason = 'model_probe_broken',
		    unavailable_at     = NOW()
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND EXISTS (
		      SELECT 1 FROM model_probe_state mps
		      WHERE mps.credential_id = cmb.credential_id
		        AND mps.raw_model_name = pm.raw_model_name
		        AND mps.state = 'broken_confirmed'
		  )
	`)
	if err != nil {
		slog.Warn("model probe: broken_confirmed reconciliation failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("model probe: reconciled broken_confirmed bindings to unavailable",
			"count", tag.RowsAffected())
	}
}

// applyPassiveBoosts scans the candidate_failure_logs table for bindings
// with a recent spike and pulls their next_retry_at forward. Without this,
// the minimax-m3 06-23 incident showed 27 'no_candidates' errors during a
// 5-minute window, but the runner was waiting on its 5-minute backoff, so
// recovery took 5+ minutes even though the underlying failure had cleared
// after the first 30 seconds.
//
// Algorithm (delegated to SQL function model_probe_passive_boost):
//   - 3+ failures in last 5 min  → next_retry_at = NOW() + 30s
//   - 2  failures in last 5 min  → next_retry_at = NOW() + 1m
//   - else                        → leave schedule alone
//
// 2026-07-13 fix: comment said "Runs once per cycle (every 10 min)" but
// ProbeInterval was 10s, leading to 21 probes/min for healthy models.
// With ProbeInterval now 5min, this matches the docstring again.
func (r *ModelProbeRunner) applyPassiveBoosts(ctx context.Context) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT credential_id, raw_model_name
		FROM candidate_failure_logs
		WHERE ts > NOW() - INTERVAL '5 minutes'
	`)
	if err != nil {
		slog.Warn("model probe: passive boost query failed", "error", err)
		return
	}
	defer rows.Close()

	boosted := 0
	for rows.Next() {
		var credID int64
		var rawModel string
		if err := rows.Scan(&credID, &rawModel); err != nil {
			continue
		}
		if _, err := r.db.Exec(ctx,
			`SELECT model_probe_passive_boost($1, $2, NOW())`,
			credID, rawModel,
		); err != nil {
			slog.Debug("model probe: passive boost exec failed",
				"credential_id", credID,
				"raw_model", rawModel,
				"error", err)
			continue
		}
		boosted++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("model probe: passive boost rows iteration error", "error", err)
		return
	}
	if boosted > 0 {
		slog.Info("model probe: applied passive-failure boost",
			"bindings_boosted", boosted,
		)
	}
}

func (r *ModelProbeRunner) applyResult(
	ctx context.Context, t probeTarget,
	status string, httpStatus *int, errCode, errMsg string, latencyMs int,
	stateChange string, applied bool, triggeredBy string,
	newSucc, newFail int, newState string,
) {

	cfg := LoadProbeBackoffConfig()
	var nextRetryInterval time.Duration
	switch newState {
	case "healthy_confirmed":
		nextRetryInterval = cfg.NextDelay(0)
		// 2026-08-13: 非常用模型降频 — 健康态看门狗乘倍率（默认 4 → ~8h 才再探），
		// 降低非常用模型自检频度。常用模型维持基准 2h。仅作用于健康态，
		// 不影响失败检测/熔断（broken_confirmed 与 default 分支不动）。
		if !globalIsFeaturedModel(t.RawModel, "") {
			if mult := settings.GetPlatformInt("probe.nonfeatured_watchdog_multiplier", 4); mult > 1 {
				nextRetryInterval *= time.Duration(mult)
			}
		}
	case "broken_confirmed":
		nextRetryInterval = time.Duration(settings.GetPlatformInt("probe.broken_watchdog_hours", 168)) * time.Hour
	default:
		nextRetryInterval = cfg.NextDelay(newFail)
	}

	q := `
		INSERT INTO model_probe_state
		    (credential_id, raw_model_name, state,
		     consecutive_successes, consecutive_failures, total_attempts,
		     last_attempt_at, next_retry_at, last_status,
		     last_state_change_at, last_state_change_run)
		VALUES ($1, $2, $3, $4, $5, 1, NOW(), NOW() + $6::interval, $7,
		        CASE WHEN $8 IN ('recovered','broke') THEN NOW() ELSE NULL END,
		        CASE WHEN $8 IN ('recovered','broke') THEN
		    (SELECT id FROM model_probe_runs_with_current_month
		             WHERE credential_id = $1 AND raw_model_name = $2
		             ORDER BY id DESC LIMIT 1)
		        ELSE NULL END)
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		    state                  = EXCLUDED.state,
		    consecutive_successes  = EXCLUDED.consecutive_successes,
		    consecutive_failures   = EXCLUDED.consecutive_failures,
		    total_attempts         = model_probe_state.total_attempts + 1,
		    last_attempt_at        = NOW(),
		    next_retry_at          = EXCLUDED.next_retry_at,
		    last_status            = EXCLUDED.last_status,
		    last_state_change_at   = COALESCE(EXCLUDED.last_state_change_at, model_probe_state.last_state_change_at),
		    last_state_change_run  = COALESCE(EXCLUDED.last_state_change_run, model_probe_state.last_state_change_run)
	`
	intervalStr := fmt.Sprintf("%d seconds", int(nextRetryInterval.Seconds()))
	if _, err := r.db.Exec(ctx, q,
		t.CredentialID, t.RawModel, newState,
		newSucc, newFail,
		intervalStr,
		status, stateChange,
	); err != nil {
		slog.Warn("model probe v2: applyResult failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"error", err)
	}

	// P4 (2026-06-19): propagate probe consensus to credential_model_bindings
	// so Path B (resolve.go) and Path C (admin/streaming.go) also see the
	// availability change — not just Path A which reads v_routable_credential_models.
	//
	// broken_confirmed  → available=FALSE, unavailable_reason='model_probe_broken'
	// healthy_confirmed → restore available=TRUE if reason was 'model_probe_broken'
	// Guard: never overwrite manual/admin-set unavailable_reason.
	switch newState {
	case "broken_confirmed":
		_, err := r.db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = FALSE,
			    unavailable_reason = 'model_probe_broken',
			    unavailable_at     = NOW()
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND cmb.credential_id     = $1
			  AND pm.raw_model_name     = $2
			  AND cmb.available         = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, t.CredentialID, t.RawModel)
		if err != nil {
			slog.Warn("model probe: broken_confirmed binding update failed",
				"credential_id", t.CredentialID, "raw_model", t.RawModel, "error", err)
		} else {
			slog.Info("model probe: marked binding unavailable (broken_confirmed)",
				"credential_id", t.CredentialID, "raw_model", t.RawModel)
		}
		r.writeAvailabilityCache(ctx, t, newState, false, status, newSucc, newFail, nextRetryInterval)
	case "healthy_confirmed":
		_, err := r.db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND cmb.credential_id     = $1
			  AND pm.raw_model_name     = $2
			  AND cmb.available         = FALSE
			  AND cmb.unavailable_reason = 'model_probe_broken'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, t.CredentialID, t.RawModel)
		if err != nil {
			slog.Warn("model probe: healthy_confirmed binding restore failed",
				"credential_id", t.CredentialID, "raw_model", t.RawModel, "error", err)
		} else {
			slog.Info("model probe: restored binding available (healthy_confirmed)",
				"credential_id", t.CredentialID, "raw_model", t.RawModel)
		}
		r.writeAvailabilityCache(ctx, t, newState, true, status, newSucc, newFail, nextRetryInterval)
	default:
		r.writeAvailabilityCache(ctx, t, newState, true, status, newSucc, newFail, nextRetryInterval)
	}
}

func (r *ModelProbeRunner) writeAvailabilityCache(
	ctx context.Context,
	t probeTarget,
	state string,
	available bool,
	lastStatus string,
	consecutiveSuccesses int,
	consecutiveFailures int,
	nextRetryIn time.Duration,
) {
	if r.cache == nil || !r.cache.Enabled() {
		return
	}
	nextRetryAt := time.Now().Add(nextRetryIn)
	if err := r.cache.Set(ctx, t.CredentialID, t.RawModel, modelAvailabilityFields(
		t.CredentialID,
		t.RawModel,
		state,
		available,
		lastStatus,
		consecutiveSuccesses,
		consecutiveFailures,
		&nextRetryAt,
		"model_probe",
	)); err != nil {
		slog.Warn("model probe: cache write failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"error", err)
	}
}

// recordRun inserts a row in model_probe_runs for traceability.
// Creates its own 5s timeout context to avoid inheriting expired parent contexts.
//
// 2026-07-13 P0 optimization: skip the INSERT when state_change='unchanged'
// AND the row has no other diagnostic value (no http_status, no error code,
// no error message, status='ok'). These are the noise of the probe system:
//   - 60s watchdog tick that found the model healthy
//   - auto IndexRefresher rerolls that found no change
//   - probe calls where the only thing that happened was "still healthy"
//
// The state machine itself (model_probe_state) is updated separately
// by applyResult, so losing these trace rows does not affect routing.
//
// In production: ~80% of rows are skipped, dropping 74k/day to ~15k/day.
// Storage drop: ~250MB/month → ~50MB/month for 90d retention.
func (r *ModelProbeRunner) recordRun(
	ctx context.Context, t probeTarget,
	status string, httpStatus *int, errCode, errMsg string,
	latencyMs int, stateChange string, applied bool, triggeredBy string,
) {
	// 2026-07-13: short-circuit noise rows. The state machine is the
	// source of truth; model_probe_runs is a forensic trail. We keep
	// rows that either (a) reported a real probe result, (b) flipped
	// state, or (c) carried an HTTP status code / error body.
	if stateChange == "unchanged" && status == "ok" && httpStatus == nil && errCode == "" && errMsg == "" {
		// Pure watchdog/healthcheck tick — no forensic value.
		return
	}

	// Create a fresh 5s timeout for the DB write, independent of parent context.
	// This prevents "context deadline exceeded" when recordRun is called late
	// in a cycle that's approaching its 3-minute timeout.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := r.db.Exec(writeCtx, `
		INSERT INTO model_probe_runs_hot
		    (tenant_id, credential_id, raw_model_name, status,
		     http_status, error_code, error_message, latency_ms,
		     state_change, state_applied, triggered_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10, $11)
	`, "default", t.CredentialID, t.RawModel, status,
		httpStatus, errCode, errMsg, latencyMs,
		stateChange, applied, triggeredBy)
	if err != nil {
		slog.Warn("model probe v2: recordRun failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"error", err)
	}
}

// probeCategory classifies why a probe failed, which determines whether
// it counts toward the consensus failure counter.
type probeCategory string

const (
	probeCategoryOK               probeCategory = "ok"                // upstream responded successfully
	probeCategoryModelUnavailable probeCategory = "model_unavailable" // model genuinely not available (counts as failure)
	probeCategoryProviderError    probeCategory = "provider_error"    // provider-side issue (does NOT count as failure)
	probeCategorySkipped          probeCategory = "skipped"           // skipped (endpoint_id_required, etc.)
)

// probeModel fires a one-shot minimal chat completion at the upstream.
func (r *ModelProbeRunner) probeModel(ctx context.Context, t probeTarget) (
	status string, category probeCategory, httpStatus int, errCode, errMsg string, latencyMs int,
) {
	start := time.Now()

	if t.BaseURL == "" {
		return "skipped", probeCategorySkipped, 0, "endpoint_unresolved", "empty base_url", int(time.Since(start).Milliseconds())
	}
	desc := providercap.Resolve(t.Protocol, "")
	mode := ProbeModeModelsList
	if desc.Protocol == "anthropic-messages" {
		// Anthropic prefers its own /v1/messages endpoint for chat probes;
		// Layer 1+2 already covered by /v1/models which we now also support.
		// Keep chat path as a fallback for the consensus state machine.
		mode = ProbeModeMessages
	}
	result := probeWithRetry(ctx, desc, t, mode)
	return result.status, result.category, result.httpStatus, result.errCode, result.errMsg, result.latencyMs
}

// isEndpointIDRequiredError has moved to internal/probeutil.IsEndpointIDRequiredError.
// Imported as probeutil below.

// TriggerManual fires one off-schedule probe for a single binding.  It
// still goes through the consensus logic — a single manual trigger is
// just one data point, not an override.
//
// 2026-08-13 audit: Bindings whose credential is manually disabled are
// filtered out at the WHERE clause (rather than relying on the
// post-fetch t.ManualDisabled check, which would otherwise still
// execute an upstream probe and decrypt the secret first). The check
// mirrors TriggerAllSync / cycle() so an admin-flipped
// `manual_disabled=true` consistently short-circuits at SQL time and
// returns ErrCredentialManuallyDisabled.
func (r *ModelProbeRunner) TriggerManual(ctx context.Context, credentialID int, rawModel string) error {
	row := r.db.QueryRow(ctx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(mc.modality, 'text'),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(mps.state, 'unknown'), COALESCE(mps.consecutive_successes, 0),
		       COALESCE(mps.consecutive_failures, 0)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN model_probe_state mps
		       ON mps.credential_id = cmb.credential_id
		      AND mps.raw_model_name = pm.raw_model_name
		WHERE cmb.credential_id = $1 AND pm.raw_model_name = $2
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		LIMIT 1
	`, credentialID, rawModel)
	var t probeTarget
	var ciphertext []byte
	var prevState string
	var prevSucc, prevFail int
	if err := row.Scan(&t.CredentialID, &t.RawModel, &t.OutboundModel, &t.Modality, &t.BaseURL, &t.Protocol,
		&ciphertext, &t.ManualDisabled, &prevState, &prevSucc, &prevFail); err != nil {
		if err == pgx.ErrNoRows {
			var disabled bool
			if probeErr := r.db.QueryRow(ctx, `
						SELECT EXISTS (
							SELECT 1
							FROM credential_model_bindings cmb
							JOIN provider_models pm ON pm.id = cmb.provider_model_id
							JOIN credentials c ON c.id = cmb.credential_id
							JOIN providers p ON p.id = c.provider_id
							WHERE cmb.credential_id = $1
							  AND pm.raw_model_name = $2
							  AND (
								  COALESCE(c.manual_disabled, FALSE)
								  OR COALESCE(p.manual_disabled, FALSE)
								  OR COALESCE(c.lifecycle_status, 'active') <> 'active'
								  OR COALESCE(c.status, 'active') <> 'active'
								  OR COALESCE(p.enabled, FALSE) = FALSE
								  OR COALESCE(cmb.unavailable_reason, '') LIKE 'manual%'
							  )
						)
					`, credentialID, rawModel).Scan(&disabled); probeErr != nil {
				return probeErr
			} else if disabled {
				return ErrCredentialManuallyDisabled
			}
			return fmt.Errorf("binding not found")
		}
		return err
	}

	// 2026-08-14: Race protection - recheck eligibility before decrypt.
	// Between initial SELECT (line ~1088) and here, operator could flip
	// manual_disabled or lifecycle_status. This prevents decrypt + upstream
	// probe of credentials that became ineligible during the race window.
	var statusOk, lifecycleOk, credDisabled, providerEnabled, providerDisabled bool
	recheckErr := r.db.QueryRow(ctx, `
		SELECT 
			COALESCE(c.status, 'active') = 'active',
			COALESCE(c.lifecycle_status, 'active') = 'active',
			COALESCE(c.manual_disabled, FALSE),
			COALESCE(p.enabled, FALSE),
			COALESCE(p.manual_disabled, FALSE)
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, t.CredentialID).Scan(&statusOk, &lifecycleOk, &credDisabled, &providerEnabled, &providerDisabled)

	if recheckErr != nil {
		return recheckErr
	}
	if !statusOk || !lifecycleOk || credDisabled || !providerEnabled || providerDisabled {
		r.recordRun(ctx, t, "skipped", nil, "disabled_after_query",
			"credential became ineligible between query and probe", 0,
			"unchanged", false, "manual")
		return ErrCredentialManuallyDisabled
	}

	apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
	if decErr != nil {
		_, _, ns, nf, nst := r.computeConsensus("auth", probeCategoryProviderError, prevState, "decrypt_error", prevSucc, prevFail)
		r.recordRun(ctx, t, "auth", nil, "decrypt_error", decErr.Error(), 0, "unchanged", false, "manual")
		r.applyResult(ctx, t, "auth", nil, "decrypt_error", decErr.Error(), 0,
			"unchanged", false, "manual", ns, nf, nst)
		return decErr
	}
	t.APIKey = apiKey

	status, category, httpStatus, errCode, errMsg, latency := r.probeModel(ctx, t)
	r.verifyTargetModality(ctx, t, status, "manual")
	stateChange, applied, newSucc, newFail, newState := r.computeConsensus(status, category, prevState, errCode, prevSucc, prevFail)
	r.recordRun(ctx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "manual")
	r.applyResult(ctx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "manual",
		newSucc, newFail, newState)

	// 2026-07-25 SPEC §3.1.5: mirror TriggerAllSync behavior — when the
	// single manual probe succeeds, clear node_probe_state so
	// v_routable_credential_models drops node_probe_failed immediately.
	if status == "ok" {
		if err := MarkNodeProbeHealthy(ctx, r.db, t.CredentialID, t.RawModel); err != nil {
			slog.Warn("TriggerManual: mark node_probe_state healthy failed",
				"credential_id", t.CredentialID, "raw_model", t.RawModel, "error", err)
		}
	}
	return nil
}

// ProbeAllResult is the per-binding result returned by TriggerAllSync.
type ProbeAllResult struct {
	CredentialID int    `json:"credential_id"`
	RawModel     string `json:"raw_model_name"`
	Status       string `json:"status"`   // ok, network, auth, http_4xx, http_5xx, skipped
	Category     string `json:"category"` // ok, model_unavailable, provider_error, skipped
	HTTPStatus   *int   `json:"http_status"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	LatencyMs    int    `json:"latency_ms"`
}

// TriggerAllSync fires synchronous probes for ALL (credential, model) bindings
// under a provider and returns real-time results immediately.
// Unlike the background cycle(), this does NOT modify model_probe_state —
// it only returns the live probe results so the operator can see what's
// actually happening without changing any state.
//
// Provider-side errors (network, auth, http_5xx, rate_limit) are reported
// separately from genuine model unavailability (404 model_not_found, 400, etc.)
// so operators can distinguish upstream problems from actual model issues.
func (r *ModelProbeRunner) TriggerAllSync(ctx context.Context, providerID int) ([]ProbeAllResult, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rows, err := r.db.Query(timeoutCtx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE c.provider_id = $1
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
	`, providerID)
	if err != nil {
		return nil, fmt.Errorf("query bindings: %w", err)
	}
	defer rows.Close()

	var results []ProbeAllResult
	for rows.Next() {
		var t probeTarget
		var ciphertext []byte
		if err := rows.Scan(
			&t.CredentialID, &t.RawModel, &t.OutboundModel,
			&t.BaseURL, &t.Protocol,
			&ciphertext, &t.ManualDisabled,
		); err != nil {
			continue
		}

		if t.ManualDisabled {
			results = append(results, ProbeAllResult{
				CredentialID: t.CredentialID,
				RawModel:     t.RawModel,
				Status:       "skipped",
				Category:     "skipped",
				ErrorCode:    "manual_disabled",
				ErrorMessage: "credential manually disabled; probe skipped",
			})
			continue
		}

		apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
		if decErr != nil {
			results = append(results, ProbeAllResult{
				CredentialID: t.CredentialID,
				RawModel:     t.RawModel,
				Status:       "auth",
				Category:     "provider_error",
				ErrorCode:    "decrypt_error",
				ErrorMessage: decErr.Error(),
			})
			continue
		}
		t.APIKey = apiKey

		status, category, httpStatus, errCode, errMsg, latency := r.probeModel(timeoutCtx, t)

		var httpStatusPtr *int
		if httpStatus > 0 {
			httpStatusPtr = &httpStatus
		}

		results = append(results, ProbeAllResult{
			CredentialID: t.CredentialID,
			RawModel:     t.RawModel,
			Status:       status,
			Category:     string(category),
			HTTPStatus:   httpStatusPtr,
			ErrorCode:    errCode,
			ErrorMessage: errMsg,
			LatencyMs:    latency,
		})

		// 2026-07-21 P0: when a manual "全面探测" probe returns OK,
		// immediately mark the node_probe_state row healthy so the
		// routing view (v_routable_credential_models) re-admits the
		// binding without waiting up to 24h for NodeProbeWorker's
		// backoff ladder to roll over (or forever if paused=TRUE).
		//
		// We deliberately do NOT touch model_probe_state here — the
		// background cycle() still owns that state machine via
		// consensus, and a single manual probe is just one data
		// point (per TriggerManual's docstring).
		if status == "ok" {
			if err := MarkNodeProbeHealthy(timeoutCtx, r.db, t.CredentialID, t.RawModel); err != nil {
				slog.Warn("triggerAllProbes: mark node_probe_state healthy failed",
					"credential_id", t.CredentialID,
					"raw_model_name", t.RawModel,
					"error", err.Error())
			}
		}
	}
	if err := rows.Err(); err != nil {
		return results, fmt.Errorf("iterate bindings: %w", err)
	}

	return results, nil
}

// probeTarget is the (credential, model, base_url, protocol, api_key)
// tuple we test.
type probeTarget struct {
	CredentialID   int
	RawModel       string
	OutboundModel  string // COALESCE(pm.outbound_model_name, pm.raw_model_name)
	Modality       string // canonical modality inferred during discovery
	BaseURL        string
	Protocol       string
	APIKey         string
	ManualDisabled bool
}

// verifyTargetModality records positive and negative modality evidence without
// changing the global canonical value. A provider credential may reject a
// modality that another credential with the same canonical model supports.
func (r *ModelProbeRunner) verifyTargetModality(ctx context.Context, t probeTarget, status, triggeredBy string) {
	if status != "ok" || t.Modality == "" || t.Modality == "text" || t.Modality == "embedding" || t.BaseURL == "" {
		return
	}

	model := t.OutboundModel
	if model == "" {
		model = t.RawModel
	}
	desc := providercap.Resolve(t.Protocol, "")
	endpoint := upstreamurl.Build(t.BaseURL, desc.ChatProbeEndpoint)
	result := ProbeModality(ctx, endpoint, t.APIKey, model, t.Modality, desc.Protocol == "anthropic-messages")
	if result.ErrCode == "" && result.Supported {
		slog.Info("model modality probe succeeded",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"modality", t.Modality,
			"latency_ms", result.LatencyMs,
			"triggered_by", triggeredBy)
		return
	}
	slog.Warn("model modality probe result",
		"credential_id", t.CredentialID,
		"raw_model", t.RawModel,
		"modality", t.Modality,
		"supported", result.Supported,
		"error_code", result.ErrCode,
		"error_message", result.ErrMsg,
		"http_status", result.HTTPStatus,
		"triggered_by", triggeredBy)
}

// GetState returns the current consensus state for a binding (used by
// the admin API to show "2/3 successful — next attempt in 4m").
func (r *ModelProbeRunner) GetState(ctx context.Context, credentialID int, rawModel string) (*ProbeStateRow, error) {
	row := r.db.QueryRow(ctx, `
		SELECT credential_id, raw_model_name, state,
		       consecutive_successes, consecutive_failures, total_attempts,
		       last_attempt_at, next_retry_at, last_status,
		       last_state_change_at, last_state_change_run
		FROM model_probe_state
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credentialID, rawModel)
	var s ProbeStateRow
	err := row.Scan(&s.CredentialID, &s.RawModel, &s.State,
		&s.ConsecutiveSuccesses, &s.ConsecutiveFailures, &s.TotalAttempts,
		&s.LastAttemptAt, &s.NextRetryAt, &s.LastStatus,
		&s.LastStateChangeAt, &s.LastStateChangeRun)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListStates returns all probe states for a given provider, optionally
// filtered by state.  Used by the providers-page "自动测试" tab.
func (r *ModelProbeRunner) ListStates(ctx context.Context, providerID int, stateFilter string) ([]ProbeStateRow, error) {
	args := []any{providerID}
	q := `
		SELECT mps.credential_id, mps.raw_model_name, mps.state,
		       mps.consecutive_successes, mps.consecutive_failures, mps.total_attempts,
		       mps.last_attempt_at, mps.next_retry_at, mps.last_status,
		       mps.last_state_change_at, mps.last_state_change_run
		FROM model_probe_state mps
		JOIN credentials c ON c.id = mps.credential_id
		WHERE c.provider_id = $1
	`
	if stateFilter != "" {
		q += " AND mps.state = $2"
		args = append(args, stateFilter)
	}
	q += " ORDER BY mps.next_retry_at NULLS FIRST LIMIT 200"

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ProbeStateRow, 0)
	for rows.Next() {
		var s ProbeStateRow
		if err := rows.Scan(&s.CredentialID, &s.RawModel, &s.State,
			&s.ConsecutiveSuccesses, &s.ConsecutiveFailures, &s.TotalAttempts,
			&s.LastAttemptAt, &s.NextRetryAt, &s.LastStatus,
			&s.LastStateChangeAt, &s.LastStateChangeRun); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// ProbeStateRow is the row shape returned by GetState / ListStates.
type ProbeStateRow struct {
	CredentialID         int        `json:"credential_id"`
	RawModel             string     `json:"raw_model_name"`
	State                string     `json:"state"`
	ConsecutiveSuccesses int        `json:"consecutive_successes"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	TotalAttempts        int        `json:"total_attempts"`
	LastAttemptAt        *time.Time `json:"last_attempt_at"`
	NextRetryAt          time.Time  `json:"next_retry_at"`
	LastStatus           *string    `json:"last_status"`
	LastStateChangeAt    *time.Time `json:"last_state_change_at"`
	LastStateChangeRun   *int64     `json:"last_state_change_run"`
}

func truncate(s string, n int) string { //nolint:unused
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
