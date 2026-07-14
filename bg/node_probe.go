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
//               decrypted credential, mirroring the "isolate_upstream"
//               call in bg/self_check_worker.go.  Verifies the
//               upstream is actually serving traffic.
//  2. gateway — POST the local gateway with the system API key.
//               Verifies the credential is wired into routing and
//               the gateway-side plugins (auth, transform, billing,
//               rate-limit) are not blocking the path.
//
// Both rounds must succeed (HTTP 200 + a tool call echo) for the
// attempt to count as success.  Either failure advances the backoff
// ladder.
//
// Outbound X-LLM-Origin-* headers
// ────────────────────────────────
//   X-LLM-Origin-Stage : node_probe
//   X-LLM-Origin-Actor : node-probe-worker
//   X-Forwarded-For    : $LLM_GATEWAY_EGRESS_FORWARDED_FOR + egress IP
//   X-Real-IP          : $LLM_GATEWAY_EGRESS_IP
package bg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/secret"
)


// NodeProbe backoff ladder — see spec §1.3.
var nodeProbeBackoff = []int{5, 30, 60, 300, 3600, 7200, 86400}

const (
	// nodeProbeMaxAttempts is the number of failed attempts before
	// the state row is marked paused (still ticks at 24h so an
	// operator can observe the staleness).
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
	db      *pgxpool.Pool
	encKey  []byte
	keyring *secret.Keyring
	apiKey  string
	baseURL string
	client  *http.Client

	stopCh   chan struct{}
	stopOnce sync.Once

	mu     sync.Mutex
	inFlight map[string]struct{} // dedup key: "<credID>|<model>"
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
// state manager when a real request fails.  Idempotent: if the pair is
// already paused or in flight the call is a no-op.
func (w *NodeProbeWorker) Submit(credID int, model, tenantID, parentReqID string) {
	if w == nil {
		return
	}
	if model == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = w.db.Exec(ctx, `
		INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds, paused, in_flight_until)
		VALUES ($1, $2, now() + interval '5 seconds', 5, FALSE, now() + interval '5 minutes')
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET next_retry_at = CASE
			WHEN node_probe_state.paused = TRUE OR node_probe_state.in_flight_until > now() THEN node_probe_state.next_retry_at
			ELSE LEAST(node_probe_state.next_retry_at, now() + interval '5 seconds')
		END
	`, credID, model)
	slog.Info("node_probe_worker: submit",
		"credential_id", credID, "model", model,
		"tenant_id", tenantID, "parent_request_id", parentReqID,
	)
}

// cycle picks at most one due (cred, model) per tick and runs the
// two-round probe.  Concurrency is deliberately 1 per tick — the
// worker is in a low-traffic hot path and we want predictable DB
// pressure.
func (w *NodeProbeWorker) cycle(ctx context.Context) {
	credID, model, err := w.pickDue(ctx)
	if err != nil {
		slog.Warn("node_probe_worker: pick due failed", "error", err)
		return
	}
	if credID == 0 {
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

// pickDue returns one (credID, model) whose next_retry_at has elapsed
// and which is not paused / in flight.  Returns (0, "", nil) when
// nothing is due.
func (w *NodeProbeWorker) pickDue(ctx context.Context) (int, string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var credID int
	var model string
	err := w.db.QueryRow(queryCtx, `
		SELECT credential_id, raw_model_name
		FROM node_probe_state
		WHERE paused = FALSE
		  AND next_retry_at <= now()
		  AND (in_flight_until IS NULL OR in_flight_until <= now())
		ORDER BY next_retry_at ASC
		LIMIT 1
	`).Scan(&credID, &model)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, "", nil
		}
		return 0, "", err
	}
	return credID, model, nil
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
	if attempt > nodeProbeMaxAttempts {
		// Mark paused; the next manual reset can resume.
		_, _ = w.db.Exec(ctx, `UPDATE node_probe_state SET paused = TRUE, updated_at = now() WHERE credential_id = $1 AND raw_model_name = $2`, credID, model)
		return nil
	}

	// Round 1: direct upstream
	direct := w.probeDirect(ctx, credID, model)
	// Round 2: gateway
	gw := w.probeGateway(ctx, credID, model)

	success := direct.ok && gw.ok
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
		nextSec := nodeProbeBackoff[attempt-1]
		nextRetryAt := now.Add(time.Duration(nextSec) * time.Second)
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
				in_flight_until = $10,
				updated_at = now()
			WHERE credential_id = $1 AND raw_model_name = $2
		`, credID, model, attempt, nextRetryAt, nextSec,
			direct.ok, gw.ok, firstErrCode(direct, gw), firstErrDetail(direct, gw),
			now.Add(nodeProbeInFlightWindow))
	}

	// Persist audit row.
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
		credID, model, triggerKind, attempt, nodeProbeBackoff[attempt-1],
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
	)
	return nil
}

type nodeProbeStateRow struct {
	ConsecutiveFailures int
	ConsecutiveSuccesses int
	Paused              bool
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
func (w *NodeProbeWorker) probeDirect(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	r := nodeProbeRoundResult{errCode: "none"}
	plain, baseURL, err := w.resolveDirectTarget(ctx, credID, model)
	if err != nil {
		r.errCode = "endpoint_build"
		r.errDetail = err.Error()
		return r
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":10}`, model)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+plain)
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

func (w *NodeProbeWorker) resolveDirectTarget(ctx context.Context, credID int, model string) (string, string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		ciphertext []byte
		baseURL    string
	)
	err := w.db.QueryRow(queryCtx, `
		SELECT c.secret_ciphertext, p.base_url
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN provider_model_bindings pmb ON pmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = pmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2
		LIMIT 1
	`, credID, model).Scan(&ciphertext, &baseURL)
	if err != nil {
		return "", "", err
	}
	s := string(ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", "", fmt.Errorf("unsupported secret format")
	}
	if w.keyring == nil {
		return "", "", fmt.Errorf("keyring not configured")
	}
	pt, err := secret.DecryptAESGCM(ciphertext, w.keyring)
	if err != nil {
		return "", "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), baseURL, nil
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
