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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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

	// 30s is a safety-net scan; Submit also wakes the worker immediately.
	nodeProbeTickInterval = 30 * time.Second
	// A bounded batch keeps a large outage from monopolizing the worker.
	nodeProbeBatchSize = 8

	// nodeProbeInFlightWindow prevents the same (cred, model) from
	// being probed concurrently by two workers / re-deploys.  Set
	// 5 minutes so a re-entry within that window is treated as
	// "still running".
	nodeProbeInFlightWindow = 5 * time.Minute

	// nodeProbeSyncFanout caps how many concurrent (cred, model)
	// direct probes ProbeSync runs in parallel. Mirrors
	// nodeProbeBatchSize so a no_candidate burst does not multiply
	// upstream pressure beyond what the background worker would
	// produce.
	nodeProbeSyncFanout = 8
)

// NodeProbeWorker polls node_probe_state and executes the two-round
// (direct + gateway) probe for each (credential, model) whose
// next_retry_at has elapsed.
//
// probeDirect and probeGateway use separate HTTP clients:
//   - probeClient — respects the upstream HTTP_PROXY via ProxyFunc,
//     matching the path real user requests take through the gateway.
//   - client — direct (no proxy), used only for the gateway round
//     which hits the local gateway API endpoint.
//
// 2026-07-16 fix: probeDirect previously used the direct client (no proxy),
// which bypassed the proxy resolver and reported false-positive recoveries
// when the proxy was actually broken. Credentials oscillated between
// "available" (probeDirect through proxy-less client → OK) and
// "unavailable" (real requests through proxy → timeout), producing the
// "models briefly work then 5xx" pattern.
type NodeProbeWorker struct {
	db            *pgxpool.Pool
	encKey        []byte
	keyring       *secret.Keyring
	apiKey        string
	baseURL       string
	client        *http.Client // direct (no proxy), for probeGateway
	probeClient   *http.Client // proxy-respecting, for probeDirect
	stateObserver credentialstate.StateObserver
	// stateProvider (2026-07-17) is the READ-side contract used by
	// ProbeSync's reuse path: after a syncWaiter channel closes, we
	// re-check IsAvailable for that (cred,model) so the function can
	// report recovery when cycle() (or another fresh-job goroutine)
	// wrote availability=true. Without this the reuse path always
	// returns false even when the pair did recover, producing a
	// spurious 503. May be nil — in that case reuse-path callers fall
	// back to ctx-driven 503.
	stateProvider credentialstate.StateProvider
	emitter       *ActiveProbeEmitter

	// Candidate cache invalidation keeps a direct probe result visible to the
	// next routing decision instead of waiting for the provider cache TTL.
	invalidateCandidateCache func(credentialID int)
	// recordCircuitSuccess closes the in-memory breaker as soon as the direct
	// provider probe confirms recovery.
	recordCircuitSuccess func(providerID, credentialID int)

	stopCh   chan struct{}
	stopOnce sync.Once
	wakeCh   chan struct{}

	mu         sync.Mutex
	inFlight   map[string]struct{} // dedup key: "<credID>|<model>"
	triggers   map[string]nodeProbeTrigger
	wakeTimers map[string]*time.Timer

	// syncWaiters lets ProbeSync callers wait for an in-flight cycle()
	// run to finish instead of issuing a duplicate probe. Closed once
	// per (cred,model) at the end of runOne, AFTER the state-cache
	// writes so any waiter's subsequent re-PlanCandidates observes
	// the recovered availability.
	syncWaitersMu sync.Mutex
	syncWaiters   map[string][]chan struct{}
}

type nodeProbeTrigger struct {
	tenantID string
	parentID string
}

// SetStateObserver wires probe results into the router's in-memory and Redis
// state caches. PostgreSQL writes alone are insufficient because routing reads
// the credentialstate cache before it reaches the database.
func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver) {
	if w != nil {
		w.stateObserver = observer
	}
}

// SetStateProvider wires the READ-side cache so ProbeSync's reuse path
// can re-check IsAvailable after a syncWaiter channel closes. The same
// *credentialstate.Manager satisfies both StateObserver and
// StateProvider, so the same instance is passed to both setters.
func (w *NodeProbeWorker) SetStateProvider(provider credentialstate.StateProvider) {
	if w != nil {
		w.stateProvider = provider
	}
}

// SetEmitter makes node-probe rounds visible in the same request stream as
// legacy active probes. The worker remains usable without telemetry.
func (w *NodeProbeWorker) SetEmitter(emitter *ActiveProbeEmitter) {
	if w != nil {
		w.emitter = emitter
	}
}

// SetInvalidateCandidateCache wires the provider cache invalidator used after
// direct probe state changes.
func (w *NodeProbeWorker) SetInvalidateCandidateCache(fn func(credentialID int)) {
	if w != nil {
		w.invalidateCandidateCache = fn
	}
}

// SetCircuitRecovery wires the request-path circuit breaker recovery hook.
func (w *NodeProbeWorker) SetCircuitRecovery(fn func(providerID, credentialID int)) {
	if w != nil {
		w.recordCircuitSuccess = fn
	}
}

// NewNodeProbeWorker constructs a worker.  baseURL="" picks
// LLM_GATEWAY_NODE_PROBE_BASE_URL or the default https://llm.kxpms.cn/v1.
// apiKey is the system-level API key the worker uses for the gateway
// round; it should be an is_system=true key with read+write on the
// provider model bindings.
//
// proxyFunc is optional.  When non-nil, the direct probe respects the
// upstream HTTP_PROXY configured in the environment so that the probe
// result accurately reflects what real user requests experience
// (instead of reporting a false-positive recovery by bypassing the
// proxy).  Pass upClient.Proxy().ProxyFunc() from main.go.
func NewNodeProbeWorker(db *pgxpool.Pool, encKey []byte, keyring *secret.Keyring, apiKey, baseURL string, proxyFunc func(*http.Request) (*url.URL, error)) *NodeProbeWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_NODE_PROBE_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = "https://llm.kxpms.cn/v1"
		}
	}
	w := &NodeProbeWorker{
		db:          db,
		encKey:      encKey,
		keyring:     keyring,
		apiKey:      apiKey,
		baseURL:     baseURL,
		client:      &http.Client{Timeout: 15 * time.Second},
		stopCh:      make(chan struct{}),
		wakeCh:      make(chan struct{}, 1),
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		wakeTimers:  make(map[string]*time.Timer),
		syncWaiters: make(map[string][]chan struct{}),
	}

	if proxyFunc != nil {
		w.probeClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy:                 proxyFunc,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		}
	} else {
		w.probeClient = w.client
	}

	return w
}

func (w *NodeProbeWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	w.resolveProbeAPIKey(ctx)
	go w.loop(ctx)
	slog.Info("node_probe_worker started",
		"tick_interval", nodeProbeTickInterval,
		"max_attempts", nodeProbeMaxAttempts,
		"api_key_resolved", w.apiKey != "",
	)
}

// resolveProbeAPIKey tries to find a system API key owned by the default
// tenant's admin user.  When found it replaces w.apiKey so the gateway
// probe round uses a real admin-owned key.  If nothing is found the
// caller-provided value (env var) is kept as-is.
func (w *NodeProbeWorker) resolveProbeAPIKey(ctx context.Context) {
	// The default tenant's admin login is "{tenant_code}user" — "defaultuser"
	// for the "default" tenant (see admin/password.go:DefaultTenantAdminUsername).
	adminUser := "defaultuser"

	qr := w.db.QueryRow(ctx, `
		SELECT key_ciphertext FROM api_keys
		WHERE COALESCE(is_system, FALSE) = TRUE AND status = 'active'
		  AND tenant_id = 'default' AND owner_user = $1
		ORDER BY created_at DESC LIMIT 1`, adminUser)

	var ciphertext string
	if err := qr.Scan(&ciphertext); err != nil {
		slog.Debug("node_probe_worker: no existing admin system key, keeping env key",
			"admin_user", adminUser, "error", err)
		return
	}
	pt, _, err := secret.DecryptAny(ciphertext, w.keyring, w.encKey)
	if err != nil {
		slog.Warn("node_probe_worker: admin system key exists but cannot decrypt, keeping env key",
			"admin_user", adminUser, "error", err)
		return
	}
	w.apiKey = string(pt)
	slog.Info("node_probe_worker: resolved probe API key from DB",
		"admin_user", adminUser)
}

func (w *NodeProbeWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.mu.Lock()
		for key, timer := range w.wakeTimers {
			if timer != nil {
				timer.Stop()
			}
			delete(w.wakeTimers, key)
		}
		w.mu.Unlock()
	})
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
			w.drainDue(ctx)
		case <-w.wakeCh:
			w.drainDue(ctx)
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
		          OR node_probe_state.next_retry_at <= now()
		        THEN now() + interval '5 seconds'
		        ELSE node_probe_state.next_retry_at
		    END,
		    next_retry_seconds = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.next_retry_at <= now()
		        THEN 5
		        ELSE node_probe_state.next_retry_seconds
		    END,
		    in_flight_until = CASE
		        WHEN node_probe_state.paused = TRUE
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
		        ELSE node_probe_state.consecutive_failures
		    END,
		    last_err_code = CASE
		        WHEN node_probe_state.paused = TRUE THEN NULL
		        ELSE node_probe_state.last_err_code
		    END,
		    updated_at = now()
	`, credID, model)
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	w.triggers[key] = nodeProbeTrigger{
		tenantID: tenantID,
		parentID: parentReqID,
	}
	if _, exists := w.wakeTimers[key]; !exists {
		w.wakeTimers[key] = time.AfterFunc(5*time.Second, func() {
			w.mu.Lock()
			delete(w.wakeTimers, key)
			w.mu.Unlock()
			nonBlockingWake(w.wakeCh)
		})
	}
	w.mu.Unlock()
	slog.Info("node_probe_worker: submit",
		"credential_id", credID, "model", model,
		"tenant_id", tenantID, "parent_request_id", parentReqID,
	)
}

func nonBlockingWake(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// MarkNodeProbeHealthy is the public hook for code paths outside
// NodeProbeWorker (e.g. the manual "全面探测" / TriggerAllSync flow)
// that successfully probe a (credential, model) pair and want the
// row in node_probe_state to immediately reflect healthy.
//
// Without this, the routing view v_routable_credential_models keeps
// excluding the binding until NodeProbeWorker's natural backoff
// ladder rolls over — up to 24h after the most recent failure, or
// indefinitely if paused=TRUE.
//
// The "next_retry_at = now() + 24h" choice mirrors runOne's success
// branch so the row's lifecycle remains identical to a naturally-
// recovered probe.
func MarkNodeProbeHealthy(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO node_probe_state (
			credential_id, raw_model_name,
			consecutive_failures, consecutive_successes,
			last_attempt_at, next_retry_at, next_retry_seconds,
			paused, in_flight_until,
			last_direct_ok, last_gateway_ok,
			last_err_code, last_err_detail,
			updated_at
		) VALUES ($1, $2, 0, 1, now(), now() + interval '24 hours', 86400, FALSE, NULL, TRUE, TRUE, NULL, NULL, now())
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET consecutive_failures = 0,
		    consecutive_successes = node_probe_state.consecutive_successes + 1,
		    last_attempt_at = now(),
		    next_retry_at = now() + interval '24 hours',
		    next_retry_seconds = 86400,
		    paused = FALSE,
		    in_flight_until = NULL,
		    last_direct_ok = TRUE,
		    last_gateway_ok = TRUE,
		    last_err_code = NULL,
		    last_err_detail = NULL,
		    updated_at = now()
	`, credentialID, rawModel)
	return err
}

// notifySyncWaiters closes every ProbeSync waiter channel registered
// for `key` and removes the entry. It is called from BOTH
// cycle() (after runOne completes for a background-triggered probe)
// AND ProbeSync's fresh-job goroutine (after the synchronous probe
// completes), so a concurrent ProbeSync caller that attached as a
// syncWaiter is released as soon as EITHER path finishes — it does
// not have to wait for ctx to expire.
//
// Closing happens AFTER the state-cache writes done inside runOne /
// the fresh-job body, so any waiter's subsequent re-PlanCandidates
// observes the recovered availability.
func (w *NodeProbeWorker) notifySyncWaiters(key string) {
	if w == nil {
		return
	}
	w.syncWaitersMu.Lock()
	chs := w.syncWaiters[key]
	delete(w.syncWaiters, key)
	w.syncWaitersMu.Unlock()
	for _, ch := range chs {
		close(ch)
	}
}

// ProbeSync fans out parallel direct→gateway probes for the supplied
// candidates and reports whether at least one pair recovered.
//
// Behaviour
// ─────────
//   - Total wall time bounded by ctx. The executor passes a 5s timeout
//     derived from the user's r.Context(); client disconnect cancels
//     the inner HTTP requests via the same ctx.
//   - For each (cred, model) that is NOT already in-flight (i.e. the
//     background cycle() did not start it in the last 5 minutes),
//     ProbeSync runs a new direct probe under nodeProbeSyncFanout
//     concurrent goroutines. The first direct.ok pair immediately
//     promotes to a gateway round; if the gateway round also passes
//     the function returns true.
//   - For each (cred, model) that IS in-flight, ProbeSync waits on a
//     per-key channel that cycle() closes at runOne completion. This
//     keeps concurrent no_candidate requests from issuing duplicate
//     probes against the same credential.
//   - The direct round's state-cache writes (updateObservedState +
//     invalidateCandidateCache) are issued the same way cycle() does
//     them, so the routing layer's next PlanCandidates sees the new
//     availability. ProbeSync does NOT modify node_probe_state — the
//     background backoff ladder is the worker's job, not the request's.
//   - Audit rows are written with trigger_kind="sync_request" so
//     dashboards can distinguish synchronous probes from the
//     request_failure / tick-driven ones.
//
// Returns true iff at least one (cred, model) is reachable directly from the
// supplier. The gateway round is still executed and recorded for diagnostics,
// but a gateway-side probe failure must not hide a recovered supplier node
// from the request router.
func (w *NodeProbeWorker) ProbeSync(
	ctx context.Context,
	candidates []credentialstate.NoCandidatesCandidate,
	tenantID string,
	parentReqID string,
) bool {
	start := time.Now()
	defer func() {
		nodeProbeSyncDuration.Observe(time.Since(start).Seconds())
	}()

	if w == nil || len(candidates) == 0 {
		nodeProbeSyncTotal.WithLabelValues("skipped").Inc()
		return false
	}

	type syncJob struct {
		key    string
		credID int
		model  string
		reuse  bool // wait for in-flight instead of running new
		waitCh chan struct{}
	}

	jobs := make([]syncJob, 0, len(candidates))
	freshJobs := make([]syncJob, 0, len(candidates))
	for _, c := range candidates {
		if c.CredentialID == 0 || strings.TrimSpace(c.RawModel) == "" {
			continue
		}
		key := fmt.Sprintf("%d|%s", c.CredentialID, c.RawModel)

		w.mu.Lock()
		_, busy := w.inFlight[key]
		w.mu.Unlock()

		if busy {
			ch := make(chan struct{})
			w.syncWaitersMu.Lock()
			w.syncWaiters[key] = append(w.syncWaiters[key], ch)
			w.syncWaitersMu.Unlock()
			nodeProbeSyncInflightWaiters.Inc()
			jobs = append(jobs, syncJob{key: key, credID: c.CredentialID, model: c.RawModel, reuse: true, waitCh: ch})
			continue
		}

		// Reserve the in-flight slot ourselves so a concurrent
		// background cycle() does not race us into a duplicate probe.
		w.mu.Lock()
		w.inFlight[key] = struct{}{}
		w.triggers[key] = nodeProbeTrigger{tenantID: tenantID, parentID: parentReqID}
		w.mu.Unlock()

		freshJobs = append(freshJobs, syncJob{key: key, credID: c.CredentialID, model: c.RawModel})
		jobs = append(jobs, freshJobs[len(freshJobs)-1])
	}
	defer func() {
		// Release in-flight slots we reserved for fresh jobs only.
		// Reuse jobs were never inserted by us; cycle() owns their slot.
		for _, j := range freshJobs {
			w.mu.Lock()
			delete(w.inFlight, j.key)
			w.mu.Unlock()
		}
	}()

	type freshResult struct {
		job     syncJob
		direct  nodeProbeRoundResult
		gateway nodeProbeRoundResult
	}

	results := make(chan freshResult, len(freshJobs))
	sem := make(chan struct{}, nodeProbeSyncFanout)
	var wg sync.WaitGroup
	for _, j := range freshJobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Ensure syncWaiters for THIS key are released even if a
			// concurrent ProbeSync caller attached as a reuse waiter
			// after we reserved inFlight. cycle() would not fire for
			// this key (we own the slot), so without this notify the
			// waiter would burn its entire ctx budget.
			defer w.notifySyncWaiters(j.key)
			res := freshResult{job: j}
			res.direct = w.probeDirect(ctx, j.credID, j.model)
			if res.direct.ok {
				// CRITICAL: update state BEFORE probeGateway so the
				// routing layer sees the restored credential, not the
				// stale cooling state left by the original 5xx.
				w.updateBindingAvailability(ctx, j.credID, j.model, true, "")
				w.updateCredentialHealth(ctx, j.credID)
				w.updateObservedState(ctx, j.credID, j.model, true, "", time.Now())
				res.gateway = w.probeGateway(ctx, j.credID, j.model)
			} else {
				w.updateBindingAvailability(ctx, j.credID, j.model, false, res.direct.errCode)
				recoverAt := time.Now().Add(5 * time.Minute)
				w.updateObservedState(ctx, j.credID, j.model, false, res.direct.errCode, recoverAt)
			}
			if w.invalidateCandidateCache != nil {
				w.invalidateCandidateCache(j.credID)
			}
			w.emitSyncAudit(ctx, j.credID, j.model, res.direct, res.gateway, start, parentReqID)
			results <- res
		}()
	}

	var (
		winnerMu     sync.Mutex
		winnerDirect nodeProbeRoundResult
		winnerGw     nodeProbeRoundResult
		winnerSet    bool
	)

	doneFresh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneFresh)
	}()

drainLoop:
	for {
		select {
		case res := <-results:
			if !winnerSet && probeRecovered(res.direct) {
				winnerMu.Lock()
				if !winnerSet {
					winnerDirect = res.direct
					winnerGw = res.gateway
					winnerSet = true
				}
				winnerMu.Unlock()
			}
		case <-doneFresh:
			for {
				select {
				case res := <-results:
					if !winnerSet && probeRecovered(res.direct) {
						winnerMu.Lock()
						if !winnerSet {
							winnerDirect = res.direct
							winnerGw = res.gateway
							winnerSet = true
						}
						winnerMu.Unlock()
					}
				default:
					break drainLoop
				}
			}
		case <-ctx.Done():
			break drainLoop
		}
	}

	// Wait for reuse jobs (in-flight background probes OR fresh-job
	// goroutines from another ProbeSync caller) to finish, then re-check
	// availability so we can report recovery when cycle() / the other
	// caller wrote availability=true. Without this re-check the reuse
	// path always returned false even on a genuine recovery, producing
	// a spurious 503 for every dedup-reuse request.
	for _, j := range jobs {
		if !j.reuse {
			continue
		}
		select {
		case <-j.waitCh:
			nodeProbeSyncInflightWaiters.Dec()
			// A background / sibling probe finished. Did it actually
			// mark this (cred,model) available? Re-check the cache.
			if !winnerSet && w.stateProvider != nil {
				if avail, _ := w.stateProvider.IsAvailable(ctx, j.credID, j.model); avail {
					winnerMu.Lock()
					if !winnerSet {
						winnerSet = true
						// We don't have the probe round result here;
						// synthesize a minimal winner record so the
						// success-path log line still fires.
						winnerDirect = nodeProbeRoundResult{
							ok:            true,
							providerID:    j.credID,
							outboundModel: j.model,
						}
						winnerGw = nodeProbeRoundResult{ok: true}
					}
					winnerMu.Unlock()
				}
			}
		case <-ctx.Done():
			nodeProbeSyncInflightWaiters.Dec()
		}
	}

	if winnerSet {
		nodeProbeSyncTotal.WithLabelValues("recovered").Inc()
		slog.Info("node_probe_worker: sync probe recovered",
			"credential_id", winnerDirect.providerID,
			"outbound_model", winnerDirect.outboundModel,
			"direct_status", winnerDirect.httpStatus,
			"gateway_status", winnerGw.httpStatus,
			"tenant_id", tenantID,
			"parent_request_id", parentReqID,
			"elapsed_ms", time.Since(start).Milliseconds(),
		)
		return true
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			nodeProbeSyncTotal.WithLabelValues("client_cancel").Inc()
		} else {
			nodeProbeSyncTotal.WithLabelValues("timeout").Inc()
		}
	} else {
		nodeProbeSyncTotal.WithLabelValues("exhausted").Inc()
	}
	slog.Info("node_probe_worker: sync probe exhausted",
		"tenant_id", tenantID,
		"parent_request_id", parentReqID,
		"candidates", len(jobs),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return false
}

// probeRecovered reports whether the supplier can serve the credential/model
// pair again. The direct round is authoritative for routing recovery because
// it isolates supplier availability from gateway-side probe failures. The
// gateway round remains an audit signal and is still emitted to diagnostics.
func probeRecovered(direct nodeProbeRoundResult) bool {
	return direct.ok
}

// emitSyncAudit writes a node_probe_runs row for a sync_request probe
// without touching node_probe_state. The columns mirror runOne() so
// downstream dashboards can filter by trigger_kind="sync_request".
func (w *NodeProbeWorker) emitSyncAudit(
	ctx context.Context,
	credID int,
	model string,
	direct nodeProbeRoundResult,
	gw nodeProbeRoundResult,
	startedAt time.Time,
	parentReqID string,
) {
	if w.db == nil {
		return
	}
	now := time.Now()
	durationMs := int(now.Sub(startedAt).Milliseconds())
	success := direct.ok && gw.ok
	cleaned := make(map[string]string, len(direct.requestHeaders))
	for k, v := range direct.requestHeaders {
		switch k {
		case "Authorization", "X-Api-Key", "x-api-key":
			continue
		}
		cleaned[k] = v
	}
	requestHeadersJSON, _ := json.Marshal(cleaned)

	_, _ = w.db.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms,
			api_model, outbound_model, provider_id,
			request_url, request_headers, request_body, response_body,
			timeout_at_ms, via_proxy,
			trigger_request_id
		) VALUES (
			$1, $2, 'sync_request', 1, 0,
			$3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19,
			$20, $21, $22, $23,
			$24, $25,
			$26
		)
	`,
		credID, model,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
		model, direct.outboundModel, direct.providerID,
		direct.requestURL, requestHeadersJSON, direct.requestBody, direct.responseBody,
		direct.latencyMs, direct.viaProxy,
		parentReqID,
	)
}

func (w *NodeProbeWorker) drainDue(ctx context.Context) {
	for i := 0; i < nodeProbeBatchSize; i++ {
		if !w.cycle(ctx) {
			return
		}
	}
}

// worker is in a low-traffic hot path and we want predictable DB pressure.
//
// Cross-instance isolation: pickDueAtomically issues a single
// SELECT ... FOR UPDATE SKIP LOCKED + UPDATE in_flight_until inside
// a transaction, so two gateway instances cannot both pick the same
// (cred, model) row at the same instant.  Combined with the in-memory
// dedup map this guarantees "one in-flight probe per (cred, model)
// globally".
func (w *NodeProbeWorker) cycle(ctx context.Context) bool {
	credID, model, ok, err := w.pickDueAtomically(ctx)
	if err != nil {
		slog.Warn("node_probe_worker: pick due failed", "error", err)
		return false
	}
	if !ok {
		return false
	}
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	if _, busy := w.inFlight[key]; busy {
		w.mu.Unlock()
		return false
	}
	w.inFlight[key] = struct{}{}
	trigger := w.triggers[key]
	delete(w.triggers, key)
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.inFlight, key)
		w.mu.Unlock()
	}()

	if err := w.runOne(ctx, credID, model, "", trigger); err != nil {
		slog.Warn("node_probe_worker: runOne failed",
			"credential_id", credID, "model", model, "error", err)
	}

	// Notify any ProbeSync callers that were waiting on this in-flight
	// background probe to complete (dedup reuse). Closing happens AFTER
	// runOne so the state-manager cache writes are visible to a waiter's
	// subsequent re-PlanCandidates.
	w.notifySyncWaiters(key)
	return true
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
// 2026-07-16: in_flight_until is a post-pick lease, not a submit-time delay.
// Submit leaves it NULL, so filtering it here prevents cross-instance duplicate
// probes without delaying a newly submitted row.
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
			  AND (in_flight_until IS NULL OR in_flight_until <= now())

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
func (w *NodeProbeWorker) runOne(ctx context.Context, credID int, model, triggerKind string, trigger nodeProbeTrigger) error {
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
	// CRITICAL: update state BEFORE probeGateway so the routing layer
	// sees the restored credential, not the stale cooling state from
	// the original 5xx or transient failure.
	if direct.ok {
		w.updateBindingAvailability(ctx, credID, model, true, "")
		w.updateCredentialHealth(ctx, credID)
		w.updateObservedState(ctx, credID, model, true, "", time.Now())
		if w.recordCircuitSuccess != nil {
			w.recordCircuitSuccess(direct.providerID, credID)
		}
	}
	// Round 2: gateway — now sees the restored state from the direct round
	gw := w.probeGateway(ctx, credID, model)

	success := direct.ok && gw.ok
	w.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "direct", attempt, trigger, direct)
	w.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "gateway", attempt, trigger, gw)
	if !direct.ok {
		w.updateBindingAvailability(ctx, credID, model, false, direct.errCode)
		recoverAt := time.Now().Add(5 * time.Minute)
		w.updateObservedState(ctx, credID, model, false, direct.errCode, recoverAt)
	}
	if w.invalidateCandidateCache != nil {
		w.invalidateCandidateCache(credID)
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

	// 准备新字段数据
	var requestHeadersJSON []byte
	if len(direct.requestHeaders) > 0 {
		requestHeadersJSON, _ = json.Marshal(direct.requestHeaders)
	}

	timeoutAtMs := 0
	if direct.errCode == "network_error" && direct.latencyMs >= 14900 {
		timeoutAtMs = direct.latencyMs
	}

	_, _ = w.db.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms,
			api_model, outbound_model, provider_id, 
			request_url, request_headers, request_body, response_body, 
			timeout_at_ms, via_proxy
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19,
			$20, $21, $22,
			$23, $24, $25, $26,
			$27, $28
		)`,
		credID, model, triggerKind, attempt, nextSec,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
		model, direct.outboundModel, direct.providerID,
		direct.requestURL, requestHeadersJSON, direct.requestBody, direct.responseBody,
		timeoutAtMs, direct.viaProxy,
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
	ok            bool
	providerID    int
	outboundModel string
	httpStatus    int
	errCode       string
	errDetail     string
	latencyMs     int
	timedOut      bool // true if context deadline or client timeout triggered
	// 2026-07-16: 新增详细字段用于完整记录probe过程
	requestURL     string
	requestHeaders map[string]string // 已脱敏（不含Authorization/x-api-key）
	requestBody    string
	responseBody   string // 前512字节
	viaProxy       bool   // probeDirect是否通过代理
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
	plain, outboundModel, baseURL, protocol, providerID, err := w.resolveDirectTarget(ctx, credID, model)
	if err != nil {
		r.errCode = "endpoint_build"
		r.errDetail = fmt.Sprintf("build endpoint failed: %s (cred_id=%d, model=%s)", err.Error(), credID, model)
		return r
	}
	r.providerID = providerID
	r.outboundModel = outboundModel
	// Use the outbound name for the upstream body; fall back to the raw name
	// passed in (matches active_probe_executor.go's OutboundModel→RawModel
	// fallback) so providers without a distinct outbound_model_name still work.
	bodyModel := outboundModel
	if bodyModel == "" {
		bodyModel = model
	}
	endpoint := directProbeEndpoint(baseURL, protocol)
	body := directProbeBody(bodyModel, protocol)

	// 2026-07-16: 记录请求详情
	r.requestURL = endpoint
	r.requestBody = body
	r.viaProxy = (w.probeClient != w.client) // 如果probeClient与client不同，说明使用了代理

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		r.errCode = "request_build"
		r.errDetail = fmt.Sprintf("build request failed: %s (url=%s)", err.Error(), endpoint)
		return r
	}
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

	// 记录请求头（脱敏）
	r.requestHeaders = make(map[string]string)
	for k, v := range req.Header {
		// 脱敏：不记录Authorization和x-api-key
		if k != "Authorization" && k != "X-Api-Key" && k != "x-api-key" {
			r.requestHeaders[k] = strings.Join(v, ", ")
		}
	}

	start := time.Now()
	// 2026-07-16 fix: use probeClient (proxy-respecting) instead of client
	// (direct). probeDirect previously bypassed the proxy, reporting
	// false-positive recoveries when the proxy was broken — the credential
	// oscillated between "available" (probe OK, no proxy) and "unavailable"
	// (real requests through proxy → timeout), causing the "models briefly
	// work then 5xx" pattern for non-domestic providers like apiclaude.cc
	// and integrate.api.nvidia.com.
	resp, err := w.probeClient.Do(req)
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		r.errCode = "network_error"
		if errors.Is(err, context.DeadlineExceeded) {
			r.timedOut = true
			r.errDetail = fmt.Sprintf("upstream timeout after %ds (cred_id=%d, url=%s, model=%s)", int(w.client.Timeout/time.Second), credID, endpoint, bodyModel)
		} else if ue, ok := err.(*url.Error); ok && ue.Timeout() {
			r.timedOut = true
			r.errDetail = fmt.Sprintf("upstream timeout after %ds (cred_id=%d, url=%s, model=%s)", int(w.client.Timeout/time.Second), credID, endpoint, bodyModel)
		} else {
			r.errDetail = fmt.Sprintf("upstream call failed: %s (cred_id=%d, url=%s, model=%s)", err.Error(), credID, endpoint, bodyModel)
		}
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode

	// 读取响应body（前512字节）
	respBuf := make([]byte, 512)
	n, _ := resp.Body.Read(respBuf)
	r.responseBody = string(respBuf[:n])

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	statusText := strings.TrimSpace(string(respBuf[:n]))
	// Trim trailing newlines/whitespace from status text
	if idx := strings.IndexAny(statusText, "\n\r"); idx >= 0 {
		statusText = statusText[:idx]
	}
	r.errDetail = fmt.Sprintf("upstream returned HTTP %d %s (cred_id=%d, url=%s, model=%s)", resp.StatusCode, statusText, credID, endpoint, bodyModel)
	return r
}

func (w *NodeProbeWorker) emitProbe(ctx context.Context, credID, providerID int, model, outboundModel, origin string, attempt int, trigger nodeProbeTrigger, result nodeProbeRoundResult) {
	if w == nil || w.emitter == nil {
		return
	}
	// 2026-07-17 (audit fix): recover the precise ProbeStatus from the
	// round result instead of collapsing every failure to
	// ProbeStatusFailed. Without this, a 429/503/network error on the
	// node_probe path was classified as probe_direct_internal_error /
	// failure_stage="gateway" — i.e. the exact misattribution this whole
	// change set was meant to eliminate. The errCode produced by
	// probeDirect/probeGateway encodes the failure kind:
	//   endpoint_build  -> gateway-side (decrypt/resolve) -> Failed
	//   network_error   -> Network (request_build/transport)
	//   http_<status>   -> mapped from the upstream status code
	status := nodeProbeResultToStatus(result)
	// 2026-07-17: forward the diagnostic detail already collected by
	// probeDirect/probeGateway so the emitter can surface the real
	// request/response context in auto_decision + synthetic traces.
	// Previously these fields were dropped here, which is why probe rows
	// showed only headers on the dashboard.
	w.emitter.Emit(ctx, credID, providerID, trigger.tenantID, model, outboundModel, origin, trigger.parentID, attempt, &ProbeResult{
		Status:       status,
		HTTPStatus:   result.httpStatus,
		ErrCode:      result.errCode,
		ErrMsg:       result.errDetail,
		LatencyMs:    result.latencyMs,
		RespPreview:  result.responseBody,
		ResponseBody: result.responseBody,
		RequestURL:   result.requestURL,
		RequestBody:  result.requestBody,
		ViaProxy:     result.viaProxy,
		StartedAt:    time.Now().Add(-time.Duration(result.latencyMs) * time.Millisecond),
		CompletedAt:  time.Now(),
	})
}

// nodeProbeResultToStatus maps a nodeProbeRoundResult (ok + httpStatus +
// errCode) onto the fine-grained ProbeStatus taxonomy that
// classifyProbeErrorKind / classifyProbeFailureStage consume. Mirrors the
// HTTP-status mapping in ActiveProbeExecutor.Run so both probe paths emit
// the same error_kind for the same upstream behaviour (e.g. a 429 is
// probe_direct_rate_limited on either path).
func nodeProbeResultToStatus(r nodeProbeRoundResult) ProbeStatus {
	if r.ok {
		return ProbeStatusSuccess
	}
	switch r.errCode {
	case "endpoint_build":
		return ProbeStatusFailed // gateway-side build error (decrypt/resolve)
	case "network_error":
		// Transport failure. A request that hit the 15s client timeout is
		// reported by probeDirect/probeGateway with timedOut=true.
		if r.timedOut {
			return ProbeStatusTimeout
		}
		return ProbeStatusNetwork
	}
	// errCode is "http_<status>" for non-200 upstream responses.
	if r.httpStatus == 401 || r.httpStatus == 403 {
		return ProbeStatusAuth
	}
	if r.httpStatus == 429 {
		return ProbeStatusRate
	}
	if r.httpStatus >= 500 {
		return ProbeStatusHTTP5xx
	}
	if r.httpStatus >= 400 {
		return ProbeStatusHTTP4xx
	}
	return ProbeStatusFailed
}

func (w *NodeProbeWorker) resolveDirectTarget(ctx context.Context, credID int, model string) (string, string, string, string, int, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		ciphertext    []byte
		outboundModel string
		baseURL       string
		protocol      string
		providerID    int
	)
	err := w.db.QueryRow(queryCtx, `
		SELECT c.secret_ciphertext,
		       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
		       p.base_url, COALESCE(p.protocol, 'openai-completions'), p.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2
		  AND c.status IN ('active', 'cooling', 'degraded')
		  AND c.lifecycle_status = 'active'
		  AND p.enabled = TRUE AND p.manual_disabled = FALSE
		LIMIT 1
	`, credID, model).Scan(&ciphertext, &outboundModel, &baseURL, &protocol, &providerID)
	if err != nil {
		return "", "", "", "", 0, err
	}
	s := string(ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", "", "", "", 0, fmt.Errorf("unsupported secret format")
	}
	if w.keyring == nil {
		return "", "", "", "", 0, fmt.Errorf("keyring not configured")
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
		return "", "", "", "", 0, fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), outboundModel, baseURL, protocol, providerID, nil
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
	endpoint := w.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		r.errCode = "request_build"
		r.errDetail = err.Error()
		return r
	}
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
	// 2026-07-17: record request context for diagnostics (mirrors probeDirect).
	r.requestURL = endpoint
	r.requestBody = body
	r.viaProxy = false // gateway probe uses the direct (non-proxy) client
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
	// Read the body for both success and failure so success-path usage
	// parsing (emitter.parseProbeUsage) has something to work with,
	// instead of reporting hardcoded token counts.
	respBuf := make([]byte, 512)
	n, _ := resp.Body.Read(respBuf)
	r.responseBody = string(respBuf[:n])
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	if n > 0 {
		r.errDetail = r.responseBody
		if len(r.errDetail) > 256 {
			r.errDetail = r.errDetail[:256]
		}
	}
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
