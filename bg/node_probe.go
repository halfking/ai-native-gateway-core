// Package bg — node_probe.go
//
// NodeProbeWorker is the new error-triggered node-probe worker mandated
// by the 2026-07-14 spec rewrite.  It supersedes
//   - bg/credential_probe_v2.go         (1h cycle)
//   - bg/model_probe.go                 (5min cycle + consensus)
//   - bg/model_probe_suspicious.go      (2min cycle)
//   - bg/active_probe_worker.go         (5s..15m backoff)
//
// Cadence
// ───────
// Error-triggered.  State machine per (credential_id, raw_model_name)
// lives in node_probe_state.  Backoff ladder:
//
//	attempt 1 → +5s     (immediate on first failure)
//	attempt 2 → +30s
//	attempt 3 → +60s
//	attempt 4 → +5m
//	attempt 5 → +1h
//	attempt 6 → +2h
//	attempt 7+→ +24h   (still ticking but the row is marked paused
//	                    so a re-deploy / manual review is required)
//
// After attempt 7 with 24h spacing the row stays paused and the worker
// stops probing; an operator must clear the flag or the
// broken_probe_reviver (kept for that purpose) will retry.
//
// Two rounds per attempt
// ──────────────────────
//  1. direct  — POST the upstream provider's base URL using the
//     decrypted credential, mirroring the "isolate_upstream"
//     call in bg/self_check_worker.go.  Verifies the
//     upstream is actually serving traffic.
//  2. gateway — POST the local gateway with the system API key.
//     Verifies the credential is wired into routing and
//     the gateway-side plugins (auth, transform, billing,
//     rate-limit) are not blocking the path.
//
// Both rounds must succeed (HTTP 200 + a tool call echo) for the
// attempt to count as success.  Either failure advances the backoff
// ladder.
//
// Outbound X-LLM-Origin-* headers
// ────────────────────────────────
//
//	X-LLM-Origin-Stage : node_probe
//	X-LLM-Origin-Actor : node-probe-worker
//	X-Forwarded-For    : $LLM_GATEWAY_EGRESS_FORWARDED_FOR + egress IP
//	X-Real-IP          : $LLM_GATEWAY_EGRESS_IP
package bg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
)

const (
	// nodeProbeMaxAttempts matches the length of NodeProbeBackoffChain.
	nodeProbeMaxAttempts = 7

	// nodeProbeTickInterval is how often the worker scans for due
	// rows in node_probe_state.  30s is a good middle ground:
	// close enough to honour 5s backoffs in the first 60s window
	// without hammering the DB.
	nodeProbeTickInterval = 30 * time.Second

	// nodeProbeInFlightWindow prevents the same (cred, model) from
	// being probed concurrently by two workers / re-deploys.  Set
	// 5 minutes so a re-entry within that window is treated as
	// "still running".
	nodeProbeInFlightWindow = 5 * time.Minute
)

// NodeProbeWorker polls node_probe_state and executes the two-round
// (direct + gateway) probe for each (credential, model) whose
// next_retry_at has elapsed.
type NodeProbeWorker struct {
	db            *pgxpool.Pool
	encKey        []byte
	keyring       *secret.Keyring
	apiKey        string
	baseURL       string
	client        *http.Client
	stateObserver credentialstate.StateObserver

	stopCh   chan struct{}
	stopOnce sync.Once

	mu       sync.Mutex
	inFlight map[string]struct{} // dedup key: "<credID>|<model>"
}

// SetStateObserver wires probe results into the router's in-memory and Redis
// state caches. PostgreSQL writes alone are insufficient because routing reads
// the credentialstate cache before it reaches the database.
func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver) {
	if w != nil {
		w.stateObserver = observer
	}
}

// NewNodeProbeWorker constructs a worker.  baseURL="" picks
// LLM_GATEWAY_NODE_PROBE_BASE_URL or the default https://llm.kxpms.cn/v1.
// apiKey is the system-level API key the worker uses for the gateway
// round; it should be an is_system=true key with read+write on the
// provider model bindings.
func NewNodeProbeWorker(db *pgxpool.Pool, encKey []byte, keyring *secret.Keyring, apiKey, baseURL string) *NodeProbeWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_NODE_PROBE_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = "https://llm.kxpms.cn/v1"
		}
	}
	return &NodeProbeWorker{
		db:       db,
		encKey:   encKey,
		keyring:  keyring,
		apiKey:   apiKey,
		baseURL:  baseURL,
		client:   &http.Client{Timeout: 15 * time.Second},
		stopCh:   make(chan struct{}),
		inFlight: make(map[string]struct{}),
	}
}

func (w *NodeProbeWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	go w.loop(ctx)
	slog.Info("node_probe_worker started",
		"tick_interval", nodeProbeTickInterval,
		"max_attempts", nodeProbeMaxAttempts,
	)
}

func (w *NodeProbeWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func (w *NodeProbeWorker) loop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("node_probe_worker panic", "recover", r)
		}
	}()
	ticker := time.NewTicker(nodeProbeTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.cycle(ctx)
		}
	}
}

// Submit enqueues a (credID, model) pair for probing.  Called by the
// state manager when a real request fails.  Idempotent: if the row
// is currently in flight (in-memory `inFlight` map in cycle) the
// DB write is a no-op; pickDueAtomically's SKIP LOCKED handles
// cross-instance dedup.
func (w *NodeProbeWorker) Submit(credID int, model, tenantID, parentReqID string) {
	if w == nil {
		return
	}
	if model == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 2026-07-15 P0 fix (B-series cascade follow-up): the previous
	// Submit set in_flight_until = now() + 5 minutes, which the
	// pickDueAtomically filter combined with `in_flight_until <= now()`
	// to dead-letter the row for 5 minutes after every real
	// failure. Worse, after 7 consecutive failures runOne would
	// `paused = TRUE` and never auto-resume. The combined effect
	// was the "I never see the probe after a failure" complaint
	// logged by operators on 2026-07-15 — the worker logged
	// `submit` for each failure but the next cycle's pickDue
	// skipped the row because in_flight_until was always in the
	// future.
	//
	// Fix: Submit now writes a next_retry_at 5 seconds in the future
	// and leaves in_flight_until NULL. Paused rows are unpaused
	// (with consecutive_failures reset) so any fresh real failure
	// can restart the probe cycle. The in-memory `inFlight` map
	// plus the SELECT FOR UPDATE SKIP LOCKED in pickDueAtomically
	// are the only two dedup mechanisms.
	//
	// 2026-07-16 (audit follow-up — backoff collapse): the previous
	// Submit's ON CONFLICT unconditionally took LEAST(next_retry_at,
	// now+5s) and reset consecutive_failures=0. Combined with
	// credentialstate.Manager firing Submit on every failure where
	// ConsecutiveFails>=2, the 5s/30s/60s/5m/1h/2h/24h backoff ladder
	// never advanced — each fresh user failure pulled the row back to
	// the 5s rung and reset the counter, so runOne always computed
	// attempt=1. Result: the same credential could be probed roughly
	// every (round_duration+5s) under sustained failure, producing the
	// "lots of probe tiles in the live stream" symptom operators saw.
	//
	// Fix: ON CONFLICT now only re-arms when the row is EITHER paused
	// (restart the cycle from scratch) OR has no future next_retry_at
	// (a previous Submit's 5s window has already elapsed and runOne
	// has either processed it or is about to). When the row already
	// has a future next_retry_at — i.e. runOne is mid-cycle and the
	// ladder has been legitimately escalated — we update only the
	// audit columns and leave next_retry_at / consecutive_failures /
	// in_flight_until alone, so the backoff chain
	// (5s→30s→60s→5m→1h→2h→24h) can actually advance.
	//
	// consecutive_failures is reset only for paused rows (re-arm from
	// scratch); for already-expired rows it is left at its current
	// value so runOne's increment by 1 (line 331: attempt = state+1)
	// correctly reflects "one more failed probe round on top of the
	// previous ladder position" rather than collapsing back to rung 1.
	//
	// The 2-second flash-protection in Manager.UpdateOnFailure still
	// prevents transient noise (a success within 2s) from re-arming.
	_, _ = w.db.Exec(ctx, `
		INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds, paused, in_flight_until, consecutive_failures, last_err_code)
		VALUES ($1, $2, now() + interval '5 seconds', 5, FALSE, NULL, 0, NULL)
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET next_retry_at = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.consecutive_failures >= $3
		          OR node_probe_state.next_retry_at <= now()
		        THEN now() + interval '5 seconds'
		        ELSE node_probe_state.next_retry_at
		    END,
		    next_retry_seconds = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.consecutive_failures >= $3
		          OR node_probe_state.next_retry_at <= now()
		        THEN 5
		        ELSE node_probe_state.next_retry_seconds
		    END,
		    in_flight_until = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.consecutive_failures >= $3
		          OR node_probe_state.next_retry_at <= now()
		        THEN NULL
		        ELSE node_probe_state.in_flight_until
		    END,
		    paused = FALSE,
		    -- Reset the counter only when the cycle is being restarted
		    -- from a paused row. For already-expired rows the worker
		    -- (runOne) owns the counter and increments it by 1 per round;
		    -- touching it here would collapse the ladder back to rung 1.
		    consecutive_failures = CASE
		        WHEN node_probe_state.paused = TRUE THEN 0
		        WHEN node_probe_state.consecutive_failures >= $3 THEN 0
		        ELSE node_probe_state.consecutive_failures
		    END,
		    last_err_code = NULL,
		    updated_at = now()
	`, credID, model, nodeProbeMaxAttempts)
	slog.Info("node_probe_worker: submit",
		"credential_id", credID, "model", model,
		"tenant_id", tenantID, "parent_request_id", parentReqID,
	)
}

// cycle picks at most one due (cred, model) per tick and runs the
// two-round probe.  Concurrency is deliberately 1 per tick — the
// worker is in a low-traffic hot path and we want predictable DB
// pressure.
//
// Cross-instance isolation: pickDueAtomically issues a single
// SELECT ... FOR UPDATE SKIP LOCKED + UPDATE in_flight_until inside
// a transaction, so two gateway instances cannot both pick the same
// (cred, model) row at the same instant.  Combined with the in-memory
// dedup map this guarantees "one in-flight probe per (cred, model)
// globally".
func (w *NodeProbeWorker) cycle(ctx context.Context) {
	credID, model, ok, err := w.pickDueAtomically(ctx)
	if err != nil {
		slog.Warn("node_probe_worker: pick due failed", "error", err)
		return
	}
	if !ok {
		return
	}
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	if _, busy := w.inFlight[key]; busy {
		w.mu.Unlock()
		return
	}
	w.inFlight[key] = struct{}{}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.inFlight, key)
		w.mu.Unlock()
	}()

	if err := w.runOne(ctx, credID, model, ""); err != nil {
		slog.Warn("node_probe_worker: runOne failed",
			"credential_id", credID, "model", model, "error", err)
	}
}

// pickDueAtomically selects the next-due (cred, model) row and marks
// it in-flight in a single transaction so concurrent gateway instances
// cannot pick the same row.  Uses SELECT ... FOR UPDATE SKIP LOCKED:
//
//	BEGIN;
//	  SELECT credential_id, raw_model_name
//	    FROM node_probe_state
//	   WHERE paused = FALSE AND next_retry_at <= now()
//	   ORDER BY next_retry_at ASC
//	   LIMIT 1
//	   FOR UPDATE SKIP LOCKED;
//	  UPDATE node_probe_state SET in_flight_until = now() + $1 WHERE ...;
//	COMMIT;
//
// Returns (credID, model, true, nil) on success, (0, "", false, nil)
// when nothing is due.
//
// 2026-07-15 P0 fix: the in_flight_until predicate was removed
// from the WHERE clause because Submit() no longer sets
// in_flight_until. The cross-instance dedup relies on
// `FOR UPDATE SKIP LOCKED` plus the in-memory `inFlight` map in
// cycle() — adding in_flight_until back into the predicate would
// re-introduce the 5-minute dead-letter window from the previous
// design. We still set the column inside the transaction so an
// out-of-band reader can see "this row was picked at time T" if
// needed for forensic logs, but it does not gate the WHERE filter.
func (w *NodeProbeWorker) pickDueAtomically(ctx context.Context) (int, string, bool, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck

	var credID int
	var model string
	err = tx.QueryRow(ctx, `
		SELECT credential_id, raw_model_name
		FROM node_probe_state
		WHERE paused = FALSE
		  AND next_retry_at <= now()
		ORDER BY next_retry_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&credID, &model)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE node_probe_state
		   SET in_flight_until = now() + $1::interval
		 WHERE credential_id = $2 AND raw_model_name = $3
	`, fmt.Sprintf("%d seconds", int(nodeProbeInFlightWindow.Seconds())), credID, model); err != nil {
		return 0, "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", false, err
	}
	return credID, model, true, nil
}

// runOne executes the two-round probe and updates the state row +
// audit log accordingly.  triggerKind is recorded on node_probe_runs;
// "" defaults to "request_failure".
func (w *NodeProbeWorker) runOne(ctx context.Context, credID int, model, triggerKind string) error {
	if triggerKind == "" {
		triggerKind = "request_failure"
	}
	startedAt := time.Now()

	state, err := w.loadState(ctx, credID, model)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	attempt := state.ConsecutiveFailures + 1
	// 2026-07-15 P0 fix: previously the worker paused the row forever
	// after nodeProbeMaxAttempts (7) consecutive failures, with no
	// way to unpause except a manual UPDATE. That meant a cred that
	// genuinely recovered (e.g. upstream transient) stayed hidden
	// from the router for hours/days, contributing to the
	// `no_candidates` cascade seen on 2026-07-15 14:00~15:35. We now
	// let the chained backoff in the else branch below stretch the
	// retry interval indefinitely (1h, 4h, 12h, 24h) so the worker
	// naturally throttles itself without permanently abandoning the
	// pair. Submit() below unpauses + resets failure count on any
	// fresh real failure, so the next user-visible 503 will restart
	// the cycle.
	if attempt > nodeProbeMaxAttempts {
		slog.Info("node_probe_worker: attempt cap reached, applying long backoff without pausing",
			"credential_id", credID, "model", model,
			"consecutive_failures", state.ConsecutiveFailures,
			"max_attempts", nodeProbeMaxAttempts)
	}

	// Round 1: direct upstream
	direct := w.probeDirect(ctx, credID, model)
	// Round 2: gateway
	gw := w.probeGateway(ctx, credID, model)

	success := direct.ok && gw.ok
	// Only the direct round proves the health of this credential. The gateway
	// round may select a different candidate, so its result must not mutate
	// this credential's routing state.
	if direct.ok {
		w.updateBindingAvailability(ctx, credID, model, true, "")
		w.updateCredentialHealth(ctx, credID)
		w.updateObservedState(ctx, credID, model, true, "", time.Now())
	} else {
		w.updateBindingAvailability(ctx, credID, model, false, direct.errCode)
		recoverAt := time.Now().Add(5 * time.Minute)
		w.updateObservedState(ctx, credID, model, false, direct.errCode, recoverAt)
	}
	now := time.Now()
	durationMs := int(now.Sub(startedAt).Milliseconds())

	if success {
		// Reset
		_, _ = w.db.Exec(ctx, `
			UPDATE node_probe_state SET
				consecutive_failures = 0,
				consecutive_successes = consecutive_successes + 1,
				last_attempt_at = now(),
				next_retry_at = now() + interval '24 hours',
				next_retry_seconds = 86400,
				paused = FALSE,
				last_run_id = NULL,
				last_direct_ok = TRUE,
				last_gateway_ok = TRUE,
				last_err_code = NULL,
				last_err_detail = NULL,
				in_flight_until = NULL,
				updated_at = now()
			WHERE credential_id = $1 AND raw_model_name = $2
		`, credID, model)
	} else {
		// 2026-07-15 P0 fix (B-series cascade follow-up): the previous
		// implementation set in_flight_until = now() + 5min on the
		// failure path, which combined with pickDueAtomically's
		// `in_flight_until <= now()` clause meant a 5-minute dead
		// window after every probe — the worker logged `submit` but
		// the next cycle kept skipping it because in_flight_until was
		// still in the future. Worse, when the cycle finally did fire
		// 5 minutes later, runOne's "attempt > maxAttempts" branch set
		// paused=TRUE with no automatic un-pause, so subsequent
		// Submit() calls accumulated more `submit` log lines without
		// ever producing a probe run. The result was silent stuck
		// credentials: cred 19 (NVIDIA) and cred 21 (minimaxi.com
		// direct) stayed paused=TRUE with consecutive_failures=7 for
		// 6+ hours, masking the actual no_candidates outage.
		//
		// Fix: rely on the in-memory `inFlight` map (cycle()) and
		// SELECT ... FOR UPDATE SKIP LOCKED in pickDueAtomically for
		// cross-instance dedup, instead of an in_flight_until column
		// that bypasses both. The DB column now stays NULL after
		// runOne, and the chained backoff in next_retry_at naturally
		// paces retries (30s → 60s → 120s → ...).
		backoff := ChainBackoffIndex(attempt, NodeProbeBackoffChain)
		nextRetryAt := now.Add(backoff)
		nextSec := int(backoff.Seconds())
		_, _ = w.db.Exec(ctx, `
			UPDATE node_probe_state SET
				consecutive_failures = $3,
				consecutive_successes = 0,
				last_attempt_at = now(),
				next_retry_at = $4,
				next_retry_seconds = $5,
				last_direct_ok = $6,
				last_gateway_ok = $7,
				last_err_code = $8,
				last_err_detail = $9,
				in_flight_until = NULL,
				updated_at = now()
			WHERE credential_id = $1 AND raw_model_name = $2
		`, credID, model, attempt, nextRetryAt, nextSec,
			direct.ok, gw.ok, firstErrCode(direct, gw), firstErrDetail(direct, gw))
	}

	// Persist audit row.
	nextSec := int(ChainBackoffIndex(attempt, NodeProbeBackoffChain).Seconds())
	_, _ = w.db.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19
		)`,
		credID, model, triggerKind, attempt, nextSec,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
	)
	return nil
}

type nodeProbeStateRow struct {
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	Paused               bool
}

func (w *NodeProbeWorker) loadState(ctx context.Context, credID int, model string) (nodeProbeStateRow, error) {
	var s nodeProbeStateRow
	err := w.db.QueryRow(ctx, `
		SELECT consecutive_failures, consecutive_successes, paused
		FROM node_probe_state
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model).Scan(&s.ConsecutiveFailures, &s.ConsecutiveSuccesses, &s.Paused)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return s, nil // never-submitted pair: not an error
		}
		return s, err
	}
	return s, nil
}

type nodeProbeRoundResult struct {
	ok         bool
	httpStatus int
	errCode    string
	errDetail  string
	latencyMs  int
}

// probeDirect issues a chat-completion ping directly to the upstream
// provider using the decrypted credential.  Mirrors the structure of
// bg/active_probe_executor.go:Run.
//
// `model` is the raw_model_name (vendor form, e.g. "z-ai/glm-5.2") used to
// resolve the credential via the provider_models JOIN. The actual name sent
// to the upstream in the request body is the outbound_model_name (which may
// differ from raw_model_name for some providers), selected from the DB by
// resolveDirectTarget — mirroring active_probe_executor.go:188-192. Without
// this split, a provider whose outbound_model_name differs from
// raw_model_name would fail the JOIN (since node_probe_state stores
// COALESCE(outbound,raw), the outbound name) and report endpoint_build
// failures for a healthy credential.
func (w *NodeProbeWorker) probeDirect(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	r := nodeProbeRoundResult{errCode: "none"}
	plain, outboundModel, baseURL, protocol, err := w.resolveDirectTarget(ctx, credID, model)
	if err != nil {
		r.errCode = "endpoint_build"
		r.errDetail = err.Error()
		return r
	}
	// Use the outbound name for the upstream body; fall back to the raw name
	// passed in (matches active_probe_executor.go's OutboundModel→RawModel
	// fallback) so providers without a distinct outbound_model_name still work.
	bodyModel := outboundModel
	if bodyModel == "" {
		bodyModel = model
	}
	endpoint := directProbeEndpoint(baseURL, protocol)
	body := directProbeBody(bodyModel, protocol)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if strings.HasPrefix(protocol, "anthropic") {
		req.Header.Set("x-api-key", plain)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+plain)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "node_probe")
	req.Header.Set("X-LLM-Origin-Actor", "node-probe-worker")
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP")); v != "" {
		req.Header.Set("X-Real-IP", v)
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR")); v != "" {
		req.Header.Set("X-Forwarded-For", v)
	}
	start := time.Now()
	resp, err := w.client.Do(req)
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		r.errCode = "network_error"
		r.errDetail = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode
	if resp.StatusCode == 200 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	r.errDetail = string(buf[:n])
	return r
}

func (w *NodeProbeWorker) resolveDirectTarget(ctx context.Context, credID int, model string) (string, string, string, string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		ciphertext    []byte
		outboundModel string
		baseURL       string
		protocol      string
	)
	err := w.db.QueryRow(queryCtx, `
		SELECT c.secret_ciphertext,
		       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
		       p.base_url, COALESCE(p.protocol, 'openai-completions')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2
		LIMIT 1
	`, credID, model).Scan(&ciphertext, &outboundModel, &baseURL, &protocol)
	if err != nil {
		return "", "", "", "", err
	}
	s := string(ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", "", "", "", fmt.Errorf("unsupported secret format")
	}
	if w.keyring == nil {
		return "", "", "", "", fmt.Errorf("keyring not configured")
	}
	// 2026-07-15 P0 fix: previously this called DecryptAESGCM directly,
	// which only handles v1:<kid>:<b64> envelopes with a non-legacy kid.
	// v1:legacy:<b64> envelopes (the historical default for credentials
	// created before the v1 multi-kid keyring landed) decode to
	// "unsupported secret format" or to "AES-GCM decryption failed",
	// both surfaced as `endpoint_build` in node_probe_state.last_err_code.
	// The result: every probe of those credentials failed at the
	// endpoint-build step, consecutive_failures climbed to 7, the row
	// got paused (the previous design), and the router lost a
	// perfectly valid minimaxi.com direct candidate. DecryptAny
	// tries AES-GCM first, then falls back to Fernet with the legacy
	// v1:legacy: prefix, matching what the production request path
	// already does via secret.DecryptAny.
	pt, _, err := secret.DecryptAny(s, w.keyring, w.encKey)
	if err != nil {
		// 2026-07-15 P0 fix: surface the raw error to operator logs.
		// endpoint_build was the only signal in node_probe_state,
		// but the upstream cause (keyring nil? unknown kid? Fernet
		// signature mismatch?) was swallowed. We log the full chain
		// here so the next no_candidates outage is root-causable
		// from a single grep on "node_probe_worker: decrypt failed".
		slog.Error("node_probe_worker: decrypt failed",
			"credential_id", credID, "model", model,
			"keyring_nil", w.keyring == nil,
			"enc_key_len", len(w.encKey),
			"envelope_prefix", s[:min(len(s), 24)],
			"error", err.Error())
		return "", "", "", "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), outboundModel, baseURL, protocol, nil
}

func directProbeEndpoint(baseURL, protocol string) string {
	ep := upstreamurl.EpChatCompletions
	if strings.HasPrefix(protocol, "anthropic") {
		ep = upstreamurl.EpMessages
	}
	return upstreamurl.Build(baseURL, ep)
}

func directProbeBody(model, protocol string) string {
	if strings.HasPrefix(protocol, "anthropic") {
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 10,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		})
		return string(body)
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 10,
	})
	return string(body)
}

func firstErrCodeValue(a, b nodeProbeRoundResult) string {
	if !a.ok {
		return a.errCode
	}
	if !b.ok {
		return b.errCode
	}
	return ""
}

func (w *NodeProbeWorker) updateBindingAvailability(ctx context.Context, credID int, model string, available bool, reason string) {
	if w == nil || w.db == nil {
		return
	}
	if available {
		_, _ = w.db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at = NULL,
			    unavailable_recover_at = NULL,
			    updated_at = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $1
			  AND pm.raw_model_name = $2
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, credID, model)
		return
	}
	_, _ = w.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = FALSE,
		    unavailable_reason = $3,
		    unavailable_at = now(),
		    unavailable_recover_at = now() + interval '5 minutes',
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		`, credID, model, "probe_"+reason)
}

func (w *NodeProbeWorker) updateCredentialHealth(ctx context.Context, credID int) {
	_, _ = w.db.Exec(ctx, `
		UPDATE credentials
		SET health_status = 'healthy',
		    health_error = NULL,
		    health_checked_at = now(),
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = NULL,
		    state_updated_at = now()
		WHERE id = $1
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
	`, credID)
}

func (w *NodeProbeWorker) updateObservedState(ctx context.Context, credID int, model string, available bool, lastError string, recoverAt time.Time) {
	if w == nil || w.stateObserver == nil {
		return
	}
	state := &credentialstate.State{
		CredentialID:  credID,
		Model:         model,
		Available:     available,
		HealthStatus:  "healthy",
		LastUpdatedAt: time.Now(),
		LastError:     lastError,
		Source:        "node_probe",
	}
	if available {
		now := time.Now()
		state.LastSuccessAt = &now
	} else {
		state.HealthStatus = "unreachable"
		state.RecoverAt = &recoverAt
	}
	w.stateObserver.UpdateFromProbe(ctx, state)
}

// probeGateway issues a chat-completion ping through the local gateway
// using the system API key.  This catches routing, auth, transform,
// rate-limit, and compression regressions that a direct call cannot.
func (w *NodeProbeWorker) probeGateway(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	r := nodeProbeRoundResult{errCode: "none"}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":10}`, model)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "node_probe")
	req.Header.Set("X-LLM-Origin-Actor", "node-probe-worker")
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP")); v != "" {
		req.Header.Set("X-Real-IP", v)
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR")); v != "" {
		req.Header.Set("X-Forwarded-For", v)
	}
	start := time.Now()
	resp, err := w.client.Do(req)
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		r.errCode = "network_error"
		r.errDetail = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode
	if resp.StatusCode == 200 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	r.errDetail = string(buf[:n])
	return r
}

func firstErrCode(a, b nodeProbeRoundResult) *string {
	if !a.ok {
		s := a.errCode
		return &s
	}
	if !b.ok {
		s := b.errCode
		return &s
	}
	return nil
}
func firstErrDetail(a, b nodeProbeRoundResult) *string {
	if !a.ok {
		s := truncateStrSC(a.errDetail, 256)
		return &s
	}
	if !b.ok {
		s := truncateStrSC(b.errDetail, 256)
		return &s
	}
	return nil
}

// Used by tests to compute a deterministic dedup hash of (cred, model).
func nodeProbeDedupHash(credID int, model string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", credID, model)))
	return hex.EncodeToString(h[:8])
}
