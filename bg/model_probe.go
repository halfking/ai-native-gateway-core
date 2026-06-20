// Package bg — model_probe.go
//
// ModelProbeRunner uses a CONSENSUS strategy with exponential backoff to
// flip credential×model bindings back to routable.
//
// State machine (per credential × model, stored in model_probe_state):
//
//   unknown  ─┐
//             │  first failure observed by the worker → 'recovering'
//   recovering  ◀──┘
//      │  consecutive successes → +1; backoff resets to 1m
//      │  consecutive failures  → reset successes, +1 failure, longer backoff
//      ↓
//   healthy_confirmed    (3 consecutive ok)  ← state flips here
//      │  any subsequent failure → back to 'recovering'
//      ↓
//   broken_confirmed     (3 consecutive fail) ← stops probing
//
// Backoff schedule (model_probe_backoff SQL function):
//   consecutive_failures = 0 → 1 min
//   consecutive_failures = 1 → 5 min
//   consecutive_failures = 2 → 15 min
//   consecutive_failures = 3 → 60 min (and stop — broken_confirmed)
//
// CRITICAL invariant: a model that's manually disabled NEVER gets
// auto-recovered.  The runner re-checks c.manual_disabled on every
// iteration and the SQL WHERE clause filters out manual bindings.
//
// Spec: 2026-06-18-model-probe-rounds (v2: consensus + backoff)
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

const (
	// RequiredConsensus is the number of consecutive successes (or
	// failures) needed before state flips.  Tuned by ops; do not
	// lower without re-reading spec §v2.
	RequiredConsensus = 3

	// MaxBatchPerCycle caps the number of probes per tick so a flood
	// of failures can't hammer the upstream all at once.
	MaxBatchPerCycle = 20

	// ProbeInterval is how often the cycle ticker fires.
	ProbeInterval = 10 * time.Minute
)

// ModelProbeRunner is the v2 (consensus + backoff) implementation.
type ModelProbeRunner struct {
	db      *pgxpool.Pool
	encKey  []byte
	keyring *secret.Keyring
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewModelProbeRunner(db *pgxpool.Pool, encKey []byte) *ModelProbeRunner {
	return &ModelProbeRunner{
		db:     db,
		encKey: encKey,
		done:   make(chan struct{}),
	}
}

func (r *ModelProbeRunner) SetKeyring(kr *secret.Keyring) { r.keyring = kr }

func (r *ModelProbeRunner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	// Layer 4: featured model deep ping every 30 minutes (v5, 2026-06-20)
	go r.featuredCycleLoop(ctx)
	slog.Info("model probe runner v2 (consensus+backoff) started",
		"interval", ProbeInterval,
		"required_consensus", RequiredConsensus,
		"max_batch", MaxBatchPerCycle,
	)
}

func (r *ModelProbeRunner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *ModelProbeRunner) run(ctx context.Context) {
	defer close(r.done)

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
func (r *ModelProbeRunner) cycle(ctx context.Context) {
	// v6 fix (2026-06-20): increased from 3min to 10min to accommodate
	// multiple probe retries (4 attempts × 55s backoff = ~70s per probe).
	// A batch of 10 models could take 10+ minutes with retries.
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	rows, err := r.db.Query(timeoutCtx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(mps.state, 'unknown'), COALESCE(mps.consecutive_successes, 0),
		       COALESCE(mps.consecutive_failures, 0)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
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
		  AND COALESCE(cmb.unavailable_reason, '') <> 'manual'
		  AND (
		      COALESCE(v.is_routable, FALSE) = FALSE
		      OR mps.state = 'recovering'
		  )
		  AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed'
		  AND (mps.next_retry_at IS NULL OR mps.next_retry_at <= NOW())
		ORDER BY mps.next_retry_at NULLS FIRST, cmb.id
		LIMIT $1
	`, MaxBatchPerCycle)
	if err != nil {
		slog.Warn("model probe v2: target query failed", "error", err)
		return
	}
	defer rows.Close()

	var due []queued
	for rows.Next() {
		var q queued
		var ciphertext []byte
		if err := rows.Scan(
			&q.t.CredentialID, &q.t.RawModel, &q.t.OutboundModel,
			&q.t.BaseURL, &q.t.Protocol,
			&ciphertext, &q.t.ManualDisabled,
			&q.state, &q.succCnt, &q.failCnt,
		); err != nil {
			continue
		}
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

// featuredCycleLoop runs Layer 4 deep probe for featured models every 30min.
func (r *ModelProbeRunner) featuredCycleLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	time.Sleep(2 * time.Minute) // stagger: wait 2min then start
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
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		CROSS JOIN routing_policy pol
		WHERE pol.tenant_id = 'default'
		  AND (
		    COALESCE(pm.standardized_name, pm.raw_model_name) = ANY(pol.featured_models)
		    OR pm.raw_model_name = ANY(pol.featured_models)
		  )
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
	`)
	if err != nil {
		slog.Warn("featured cycle: query failed", "error", err)
		return
	}
	defer rows.Close()

	var tested int
	for rows.Next() {
		var t probeTarget
		var ciphertext []byte
		if err := rows.Scan(
			&t.CredentialID, &t.RawModel, &t.OutboundModel,
			&t.BaseURL, &t.Protocol, &ciphertext, &t.ManualDisabled,
		); err != nil {
			continue
		}
		if t.ManualDisabled {
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
func (r *ModelProbeRunner) applyResult(
	ctx context.Context, t probeTarget,
	status string, httpStatus *int, errCode, errMsg string, latencyMs int,
	stateChange string, applied bool, triggeredBy string,
	newSucc, newFail int, newState string,
) {

	var nextRetryExpr string
	switch newState {
	case "healthy_confirmed":
		// Watchdog: re-probe every 2h to catch silent regressions.
		nextRetryExpr = "NOW() + INTERVAL '2 hours'"
	case "broken_confirmed":
		// Stop probing; require operator to nudge.
		nextRetryExpr = "NOW() + INTERVAL '7 days'"
	default:
		// recovering: schedule next attempt per the backoff schedule.
		nextRetryExpr = "NOW() + model_probe_backoff($5)"
	}

	q := `
		INSERT INTO model_probe_state
		    (credential_id, raw_model_name, state,
		     consecutive_successes, consecutive_failures, total_attempts,
		     last_attempt_at, next_retry_at, last_status,
		     last_state_change_at, last_state_change_run)
		VALUES ($1, $2, $3, $4, $5, 1, NOW(), ` + nextRetryExpr + `, $6,
		        CASE WHEN $7 IN ('recovered','broke') THEN NOW() ELSE NULL END,
		        CASE WHEN $7 IN ('recovered','broke') THEN
		            (SELECT id FROM model_probe_runs
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
	if _, err := r.db.Exec(ctx, q,
		t.CredentialID, t.RawModel, newState,
		newSucc, newFail,
		status, stateChange,
	); err != nil {
		slog.Warn("model probe v2: applyResult failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"error", err)
	}

	// P4 (2026-06-19): propagate probe consensus to credential_model_bindings
	// so Path B (resolve.go) and Path C (admin/routing.go) also see the
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
		`, t.CredentialID, t.RawModel)
		if err != nil {
			slog.Warn("model probe: broken_confirmed binding update failed",
				"credential_id", t.CredentialID, "raw_model", t.RawModel, "error", err)
		} else {
			slog.Info("model probe: marked binding unavailable (broken_confirmed)",
				"credential_id", t.CredentialID, "raw_model", t.RawModel)
		}
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
		`, t.CredentialID, t.RawModel)
		if err != nil {
			slog.Warn("model probe: healthy_confirmed binding restore failed",
				"credential_id", t.CredentialID, "raw_model", t.RawModel, "error", err)
		} else {
			slog.Info("model probe: restored binding available (healthy_confirmed)",
				"credential_id", t.CredentialID, "raw_model", t.RawModel)
		}
	}
}

// recordRun inserts a row in model_probe_runs for traceability.
// Creates its own 5s timeout context to avoid inheriting expired parent contexts.
func (r *ModelProbeRunner) recordRun(
	ctx context.Context, t probeTarget,
	status string, httpStatus *int, errCode, errMsg string,
	latencyMs int, stateChange string, applied bool, triggeredBy string,
) {
	// Create a fresh 5s timeout for the DB write, independent of parent context.
	// This prevents "context deadline exceeded" when recordRun is called late
	// in a cycle that's approaching its 3-minute timeout.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	_, err := r.db.Exec(writeCtx, `
		INSERT INTO model_probe_runs
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
	probeCategoryOK              probeCategory = "ok"               // upstream responded successfully
	probeCategoryModelUnavailable probeCategory = "model_unavailable" // model genuinely not available (counts as failure)
	probeCategoryProviderError    probeCategory = "provider_error"   // provider-side issue (does NOT count as failure)
	probeCategorySkipped         probeCategory = "skipped"         // skipped (endpoint_id_required, etc.)
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
func (r *ModelProbeRunner) TriggerManual(ctx context.Context, credentialID int, rawModel string) error {
	row := r.db.QueryRow(ctx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(mps.state, 'unknown'), COALESCE(mps.consecutive_successes, 0),
		       COALESCE(mps.consecutive_failures, 0)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN model_probe_state mps
		       ON mps.credential_id = cmb.credential_id
		      AND mps.raw_model_name = pm.raw_model_name
		WHERE cmb.credential_id = $1 AND pm.raw_model_name = $2
		LIMIT 1
	`, credentialID, rawModel)
	var t probeTarget
	var ciphertext []byte
	var prevState string
	var prevSucc, prevFail int
	if err := row.Scan(&t.CredentialID, &t.RawModel, &t.OutboundModel, &t.BaseURL, &t.Protocol,
		&ciphertext, &t.ManualDisabled, &prevState, &prevSucc, &prevFail); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("binding not found")
		}
		return err
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
	stateChange, applied, newSucc, newFail, newState := r.computeConsensus(status, category, prevState, errCode, prevSucc, prevFail)
	r.recordRun(ctx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "manual")
	r.applyResult(ctx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "manual",
		newSucc, newFail, newState)
	return nil
}

// ProbeAllResult is the per-binding result returned by TriggerAllSync.
type ProbeAllResult struct {
	CredentialID  int    `json:"credential_id"`
	RawModel      string `json:"raw_model_name"`
	Status        string `json:"status"`   // ok, network, auth, http_4xx, http_5xx, skipped
	Category      string `json:"category"` // ok, model_unavailable, provider_error, skipped
	HTTPStatus    *int   `json:"http_status"`
	ErrorCode     string `json:"error_code"`
	ErrorMessage  string `json:"error_message"`
	LatencyMs     int    `json:"latency_ms"`
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
				CredentialID:  t.CredentialID,
				RawModel:      t.RawModel,
				Status:        "auth",
				Category:      "provider_error",
				ErrorCode:     "decrypt_error",
				ErrorMessage:  decErr.Error(),
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
	BaseURL        string
	Protocol       string
	APIKey         string
	ManualDisabled bool
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
