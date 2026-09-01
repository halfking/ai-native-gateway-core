package credentialhealth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/modelbinding"
)

// ColdNodeActiveProber issues a real upstream probe when the
// historical DBProber sees zero recent calls (the "cold node" case).
// The signature matches CredentialProber so any existing active-probe
// worker can plug in. nil → cold nodes still probe-fail (legacy
// behaviour); wired → cold nodes get a real request that records into
// the recorder and either succeeds (degradation skipped) or fails
// (degradation proceeds as usual).
type ColdNodeActiveProber interface {
	ProbeCredential(ctx context.Context, credentialID int, model string) ProbeResult
}

// Checker detects continuous failures and marks credentials as degraded.
type Checker struct {
	recorder         *Recorder
	db               DBQuerier
	prober           CredentialProber       // optional: probe before marking degraded
	coldProber       ColdNodeActiveProber   // 2026-08-23: probe cold nodes (no recent calls) via real request
	windowDuration   time.Duration          // default 1 hour
	failureThreshold float64                // default 0.80 (80%)
	minSampleSize    int                    // default 5
	degradedCooldown time.Duration          // default 15 minutes
	enableCheck      bool                   // feature flag
	invalidateCache  func(credentialID int) // candidate-cache invalidator (nil → no-op)

	// 2026-08-23 hzx-2 audit: error-kind gradient thresholds. A single
	// 80% threshold collapses transient 5xx noise, 429s that resolve in
	// seconds, and permanent auth failures into one bucket. Each kind
	// gets its own threshold + cooldown so the gateway degrades fast on
	// real outages but tolerates expected error patterns. Empty maps
	// fall back to the legacy single-threshold behaviour.
	kindThresholds map[string]KindThreshold
}

// KindThreshold is the per-error-kind failure threshold + cooldown.
// Zero values are ignored — the Checker falls back to failureThreshold /
// degradedCooldown in that case.
type KindThreshold struct {
	FailureThreshold float64       // 0..1, e.g. 0.90 for 5xx
	MinSampleSize    int           // override the global min sample size
	DegradedCooldown time.Duration // override the global cooldown
}

// CheckerConfig holds checker configuration.
type CheckerConfig struct {
	WindowDuration   time.Duration
	FailureThreshold float64
	MinSampleSize    int
	DegradedCooldown time.Duration
	EnableCheck      bool
	// Prober (optional) probes credential before marking degraded.
	// If probe succeeds, degradation is skipped (prevents false positives).
	Prober CredentialProber
	// ColdProber (optional, 2026-08-23 hzx-2): probes credentials that
	// have ZERO recent calls in the recorder window. The legacy
	// behaviour is to treat "no data" as "degrade" — which means a
	// never-used credential gets degraded the first time a request
	// lands. With ColdProber wired, the checker fires a real probe
	// instead and only degrades if the upstream actually fails. nil
	// preserves the legacy "no data = probe fail" path.
	ColdProber ColdNodeActiveProber
	// InvalidateCandidateCache (optional) is invoked synchronously with the
	// affected credential after a successful state change so unrelated cached
	// candidate lists stay warm. nil → no-op.
	InvalidateCandidateCache func(credentialID int)
	// KindThresholds (2026-08-23 hzx-2): per-error-kind overrides for
	// FailureThreshold / MinSampleSize / DegradedCooldown. Empty map
	// keeps the legacy single-threshold behaviour for that kind.
	KindThresholds map[string]KindThreshold
}

// DefaultCheckerConfig returns sensible defaults.
func DefaultCheckerConfig() CheckerConfig {
	return CheckerConfig{
		WindowDuration:   1 * time.Hour,
		FailureThreshold: 0.80, // 80% failure rate
		MinSampleSize:    5,
		DegradedCooldown: 15 * time.Minute,
		EnableCheck:      true,
		// 2026-08-23 hzx-2 audit: error-kind gradient. Each row is
		// calibrated against the observed noise floor of that kind
		// across 2026-Q2/Q3 production incidents.
		KindThresholds: defaultKindThresholds(),
	}
}

// defaultKindThresholds encodes the post-2026-08-23 error-kind policy.
//
//   - timeout / stream_timeout: gateway-level signal that the upstream
//     is genuinely unresponsive. Tighten the threshold to 70% and use
//     a long cooldown so failover routes around the dead node quickly.
//   - rate_limit: expected on healthy credentials during burst spikes;
//     the threshold should be near 100% (only sustained rate-limit
//     floods indicate a stuck node) and the cooldown should be short
//     (1 minute) so recovery is fast once the upstream clears the
//     burst.
//   - upstream_context_loss: silent body-stripping — must be caught
//     aggressively (50% threshold, 30 minute cooldown) because the
//     user perceives this as a correct but useless reply.
//   - upstream_down / upstream_overloaded: 5xx blips are tolerated;
//     only sustained outages (90%) trigger degradation, and the
//     cooldown stays at the global 15min default.
//   - model_not_found / model_deprecated / unsupported_feature:
//     permanent for that model. Threshold 100% (any single sample
//     suffices; minSampleSize 1), 24-hour cooldown so the operator
//     notices via the dashboard and re-binds to a different model.
//
// Errors not listed fall back to the global failureThreshold /
// degradedCooldown (the legacy behaviour).
func defaultKindThresholds() map[string]KindThreshold {
	return map[string]KindThreshold{
		"timeout":        {FailureThreshold: 0.70, MinSampleSize: 5, DegradedCooldown: 20 * time.Minute},
		"stream_timeout": {FailureThreshold: 0.70, MinSampleSize: 5, DegradedCooldown: 20 * time.Minute},
		// 2026-08-29 fix: rate_limit 阈值从 0.95 提升到 0.98，最小样本从 8 提升到 15，
		// 冷却从 1 分钟缩短到 30 秒。智谱 GLM/MiniMax 等国内模型在高峰期会返回较多
		// 429，但这些是正常的流控信号，不应触发长时间降级。提升阈值需要更多证据
		// (15 样本中有 14.7 个失败才触发)，缩短冷却让恢复更快。
		"rate_limit": {FailureThreshold: 0.98, MinSampleSize: 15, DegradedCooldown: 30 * time.Second},
		// 2026-08-29 fix: concurrent 并发过载也应更宽容。智谱/MiniMax 的 503 "engine busy"
		// 是瞬态信号，提升阈值到 0.95 避免误判，增加样本到 12 保证统计意义。
		"concurrent":            {FailureThreshold: 0.95, MinSampleSize: 12, DegradedCooldown: 2 * time.Minute},
		"upstream_context_loss": {FailureThreshold: 0.50, MinSampleSize: 3, DegradedCooldown: 30 * time.Minute},
		"upstream_down":         {FailureThreshold: 0.90, MinSampleSize: 8, DegradedCooldown: 15 * time.Minute},
		"upstream_overloaded":   {FailureThreshold: 0.90, MinSampleSize: 8, DegradedCooldown: 15 * time.Minute},
		"model_not_found":       {FailureThreshold: 1.0, MinSampleSize: 1, DegradedCooldown: 24 * time.Hour},
		"model_deprecated":      {FailureThreshold: 1.0, MinSampleSize: 1, DegradedCooldown: 24 * time.Hour},
		"unsupported_feature":   {FailureThreshold: 1.0, MinSampleSize: 1, DegradedCooldown: 24 * time.Hour},
	}
}

// NewChecker creates a continuous failure checker.
func NewChecker(recorder *Recorder, db DBQuerier, cfg CheckerConfig) *Checker {
	return &Checker{
		recorder:         recorder,
		db:               db,
		prober:           cfg.Prober,
		coldProber:       cfg.ColdProber,
		windowDuration:   cfg.WindowDuration,
		failureThreshold: cfg.FailureThreshold,
		minSampleSize:    cfg.MinSampleSize,
		degradedCooldown: cfg.DegradedCooldown,
		enableCheck:      cfg.EnableCheck,
		invalidateCache:  cfg.InvalidateCandidateCache,
		kindThresholds:   cfg.KindThresholds,
	}
}

// resolveKindThreshold returns the effective threshold / min-sample /
// cooldown for the dominant failure kind. Zero / missing entries fall
// back to the global Checker fields. The dominant kind is the one
// with the largest count in the recent window; ties broken by
// alphabetical order for determinism.
func (c *Checker) resolveKindThreshold(errorKinds map[string]int) (KindThreshold, string) {
	if len(c.kindThresholds) == 0 || len(errorKinds) == 0 {
		return KindThreshold{}, ""
	}
	dominant := ""
	maxCount := 0
	for k, n := range errorKinds {
		if n > maxCount || (n == maxCount && k < dominant) {
			dominant = k
			maxCount = n
		}
	}
	if dominant == "" {
		return KindThreshold{}, ""
	}
	t, ok := c.kindThresholds[dominant]
	if !ok {
		return KindThreshold{}, ""
	}
	return t, dominant
}

// Enabled returns true if checking is enabled.
func (c *Checker) Enabled() bool {
	return c != nil && c.recorder != nil && c.db != nil && c.enableCheck
}

// CheckAndUpdate analyzes recent call history and marks credential as degraded if needed.
func (c *Checker) CheckAndUpdate(ctx context.Context, credentialID int, model string) error {
	if !c.Enabled() {
		return nil
	}

	// Get recent entries within window
	since := time.Now().Add(-c.windowDuration)
	entries, err := c.recorder.GetRecent(ctx, credentialID, model, since)
	if err != nil {
		return fmt.Errorf("get recent entries: %w", err)
	}

	// Check sample size
	if len(entries) < c.minSampleSize {
		// 2026-08-23 hzx-2 audit: cold-node handling. The legacy behaviour
		// was "no samples → return nil", which means cold credentials
		// (no recent traffic) never get marked degraded but ALSO never
		// get probed. Combined with the DBProber cold-fail, the first
		// real request that lands on a cold credential during a
		// sustained outage cascades through the 80% threshold with no
		// chance to recover.
		//
		// With ColdProber wired, "no samples" becomes an opportunity to
		// probe via a real upstream request. The probe result drives
		// the decision: success → return nil; failure → degrade. nil
		// ColdProber preserves the legacy "no data → no action" path so
		// existing tests and older call sites are unaffected.
		if c.coldProber != nil {
			coldCtx, coldCancel := context.WithTimeout(ctx, 5*time.Second)
			defer coldCancel()
			result := c.coldProber.ProbeCredential(coldCtx, credentialID, model)
			if result.Success {
				slog.Info("checker: cold-node probe succeeded, skipping degradation",
					"credential_id", credentialID,
					"model", model,
					"probe_latency_ms", result.Latency.Milliseconds())
				return nil
			}
			slog.Info("checker: cold-node probe failed, proceeding with degradation",
				"credential_id", credentialID,
				"model", model,
				"probe_detail", result.Detail)
			// Synthesise a single failure sample so markDegraded has
			// something to log. failureRate=1.0 guarantees the global
			// threshold trips; we don't apply the per-kind gradient
			// here because we only have ONE sample.
			return c.markDegraded(ctx, credentialID, model, 1.0,
				map[string]int{string(result.ErrorKind): 1}, 1,
				string(result.ErrorKind), KindThreshold{})
		}
		return nil // not enough data, no cold prober wired
	}

	// Compute stats (exclude network errors, client problems, and transient issues)
	var total, failed int
	errorKinds := make(map[string]int)

	for _, e := range entries {
		// 2026-07-03 P0 fix: skip network errors AND client bugs.
		// Client bugs (tool_call_id_mismatch, invalid_request_format, etc.)
		// are not credential health issues and should not affect failureRate.
		//
		// 2026-07-15 P0 fix: skip benign stream-timeout (SSE EOF without
		// [DONE]). The previous guard excluded the literal "eof_without_done",
		// but errorsx.ClassifyError maps that condition to KindStreamTimeout
		// (= "stream_timeout"), which is what the recorder stores — so the
		// guard never matched and benign EOFs counted toward the 80%
		// threshold. That was the root cause of the 154 minimax-m3
		// "no_candidates" cascade: every benign EOF pushed a healthy
		// credential into a 15-minute cooldown. Now the guard uses the
		// actual classified kind. (Genuinely benign EOFs — ChunkCount>0 —
		// are also short-circuited as success in executor_chat.go:687 and
		// never reach the recorder; this guard covers the non-benign tail.)
		//
		// 2026-07-16 P0 fix: skip client-side failures (canceled, transient).
		// Root cause of "No available provider" false positive: client_disconnect
		// (VSCode cancel, network hiccup) was counted as credential failure.
		// 15 client cancellations → credential degraded 15 minutes → all
		// requests fail even though credential is healthy.
		//
		// 2026-07-16 P1 fix: KindTimeout and KindStreamTimeout are NO LONGER
		// excluded. These are gateway-level timeout signals (first-byte timeout,
		// upstream context deadline), NOT client-side issues — client disconnect
		// produces KindCanceled, not KindTimeout. A credential that consistently
		// times out should be degraded so the router can fail over to healthier
		// candidates. This was the root cause of "minimax-m3 on NIM keeps timing
		// out but never fails over": every timeout was silently skipped here,
		// the 80% threshold was never reached, and the credential stayed in the
		// candidate pool indefinitely.
		//
		// Skip list now includes:
		// - network: DNS/TCP/connection errors
		// - canceled: client cancel (context.Canceled)
		// - transient: temporary upstream issues (503 for <5s)
		// - client bugs: malformed requests
		// - empty_response: NIM 13% empty-stream quirk (see errorsx.classify
		//   line 60). The KindEmptyResponse design intent explicitly says
		//   "a transient empty burst must not hard-exclude the credential".
		//   Counting it toward the 80% degradation threshold defeats that
		//   intent and triggered the 2026-07-18 credential 19 (NIM/endless)
		//   1-hour cooldown after only ~30 empty streams — the upstream was
		//   healthy throughout. The stream-resumable failover path in
		//   executor_chat.go:838-840 already routes the per-request retry
		//   to the next candidate, so we don't need degradation here either.
		//
		// NOT skipped (intentionally):
		// - upstream_context_loss: a third-party relay silently dropped the
		//   request body's context and answered a stripped prompt (HTTP 200,
		//   clean [DONE], but prompt_tokens a tiny fraction of body size).
		//   Observed on apiclaude.cc 2026-08-04 (request e8bf0d5fc726: 919KB
		//   body → prompt_tokens=337 → 21-token reply; sibling requests of
		//   the same session reported 256K–305K). Unlike KindEmptyResponse
		//   this is a genuine upstream fault the user perceives as an error,
		//   so it MUST count toward degradation to let the router soft-demote
		//   the credential. Do not add it to this skip list.
		if e.ErrorKind == "network" ||
			e.ErrorKind == string(errorsx.KindCanceled) ||
			e.ErrorKind == string(errorsx.KindTransient) ||
			e.ErrorKind == string(errorsx.KindUpstreamOverloaded) ||
			e.ErrorKind == string(errorsx.KindEmptyResponse) ||
			errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
			continue
		}
		total++
		if !e.Success {
			failed++
			if e.ErrorKind != "" {
				errorKinds[e.ErrorKind]++
			}
		}
	}

	if total < c.minSampleSize {
		return nil // not enough non-network samples
	}

	// 2026-08-23 hzx-2 audit: pick the dominant failure kind's
	// threshold BEFORE the global check. The gradient lets transient
	// 5xx noise (e.g. upstream_down) avoid degrading a healthy node
	// while still reacting fast to a sustained rate_limit flood.
	threshold, dominantKind := c.resolveKindThreshold(errorKinds)
	effectiveThreshold := c.failureThreshold
	effectiveMinSample := c.minSampleSize
	if threshold.FailureThreshold > 0 {
		effectiveThreshold = threshold.FailureThreshold
	}
	if threshold.MinSampleSize > 0 {
		effectiveMinSample = threshold.MinSampleSize
	}

	// The dominant-kind min sample size is the STRICTER of the two:
	// high-volume kinds (e.g. rate_limit with perKind=8 vs global=5)
	// need more evidence before degrading; low-frequency kinds
	// (e.g. model_not_found with perKind=1) already have a strict
	// 100% threshold so they fall back to the global min.
	minSamples := c.minSampleSize
	if effectiveMinSample > c.minSampleSize {
		minSamples = effectiveMinSample
	}
	if total < minSamples {
		return nil // not enough samples for the dominant kind
	}

	failureRate := float64(failed) / float64(total)

	// Check threshold
	if failureRate < effectiveThreshold {
		return nil // below threshold, credential is healthy
	}

	// 2026-07-16 P0 fix: probe before marking degraded.
	// If recent call history shows success (within last 30s), don't mark degraded.
	// This prevents false positives where transient errors (that passed the skip
	// filter above) trigger degradation even though credential is actually healthy.
	if c.prober != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		result := c.prober.ProbeCredential(probeCtx, credentialID, model)
		if result.Success {
			slog.Info("credential probe succeeded, skipping degradation",
				"credential_id", credentialID,
				"model", model,
				"failure_rate", failureRate,
				"sample_size", total,
				"probe_latency_ms", result.Latency.Milliseconds())
			return nil // probe passed, don't mark degraded
		}

		slog.Warn("credential probe failed, proceeding with degradation",
			"credential_id", credentialID,
			"model", model,
			"failure_rate", failureRate,
			"sample_size", total,
			"probe_detail", result.Detail)
	}

	// Mark as degraded
	return c.markDegraded(ctx, credentialID, model, failureRate, errorKinds, total, dominantKind, threshold)
}

// markDegraded updates the (credential, model) binding to unavailable.
//
// Why the credential_model_bindings row and not credentials.availability_state:
// v_routable_credential_models.is_routable reads cmb.available, so writing
// to credentials.availability_state alone has zero effect on routing — the
// binding stays routable while the admin UI shows the credential as
// "degraded", and a single bad model is enough to flip the whole credential.
// Updating the specific (credential_id, raw_model_name) row keeps sibling
// models on the same credential routable (per the 2026-06-22 audit on
// cross-model collateral damage).
//
// 2026-08-23 hzx-2 audit: dominantKind + threshold flow through from
// CheckAndUpdate so per-kind cooldown (rate_limit=1min vs auth=24h)
// reaches the cmb write. dominantKind is also logged so dashboards
// can group degradations by root cause without re-querying request_logs.
func (c *Checker) markDegraded(ctx context.Context, credentialID int, model string, rate float64, kinds map[string]int, sampleSize int, dominantKind string, threshold KindThreshold) error {
	now := time.Now()
	cooldown := c.degradedCooldown
	if threshold.DegradedCooldown > 0 {
		cooldown = threshold.DegradedCooldown
	}
	recoverAt := now.Add(cooldown)
	rawModel, err := modelbinding.ResolveRawBinding(ctx, c.db, credentialID, model)
	if err != nil {
		return err
	}

	tag, err := c.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available          = FALSE,
		    unavailable_reason = 'continuous_failure',
		    unavailable_at     = $4,
		    unavailable_recover_at = $3,
		    updated_at         = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
	`, credentialID, rawModel, recoverAt, now)
	if err != nil {
		return fmt.Errorf("update credential_model_bindings: %w", err)
	}

	if tag.RowsAffected() > 0 {
		// Mirror to model_offers so /api/routing/resolve ("test route")
		// surfaces the same unavailability — admin UI and production
		// routing must stay in lock-step.
		//
		// model_offers is a VIEW over credential_model_bindings + provider_models
		// with an INSTEAD OF UPDATE trigger that routes writes back to cmb.
		// The mirror exists because the cmb UPDATE above does NOT touch the
		// view row directly (views are read-only without the trigger path),
		// so /api/routing/resolve would otherwise keep showing the offer as
		// available until something else refreshes it.
		//
		// 2026-08-11 fix: the previous subquery joined on
		//   cmb.unavailable_at = $2  with $2 = recoverAt (= now()+15min),
		// but the cmb write sets unavailable_at = now(), not recoverAt — so
		// the subquery NEVER matched and the mirror updated zero rows. The
		// "lock-step" guarantee was silently broken. Now we match on the same
		// (credential_id, canonical_raw_name) the cmb UPDATE targeted and pass
		// the exact now() timestamp written to cmb.unavailable_at, so the
		// join is stable.
		//
		// Note: the view DOES expose unavailable_recover_at, but the INSTEAD OF
		// UPDATE trigger (model_offers_update_trigger.sql) does NOT propagate
		// that column to cmb, so writing it here would be silently dropped.
		// unavailable_recover_at is already set correctly on the cmb row above;
		// we only mirror the columns the trigger honours.
		if _, moErr := c.db.Exec(ctx, `
			UPDATE model_offers mo
			SET available          = FALSE,
			    unavailable_reason = 'continuous_failure',
			    unavailable_at     = $3
			FROM provider_models pm
			WHERE pm.raw_model_name = $2
			  AND pm.id = (
			      SELECT cmb.provider_model_id
			      FROM credential_model_bindings cmb
			      WHERE cmb.credential_id = $1
			        AND cmb.available = FALSE
			        AND cmb.unavailable_reason = 'continuous_failure'
			        AND cmb.unavailable_at = $3
			  )
			  AND mo.credential_id = $1
			  AND mo.raw_model_name = $2
			  AND mo.available = TRUE
			  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		`, credentialID, rawModel, now); moErr != nil {
			slog.Warn("checker: model_offers mirror write failed",
				"credential_id", credentialID, "model", model, "error", moErr)
		}

		if c.invalidateCache != nil {
			c.invalidateCache(credentialID)
		}
	}

	slog.Warn("credential binding marked degraded due to continuous failures",
		"credential_id", credentialID,
		"model", model,
		"failure_rate", rate,
		"sample_size", sampleSize,
		"error_kinds", kinds,
		"dominant_kind", dominantKind,
		"cooldown", cooldown.String(),
		"recover_at", recoverAt,
		"window", c.windowDuration,
		"rows_affected", tag.RowsAffected())

	return nil
}

// RecoverExpired checks for expired degraded credentials and restores them.
// This is called by the background health_auto_recover worker.
//
// It restores THREE state surfaces in the same call:
//  1. credential_model_bindings  (production router's source of truth)
//  2. model_offers               (/api/routing/resolve "test route" + admin UI)
//  3. credentials.availability_state  (the candidate loader's v_routable filter)
//
// 2026-06-23 (PR-3 T3): also clear credentials.availability_state in the same
// tick. Without this, a credential whose availability_recover_at has passed
// would still show is_routable=FALSE in the candidate loader for up to 60s
// (until the next bg/credential_recovery.go tick), producing the
// "cmb=TRUE but availability=cooling" false negative that hid the cred-11/
// minimax-m3 incident. Mirrors the SQL in bg/credential_recovery.go:recover()
// for defence-in-depth: if either worker tick is delayed, the other still
// covers the recovery.
//
// Historical note: the previous version of this function deliberately did
// NOT touch availability_state, on the assumption that the router reads
// only cmb. That assumption was wrong — v_routable_credential_models.is_routable
// also requires availability_state='ready' (see 2026-06-22 defect 4).
func RecoverExpired(ctx context.Context, db DBQuerier) (int, error) {
	cmbTag, err := db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available              = TRUE,
		    unavailable_reason     = NULL,
		    unavailable_at         = NULL,
		    unavailable_recover_at = NULL,
		    updated_at             = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND cmb.unavailable_reason <> 'model_probe_broken'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_recover_at,
		               cmb.unavailable_at + INTERVAL '30 seconds') IS NOT NULL
		  AND COALESCE(cmb.unavailable_recover_at,
		               cmb.unavailable_at + INTERVAL '30 seconds') < now()
	`)
	if err != nil {
		return 0, fmt.Errorf("recover expired credential_model_bindings: %w", err)
	}

	// Mirror to model_offers in the same tick so /api/routing/resolve
	// reflects the recovery immediately. Skip rows that are admin-pinned
	// (cmb.unavailable_reason LIKE 'manual%' was preserved on the cmb
	// side, so any model_offers row with reason LIKE 'manual%' is also
	// pinned here).
	//
	// NOTE: model_offers is a VIEW, so it doesn't have an updated_at column.
	// The underlying credential_model_bindings.updated_at was already set above.
	//
	// 2026-08-11 fix: the previous WHERE used
	//   COALESCE(unavailable_at + 30s, now()+1h) < now()
	// which (since unavailable_at is non-null) reduced to
	//   unavailable_at + 30s < now()
	// i.e. recover 30s after the degradation. But the cmb path above
	// recovers at unavailable_recover_at = now()+degradedCooldown (15min).
	// model_offers has an INSTEAD OF UPDATE trigger that writes the recovery
	// back to cmb (clearing unavailable_reason/unavailable_at), so the
	// premature 30s recovery also silently re-armed the binding on cmb —
	// the 15min cooldown was effectively bypassed. The view exposes
	// unavailable_recover_at, so mirror the same condition the cmb UPDATE uses.
	moTag, err := db.Exec(ctx, `
		UPDATE model_offers mo
		SET available          = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at     = NULL
		WHERE mo.available = FALSE
		  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
		  AND mo.unavailable_reason <> 'model_probe_broken'
		  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		  AND COALESCE(mo.unavailable_recover_at,
		               mo.unavailable_at + INTERVAL '30 seconds') < now()
	`)
	if err != nil {
		return int(cmbTag.RowsAffected()), fmt.Errorf("recover expired model_offers: %w", err)
	}

	// ALSO clear credentials.availability_state when its recover_at has
	// passed. Same broken_confirmed guard as bg/credential_recovery.go:
	// do not auto-restore a credential that the probe worker has marked
	// permanently broken — the probe worker only un-marks a binding via
	// a manual nudge (TriggerManual), so guarding here cannot strand a
	// credential that has actually recovered.
	// 2026-08-07 P0 死锁修复：与 bg/credential_recovery.go 保持一致地
	// 认领 'suspended'。此前两处恢复路径都不含 suspended，导致
	// quota_periodic 写入的 suspended 无法自动恢复（详见
	// bg/credential_recovery.go 同名修复的注释与 154 生产 cred 22 证据）。
	//
	// suspended 的守卫比其它状态严格：
	//   1. 必须有已到期的 availability_recover_at（下方 IS NOT NULL 已保证）。
	//      auth_revoked / quota_balance 写 NULL，因此不会被误救。
	//   2. 硬配额（余额/永久用尽）仍未解除时不放行；那类凭据只能由
	//      balance_quota_probe 探活成功后经 probe writeHealth 翻回。
	credTag, err := db.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'ready',
		    availability_recover_at = NULL,
		    state_updated_at        = now()
		WHERE availability_state IN ('cooling','rate_limited','unreachable','suspended')
		  AND availability_recover_at IS NOT NULL
		  AND availability_recover_at <= now()
		  AND (
		      availability_state <> 'suspended'
		      OR COALESCE(quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  )
		  AND lifecycle_status = 'active'
			AND NOT (
			    -- Match bg/credential_recovery.go: only block credential-level
			    -- recovery when every bound model is broken and unavailable.
			    (SELECT COUNT(*)
			     FROM model_probe_state mps
			     JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
			     JOIN credential_model_bindings cmb
			          ON cmb.credential_id = mps.credential_id
			         AND cmb.provider_model_id = pm.id
			     WHERE mps.credential_id = credentials.id
			       AND mps.state = 'broken_confirmed'
			       AND cmb.available = FALSE)
			    =
			    (SELECT COUNT(*)
			     FROM credential_model_bindings
			     WHERE credential_id = credentials.id)
			    AND (SELECT COUNT(*)
			         FROM credential_model_bindings
			         WHERE credential_id = credentials.id) > 0
			)

	`)
	if err != nil {
		slog.Warn("availability_state recovery in RecoverExpired failed", "error", err)
	}

	rowsAffected := int(cmbTag.RowsAffected())
	if rowsAffected > 0 {
		slog.Info("auto recovered expired bindings",
			"cmb_count", cmbTag.RowsAffected(),
			"model_offers_count", moTag.RowsAffected(),
			"credentials_availability_count", credTag.RowsAffected(),
		)
	}

	return rowsAffected, nil
}
